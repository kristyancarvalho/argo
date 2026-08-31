package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/scheduler"
)

const DefaultMaximumConcurrentDownloads = 3

type Store interface {
	CreateDownload(context.Context, model.Download) error
	Download(context.Context, model.DownloadID) (model.Download, error)
	Downloads(context.Context) ([]model.Download, error)
	RecoverActiveDownloads(context.Context, time.Time) error
	UpdateDownloadPriority(context.Context, model.DownloadID, model.Priority, time.Time) error
	UpdateDownloadStatus(context.Context, model.DownloadID, model.Status, time.Time, string) error
}

type DownloadEngine interface {
	Download(context.Context, model.Download) error
}

type DownloadRateController interface {
	SetRateLimit(int64) error
}

type ProfileStore interface {
	ActiveProfile(context.Context) (string, error)
	SetActiveProfile(context.Context, string) error
}

type NetworkObserver interface {
	Observe(context.Context, func(network.Snapshot) error) error
}

type ServiceOptions struct {
	MaximumConcurrentDownloads int
	NetworkObserver            NetworkObserver
	PauseOnMetered             bool
	ResumeAfterMetered         bool
	DefaultPriority            model.Priority
	Profiles                   map[string]Profile
}

type Profile struct {
	Name                       string
	BytesPerSecond             int64
	DefaultPriority            model.Priority
	MaximumConcurrentDownloads int
	PauseOnMetered             bool
	ResumeAfterMetered         bool
	Policy                     string
}

type priorityUpdate struct {
	identifier model.DownloadID
	priority   model.Priority
	completed  chan struct{}
}

type schedulerLimitUpdate struct {
	maximum   int
	completed chan struct{}
}

type Service struct {
	store              Store
	engine             DownloadEngine
	status             *ipc.StatusHandler
	jobs               chan scheduler.Entry
	priorities         chan priorityUpdate
	limits             chan schedulerLimitUpdate
	completed          chan model.DownloadID
	initial            []scheduler.Entry
	maximumActive      int
	ctx                context.Context
	cancel             context.CancelFunc
	waitGroup          sync.WaitGroup
	closeOnce          sync.Once
	activeMutex        sync.Mutex
	activeCancels      map[model.DownloadID]context.CancelFunc
	networkObserver    NetworkObserver
	networkMutex       sync.RWMutex
	networkSnapshot    network.Snapshot
	networkAvailable   bool
	pauseOnMetered     bool
	resumeAfterMetered bool
	meteredMutex       sync.Mutex
	meteredPaused      map[model.DownloadID]struct{}
	defaultPriority    model.Priority
	profileMutex       sync.RWMutex
	profiles           map[string]Profile
	activeProfile      string
	profileStore       ProfileStore
	rateController     DownloadRateController
}

func NewService(parent context.Context, store Store, engine DownloadEngine) (*Service, error) {
	return NewServiceWithOptions(parent, store, engine, ServiceOptions{})
}

func NewServiceWithOptions(
	parent context.Context,
	store Store,
	engine DownloadEngine,
	options ServiceOptions,
) (*Service, error) {
	if options.MaximumConcurrentDownloads == 0 {
		options.MaximumConcurrentDownloads = DefaultMaximumConcurrentDownloads
	}
	if options.MaximumConcurrentDownloads < 0 {
		return nil, fmt.Errorf("maximum concurrent downloads must be positive")
	}
	if options.DefaultPriority == "" {
		options.DefaultPriority = model.PriorityNormal
	}
	if _, err := model.ParsePriority(string(options.DefaultPriority)); err != nil {
		return nil, err
	}
	profiles := make(map[string]Profile, len(options.Profiles))
	for name, profile := range options.Profiles {
		if profile.Name == "" {
			profile.Name = name
		}
		if profile.Name != name || profile.BytesPerSecond < 0 || profile.MaximumConcurrentDownloads <= 0 {
			return nil, fmt.Errorf("invalid profile %q", name)
		}
		if _, err := model.ParsePriority(string(profile.DefaultPriority)); err != nil {
			return nil, fmt.Errorf("invalid profile %q: %w", name, err)
		}
		if profile.ResumeAfterMetered && !profile.PauseOnMetered {
			return nil, fmt.Errorf("invalid profile %q: resume after metered requires pause on metered", name)
		}
		profiles[name] = profile
	}
	profileStore, supportsProfiles := store.(ProfileStore)
	rateController, controlsRate := engine.(DownloadRateController)
	activeProfile := ""
	if supportsProfiles {
		var err error
		activeProfile, err = profileStore.ActiveProfile(parent)
		if err != nil {
			return nil, err
		}
	}
	if activeProfile != "" {
		profile, exists := profiles[activeProfile]
		if !exists {
			return nil, UnknownProfileError{Name: activeProfile}
		}
		if !controlsRate {
			return nil, fmt.Errorf("download engine cannot apply profiles")
		}
		if err := rateController.SetRateLimit(profile.BytesPerSecond); err != nil {
			return nil, err
		}
		options.MaximumConcurrentDownloads = profile.MaximumConcurrentDownloads
		options.DefaultPriority = profile.DefaultPriority
		options.PauseOnMetered = profile.PauseOnMetered
		options.ResumeAfterMetered = profile.ResumeAfterMetered
	}
	if len(profiles) > 0 && (!supportsProfiles || !controlsRate) {
		return nil, fmt.Errorf("service dependencies cannot apply profiles")
	}
	if err := store.RecoverActiveDownloads(parent, time.Now().UTC()); err != nil {
		return nil, err
	}
	downloads, err := store.Downloads(parent)
	if err != nil {
		return nil, err
	}
	initial := make([]scheduler.Entry, 0)
	for _, download := range downloads {
		if download.Status == model.StatusQueued {
			initial = append(initial, scheduler.Entry{ID: download.ID, Priority: download.Priority})
		}
	}

	ctx, cancel := context.WithCancel(parent)
	service := &Service{
		store:              store,
		engine:             engine,
		status:             ipc.NewStatusHandler(),
		jobs:               make(chan scheduler.Entry),
		priorities:         make(chan priorityUpdate),
		limits:             make(chan schedulerLimitUpdate),
		completed:          make(chan model.DownloadID),
		initial:            initial,
		maximumActive:      options.MaximumConcurrentDownloads,
		ctx:                ctx,
		cancel:             cancel,
		activeCancels:      make(map[model.DownloadID]context.CancelFunc),
		networkObserver:    options.NetworkObserver,
		pauseOnMetered:     options.PauseOnMetered,
		resumeAfterMetered: options.ResumeAfterMetered,
		meteredPaused:      make(map[model.DownloadID]struct{}),
		defaultPriority:    options.DefaultPriority,
		profiles:           profiles,
		activeProfile:      activeProfile,
		profileStore:       profileStore,
		rateController:     rateController,
	}
	service.waitGroup.Add(1)
	go service.runScheduler()
	if service.networkObserver != nil {
		service.waitGroup.Add(1)
		go service.runNetworkObserver()
	}

	return service, nil
}

func (service *Service) Handle(ctx context.Context, request ipc.Request) (any, error) {
	switch request.Operation {
	case ipc.OperationStatus:
		return service.statusResponse(), nil
	case ipc.OperationAdd:
		return service.add(ctx, request.Payload)
	case ipc.OperationPause:
		return service.pause(ctx, request.Payload)
	case ipc.OperationResume:
		return service.resume(ctx, request.Payload)
	case ipc.OperationCancel:
		return service.cancelDownload(ctx, request.Payload)
	case ipc.OperationPriority:
		return service.setPriority(ctx, request.Payload)
	case ipc.OperationList:
		return service.list(ctx)
	case ipc.OperationShow:
		return service.show(ctx, request.Payload)
	case ipc.OperationProfile:
		return service.setProfile(ctx, request.Payload)
	default:
		return nil, ipc.UnsupportedOperationError{Operation: request.Operation}
	}
}

func (service *Service) statusResponse() ipc.Status {
	status := service.status.Status()
	service.networkMutex.RLock()
	snapshot := service.networkSnapshot
	available := service.networkAvailable
	service.networkMutex.RUnlock()
	status.Network = ipc.NetworkStatus{
		Available:        available,
		Connected:        snapshot.Connected,
		State:            string(snapshot.State),
		Connectivity:     string(snapshot.Connectivity),
		ActiveConnection: snapshot.ActiveConnection,
		ConnectionType:   snapshot.ConnectionType,
		Interface:        snapshot.Interface,
		Metered:          string(snapshot.Metered),
	}
	service.profileMutex.RLock()
	status.ActiveProfile = service.activeProfile
	service.profileMutex.RUnlock()

	return status
}

func (service *Service) Close() error {
	service.closeOnce.Do(func() {
		service.cancel()
		service.waitGroup.Wait()
	})

	return nil
}

func (service *Service) add(ctx context.Context, payload json.RawMessage) (ipc.AddResponse, error) {
	var request ipc.AddRequest
	if err := decodePayload(payload, &request); err != nil {
		return ipc.AddResponse{}, InvalidAddRequestError{Reason: err.Error()}
	}

	download, err := newDownload(request, service.currentDefaultPriority())
	if err != nil {
		return ipc.AddResponse{}, err
	}
	if err := service.store.CreateDownload(ctx, download); err != nil {
		return ipc.AddResponse{}, fmt.Errorf("persist added download: %w", err)
	}
	if err := service.enqueue(ctx, download.ID, download.Priority); err != nil {
		now := time.Now().UTC()
		statusErr := service.store.UpdateDownloadStatus(
			context.WithoutCancel(ctx),
			download.ID,
			model.StatusCanceled,
			now,
			err.Error(),
		)
		return ipc.AddResponse{}, errors.Join(err, statusErr)
	}

	return ipc.AddResponse{
		ID:          download.ID.String(),
		Filename:    download.Filename,
		Destination: download.Destination,
		Status:      string(download.Status),
	}, nil
}

func (service *Service) pause(ctx context.Context, payload json.RawMessage) (ipc.DownloadActionResponse, error) {
	identifier, download, err := service.actionDownload(ctx, payload)
	if err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	if download.Status == model.StatusPaused {
		service.forgetMeteredPause(download.ID)
		return actionResponse(download.ID, model.StatusPaused), nil
	}
	switch download.Status {
	case model.StatusQueued, model.StatusResolving, model.StatusDownloading:
	case model.StatusPaused:
		return actionResponse(download.ID, model.StatusPaused), nil
	case model.StatusCompleted, model.StatusFailed, model.StatusCanceled:
		return ipc.DownloadActionResponse{}, InvalidDownloadActionError{
			ID:     download.ID.String(),
			Action: "pause",
			Status: download.Status,
		}
	}
	if err := service.store.UpdateDownloadStatus(
		ctx,
		identifier,
		model.StatusPaused,
		time.Now().UTC(),
		"",
	); err != nil {
		return ipc.DownloadActionResponse{}, err
	}

	service.cancelActive(identifier)
	service.forgetMeteredPause(identifier)

	return actionResponse(identifier, model.StatusPaused), nil
}

func (service *Service) resume(ctx context.Context, payload json.RawMessage) (ipc.DownloadActionResponse, error) {
	identifier, download, err := service.actionDownload(ctx, payload)
	if err != nil {
		return ipc.DownloadActionResponse{}, err
	}

	var status model.Status
	switch download.Status {
	case model.StatusPaused:
		status = model.StatusDownloading
	case model.StatusFailed:
		status = model.StatusQueued
	case model.StatusQueued, model.StatusResolving, model.StatusDownloading:
		return actionResponse(identifier, download.Status), nil
	case model.StatusCompleted, model.StatusCanceled:
		return ipc.DownloadActionResponse{}, InvalidDownloadActionError{
			ID:     download.ID.String(),
			Action: "resume",
			Status: download.Status,
		}
	}
	if err := service.store.UpdateDownloadStatus(ctx, identifier, status, time.Now().UTC(), ""); err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	if err := service.enqueue(ctx, identifier, download.Priority); err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	service.forgetMeteredPause(identifier)

	return actionResponse(identifier, status), nil
}

func (service *Service) actionDownload(
	ctx context.Context,
	payload json.RawMessage,
) (model.DownloadID, model.Download, error) {
	var request ipc.DownloadActionRequest
	if err := decodePayload(payload, &request); err != nil {
		return "", model.Download{}, InvalidDownloadActionError{Action: "decode", Reason: err.Error()}
	}
	identifier, err := model.ParseDownloadID(request.ID)
	if err != nil {
		return "", model.Download{}, InvalidDownloadActionError{ID: request.ID, Action: "decode", Reason: err.Error()}
	}
	download, err := service.store.Download(ctx, identifier)
	if err != nil {
		return "", model.Download{}, err
	}

	return identifier, download, nil
}

func (service *Service) setPriority(
	ctx context.Context,
	payload json.RawMessage,
) (ipc.PriorityResponse, error) {
	var request ipc.PriorityRequest
	if err := decodePayload(payload, &request); err != nil {
		return ipc.PriorityResponse{}, InvalidDownloadActionError{Action: "priority", Reason: err.Error()}
	}
	identifier, err := model.ParseDownloadID(request.ID)
	if err != nil {
		return ipc.PriorityResponse{}, InvalidDownloadActionError{
			ID:     request.ID,
			Action: "priority",
			Reason: err.Error(),
		}
	}
	priority, err := model.ParsePriority(request.Priority)
	if err != nil {
		return ipc.PriorityResponse{}, err
	}
	if err := service.store.UpdateDownloadPriority(ctx, identifier, priority, time.Now().UTC()); err != nil {
		return ipc.PriorityResponse{}, err
	}
	if err := service.updateScheduledPriority(ctx, identifier, priority); err != nil {
		return ipc.PriorityResponse{}, err
	}

	return ipc.PriorityResponse{ID: identifier.String(), Priority: string(priority)}, nil
}

func (service *Service) cancelDownload(
	ctx context.Context,
	payload json.RawMessage,
) (ipc.DownloadActionResponse, error) {
	identifier, download, err := service.actionDownload(ctx, payload)
	if err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	if download.Status == model.StatusCanceled {
		return actionResponse(identifier, model.StatusCanceled), nil
	}
	switch download.Status {
	case model.StatusQueued,
		model.StatusResolving,
		model.StatusDownloading,
		model.StatusPaused,
		model.StatusFailed:
	case model.StatusCompleted:
		return ipc.DownloadActionResponse{}, InvalidDownloadActionError{
			ID:     identifier.String(),
			Action: "cancel",
			Status: download.Status,
		}
	case model.StatusCanceled:
		return actionResponse(identifier, model.StatusCanceled), nil
	}
	if err := service.store.UpdateDownloadStatus(
		ctx,
		identifier,
		model.StatusCanceled,
		time.Now().UTC(),
		"",
	); err != nil {
		return ipc.DownloadActionResponse{}, err
	}

	service.cancelActive(identifier)
	service.forgetMeteredPause(identifier)

	return actionResponse(identifier, model.StatusCanceled), nil
}

func (service *Service) list(ctx context.Context) (ipc.ListResponse, error) {
	downloads, err := service.store.Downloads(ctx)
	if err != nil {
		return ipc.ListResponse{}, err
	}
	response := ipc.ListResponse{Downloads: make([]ipc.Download, 0, len(downloads))}
	for _, download := range downloads {
		response.Downloads = append(response.Downloads, downloadResponse(download))
	}

	return response, nil
}

func (service *Service) show(ctx context.Context, payload json.RawMessage) (ipc.Download, error) {
	var request ipc.ShowRequest
	if err := decodePayload(payload, &request); err != nil {
		return ipc.Download{}, InvalidDownloadActionError{Action: "show", Reason: err.Error()}
	}
	identifier, err := model.ParseDownloadID(request.ID)
	if err != nil {
		return ipc.Download{}, InvalidDownloadActionError{ID: request.ID, Action: "show", Reason: err.Error()}
	}
	download, err := service.store.Download(ctx, identifier)
	if err != nil {
		return ipc.Download{}, err
	}

	return downloadResponse(download), nil
}

func (service *Service) enqueue(
	ctx context.Context,
	identifier model.DownloadID,
	priority model.Priority,
) error {
	select {
	case service.jobs <- scheduler.Entry{ID: identifier, Priority: priority}:
		return nil
	case <-service.ctx.Done():
		return fmt.Errorf("daemon is shutting down")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Service) updateScheduledPriority(
	ctx context.Context,
	identifier model.DownloadID,
	priority model.Priority,
) error {
	update := priorityUpdate{
		identifier: identifier,
		priority:   priority,
		completed:  make(chan struct{}),
	}
	select {
	case service.priorities <- update:
	case <-service.ctx.Done():
		return fmt.Errorf("daemon is shutting down")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-update.completed:
		return nil
	case <-service.ctx.Done():
		return fmt.Errorf("daemon is shutting down")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Service) runScheduler() {
	defer service.waitGroup.Done()
	queue := scheduler.NewQueue(service.initial)
	maximumActive := service.maximumActive
	for {
		for queue.Active() < maximumActive {
			identifier, available := queue.Next()
			if !available {
				break
			}
			service.start(identifier)
		}
		select {
		case <-service.ctx.Done():
			return
		case entry := <-service.jobs:
			queue.Enqueue(entry)
		case update := <-service.priorities:
			queue.UpdatePriority(update.identifier, update.priority)
			close(update.completed)
		case update := <-service.limits:
			maximumActive = update.maximum
			close(update.completed)
		case identifier := <-service.completed:
			queue.Complete(identifier)
		}
	}
}

func (service *Service) setProfile(ctx context.Context, payload json.RawMessage) (ipc.ProfileResponse, error) {
	var request ipc.ProfileRequest
	if err := decodePayload(payload, &request); err != nil {
		return ipc.ProfileResponse{}, InvalidDownloadActionError{Action: "profile", Reason: err.Error()}
	}
	profile, exists := service.profiles[request.Name]
	if !exists {
		return ipc.ProfileResponse{}, UnknownProfileError{Name: request.Name}
	}
	if err := service.profileStore.SetActiveProfile(ctx, profile.Name); err != nil {
		return ipc.ProfileResponse{}, err
	}
	if err := service.rateController.SetRateLimit(profile.BytesPerSecond); err != nil {
		return ipc.ProfileResponse{}, err
	}
	service.profileMutex.Lock()
	service.activeProfile = profile.Name
	service.defaultPriority = profile.DefaultPriority
	service.pauseOnMetered = profile.PauseOnMetered
	service.resumeAfterMetered = profile.ResumeAfterMetered
	service.profileMutex.Unlock()
	if err := service.updateSchedulerLimit(ctx, profile.MaximumConcurrentDownloads); err != nil {
		return ipc.ProfileResponse{}, err
	}

	return profileResponse(profile), nil
}

func (service *Service) updateSchedulerLimit(ctx context.Context, maximum int) error {
	update := schedulerLimitUpdate{maximum: maximum, completed: make(chan struct{})}
	select {
	case service.limits <- update:
	case <-service.ctx.Done():
		return fmt.Errorf("daemon is shutting down")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-update.completed:
		return nil
	case <-service.ctx.Done():
		return fmt.Errorf("daemon is shutting down")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Service) currentDefaultPriority() model.Priority {
	service.profileMutex.RLock()
	defer service.profileMutex.RUnlock()

	return service.defaultPriority
}

func profileResponse(profile Profile) ipc.ProfileResponse {
	return ipc.ProfileResponse{
		Name:                   profile.Name,
		BytesPerSecond:         profile.BytesPerSecond,
		DefaultPriority:        string(profile.DefaultPriority),
		MaxConcurrentDownloads: profile.MaximumConcurrentDownloads,
		PauseOnMetered:         profile.PauseOnMetered,
		ResumeAfterMetered:     profile.ResumeAfterMetered,
		Policy:                 profile.Policy,
	}
}

func (service *Service) start(identifier model.DownloadID) {
	downloadContext, cancel := context.WithCancel(service.ctx)
	service.activeMutex.Lock()
	service.activeCancels[identifier] = cancel
	service.activeMutex.Unlock()
	service.waitGroup.Add(1)
	go service.process(downloadContext, identifier, cancel)
}

func (service *Service) process(
	downloadContext context.Context,
	identifier model.DownloadID,
	cancel context.CancelFunc,
) {
	defer service.waitGroup.Done()
	defer func() {
		cancel()
		service.activeMutex.Lock()
		delete(service.activeCancels, identifier)
		service.activeMutex.Unlock()
		select {
		case service.completed <- identifier:
		case <-service.ctx.Done():
		}
	}()
	download, err := service.store.Download(service.ctx, identifier)
	if err != nil {
		return
	}
	if download.Status != model.StatusQueued && download.Status != model.StatusDownloading {
		return
	}

	_ = service.engine.Download(downloadContext, download)
}

func (service *Service) cancelActive(identifier model.DownloadID) {
	service.activeMutex.Lock()
	if cancel := service.activeCancels[identifier]; cancel != nil {
		cancel()
	}
	service.activeMutex.Unlock()
}

func decodePayload(payload json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid payload")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("payload contains trailing data")
	}

	return nil
}

func actionResponse(identifier model.DownloadID, status model.Status) ipc.DownloadActionResponse {
	return ipc.DownloadActionResponse{ID: identifier.String(), Status: string(status)}
}

func downloadResponse(download model.Download) ipc.Download {
	return ipc.Download{
		ID:              download.ID.String(),
		URL:             download.URL,
		Destination:     download.Destination,
		Filename:        download.Filename,
		TotalSize:       download.TotalSize,
		DownloadedBytes: download.DownloadedBytes,
		Status:          string(download.Status),
		Priority:        string(download.Priority),
		CreatedAt:       download.CreatedAt,
		UpdatedAt:       download.UpdatedAt,
		Error:           download.Error,
	}
}

func newDownload(request ipc.AddRequest, priority model.Priority) (model.Download, error) {
	parsedURL, err := url.Parse(request.URL)
	if err != nil {
		return model.Download{}, InvalidAddRequestError{Reason: "URL cannot be parsed"}
	}
	if (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return model.Download{}, InvalidAddRequestError{Reason: "URL must use HTTP or HTTPS and include a host"}
	}
	if request.Destination == "" {
		return model.Download{}, InvalidAddRequestError{Reason: "destination is required"}
	}
	destination, err := filepath.Abs(request.Destination)
	if err != nil {
		return model.Download{}, InvalidAddRequestError{Reason: "destination cannot be resolved"}
	}

	filename := path.Base(parsedURL.Path)
	filename, err = url.PathUnescape(filename)
	if err != nil {
		return model.Download{}, InvalidAddRequestError{Reason: "URL filename cannot be decoded"}
	}
	filename = filepath.Base(filename)
	if filename == "" || filename == "." || filename == string(filepath.Separator) || strings.TrimSpace(filename) == "" {
		filename = "download"
	}
	if filename == ".." {
		return model.Download{}, InvalidAddRequestError{Reason: "URL filename is unsafe"}
	}

	identifier, err := model.NewDownloadID()
	if err != nil {
		return model.Download{}, err
	}
	now := time.Now().UTC()

	return model.Download{
		ID:              identifier,
		URL:             parsedURL.String(),
		Destination:     destination,
		Filename:        filename,
		TotalSize:       -1,
		DownloadedBytes: 0,
		Status:          model.StatusQueued,
		Priority:        priority,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}
