package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/kristyancarvalho/argo/internal/diagnostic"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/scheduler"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

const DefaultMaximumConcurrentDownloads = 3

const (
	defaultListPageSize    = 100
	maximumListPageSize    = 200
	maximumSummaryFilename = 1024
)

type Store interface {
	CreateDownload(context.Context, model.Download) error
	DeleteDownload(context.Context, model.DownloadID) error
	ClearDownloadHistory(context.Context) ([]model.Download, error)
	Download(context.Context, model.DownloadID) (model.Download, error)
	Downloads(context.Context) ([]model.Download, error)
	DownloadPage(context.Context, model.DownloadCursor, int) ([]model.Download, model.DownloadCursor, bool, error)
	RecoverActiveDownloads(context.Context, time.Time) error
	UpdateDownloadPriority(context.Context, model.DownloadID, model.Priority, time.Time) error
	UpdateDownloadStatus(context.Context, model.DownloadID, model.Status, time.Time, string) error
}

type DownloadEngine interface {
	Download(context.Context, model.Download) error
}

type PartialCleaner interface {
	RemovePartial(model.DownloadID) error
}

type CanceledResumeValidator interface {
	ValidateCanceledResume(context.Context, model.Download) error
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

type NetworkReconnector interface {
	Reconnect(context.Context) error
}

type TelemetryObserver interface {
	Observe(context.Context, func(telemetry.Snapshot) error) error
}

type ServiceOptions struct {
	MaximumConcurrentDownloads int
	NetworkObserver            NetworkObserver
	PauseOnMetered             bool
	ResumeAfterMetered         bool
	DefaultPriority            model.Priority
	DefaultDestination         string
	Profiles                   map[string]Profile
	TrafficPolicy              qos.Policy
	TrafficLinkRate            uint64
	TrafficCgroup              qos.CgroupSelector
	TrafficBackend             qos.Backend
	TelemetryObserver          TelemetryObserver
	LatencyPolicy              *qos.LatencyPolicy
}

type Profile struct {
	Name                       string
	BytesPerSecond             int64
	DefaultPriority            model.Priority
	MaximumConcurrentDownloads int
	PauseOnMetered             bool
	ResumeAfterMetered         bool
	Policy                     string
	LatencyPolicy              *qos.LatencyPolicy
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
	closeErr           error
	activeMutex        sync.Mutex
	activeCancels      map[model.DownloadID]context.CancelFunc
	networkObserver    NetworkObserver
	networkReady       chan struct{}
	networkReadyOnce   sync.Once
	networkPolicyMutex sync.Mutex
	networkMutex       sync.RWMutex
	networkSnapshot    network.Snapshot
	networkAvailable   bool
	networkError       string
	pauseOnMetered     bool
	resumeAfterMetered bool
	meteredMutex       sync.Mutex
	meteredPaused      map[model.DownloadID]struct{}
	defaultPriority    model.Priority
	defaultDestination string
	profileMutex       sync.RWMutex
	profiles           map[string]Profile
	activeProfile      string
	profileStore       ProfileStore
	rateController     DownloadRateController
	trafficController  *qos.Controller
	trafficPolicy      qos.Policy
	trafficLinkRate    uint64
	trafficCgroup      qos.CgroupSelector
	trafficErrorMutex  sync.RWMutex
	trafficError       string
	telemetryObserver  TelemetryObserver
	latencyPolicy      *qos.LatencyPolicy
	partialCleaner     PartialCleaner
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
	if options.DefaultDestination != "" && !filepath.IsAbs(options.DefaultDestination) {
		return nil, fmt.Errorf("default download destination must be absolute")
	}
	if options.TrafficPolicy == "" {
		options.TrafficPolicy = qos.PolicyOff
	}
	if err := options.TrafficPolicy.Validate(); err != nil {
		return nil, err
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
		if profile.Policy == "" {
			profile.Policy = string(qos.PolicyOff)
		}
		if _, err := qos.ParsePolicy(profile.Policy); err != nil {
			return nil, fmt.Errorf("invalid profile %q: %w", name, err)
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
		options.TrafficPolicy = qos.Policy(profile.Policy)
		options.LatencyPolicy = profile.LatencyPolicy
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
	var trafficController *qos.Controller
	if options.TrafficBackend != nil {
		trafficController, err = qos.NewController(options.TrafficBackend)
		if err != nil {
			cancel()
			return nil, err
		}
	}
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
		networkReady:       make(chan struct{}),
		pauseOnMetered:     options.PauseOnMetered,
		resumeAfterMetered: options.ResumeAfterMetered,
		meteredPaused:      make(map[model.DownloadID]struct{}),
		defaultPriority:    options.DefaultPriority,
		defaultDestination: options.DefaultDestination,
		profiles:           profiles,
		activeProfile:      activeProfile,
		profileStore:       profileStore,
		rateController:     rateController,
		trafficController:  trafficController,
		trafficPolicy:      options.TrafficPolicy,
		trafficLinkRate:    options.TrafficLinkRate,
		trafficCgroup:      options.TrafficCgroup,
		telemetryObserver:  options.TelemetryObserver,
		latencyPolicy:      options.LatencyPolicy,
	}
	service.partialCleaner, _ = engine.(PartialCleaner)
	if service.networkObserver == nil {
		service.markNetworkReady()
	}
	service.waitGroup.Add(1)
	go service.runScheduler()
	if service.networkObserver != nil {
		service.waitGroup.Add(1)
		go service.runNetworkObserver()
	}
	if service.telemetryObserver != nil {
		service.waitGroup.Add(1)
		go service.runTelemetryObserver()
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
		return service.list(ctx, request.Payload)
	case ipc.OperationShow:
		return service.show(ctx, request.Payload)
	case ipc.OperationProfile:
		return service.setProfile(ctx, request.Payload)
	case ipc.OperationPolicy:
		return service.setTrafficPolicy(ctx, request.Payload)
	case ipc.OperationRemove:
		return service.remove(ctx, request.Payload)
	case ipc.OperationClear:
		return service.clearHistory(ctx)
	case ipc.OperationRetry:
		return service.retry(ctx, request.Payload)
	default:
		return nil, ipc.UnsupportedOperationError{Operation: request.Operation}
	}
}

func (service *Service) statusResponse() ipc.Status {
	status := service.status.Status()
	service.networkMutex.RLock()
	snapshot := service.networkSnapshot
	available := service.networkAvailable
	networkError := service.networkError
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
		Error:            networkError,
	}
	service.profileMutex.RLock()
	status.ActiveProfile = service.activeProfile
	policy := service.trafficPolicy
	latencyPolicy := service.latencyPolicy
	service.profileMutex.RUnlock()
	status.Traffic.Policy = string(policy)
	service.trafficErrorMutex.RLock()
	status.Traffic.Error = service.trafficError
	service.trafficErrorMutex.RUnlock()
	if service.trafficController != nil {
		current, applied := service.trafficController.Current()
		status.Traffic.Applied = applied
		if applied {
			status.Traffic.CurrentRateBitsPerSecond = current.ArgoRateBitsPerSecond
		}
	}
	if policy == qos.PolicyLatency && latencyPolicy != nil {
		diagnostics := latencyPolicy.Diagnostics(time.Now().UTC())
		status.Traffic.CurrentRateBitsPerSecond = diagnostics.State.RateBitsPerSecond
		status.Traffic.MeasuredLatency = diagnostics.MeasuredLatency
		status.Traffic.LatencyAvailable = diagnostics.LatencyAvailable
		status.Traffic.BaselineLatency = diagnostics.BaselineLatency
		status.Traffic.BaselineAvailable = diagnostics.BaselineAvailable
		status.Traffic.ControllerState = string(diagnostics.State.Reason)
	}

	return status
}

func (service *Service) Close() error {
	service.closeOnce.Do(func() {
		service.cancel()
		service.waitGroup.Wait()
		if service.trafficController != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			service.closeErr = service.trafficController.Reconcile(ctx, qos.DesiredState{Policy: qos.PolicyOff})
			cancel()
		}
	})

	return service.closeErr
}

func (service *Service) add(ctx context.Context, payload json.RawMessage) (ipc.AddResponse, error) {
	var request ipc.AddRequest
	if err := decodePayload(payload, &request); err != nil {
		return ipc.AddResponse{}, InvalidAddRequestError{Reason: err.Error()}
	}

	download, err := newDownload(request, service.currentDefaultPriority(), service.defaultDestination)
	if err != nil {
		return ipc.AddResponse{}, err
	}

	return service.queueDownload(ctx, download)
}

func (service *Service) queueDownload(ctx context.Context, download model.Download) (ipc.AddResponse, error) {
	if err := service.store.CreateDownload(ctx, download); err != nil {
		return ipc.AddResponse{}, fmt.Errorf("persist added download: %w", err)
	}
	blocked, err := service.pauseAdmissionOnMetered(ctx, download.ID)
	if err != nil {
		return ipc.AddResponse{}, err
	}
	if blocked {
		download.Status = model.StatusPaused
		return addResponse(download), nil
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
	_ = service.reconcileTrafficPolicy(context.WithoutCancel(ctx))

	return addResponse(download), nil
}

func addResponse(download model.Download) ipc.AddResponse {
	return ipc.AddResponse{
		ID:          download.ID.String(),
		Filename:    download.Filename,
		Destination: download.Destination,
		Status:      string(download.Status),
	}
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
	_ = service.reconcileTrafficPolicy(context.WithoutCancel(ctx))

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
	case model.StatusCanceled:
		validator, ok := service.engine.(CanceledResumeValidator)
		if !ok {
			return ipc.DownloadActionResponse{}, InvalidDownloadActionError{
				ID: download.ID.String(), Action: "resume", Reason: "download engine cannot validate canceled partial data",
			}
		}
		if err := validator.ValidateCanceledResume(ctx, download); err != nil {
			return ipc.DownloadActionResponse{}, InvalidDownloadActionError{
				ID: download.ID.String(), Action: "resume", Reason: err.Error(),
			}
		}
		status = model.StatusQueued
	case model.StatusQueued, model.StatusResolving, model.StatusDownloading:
		return actionResponse(identifier, download.Status), nil
	case model.StatusCompleted:
		return ipc.DownloadActionResponse{}, InvalidDownloadActionError{
			ID:     download.ID.String(),
			Action: "resume",
			Status: download.Status,
		}
	}
	if download.Status == model.StatusPaused {
		blocked, err := service.pauseAdmissionOnMetered(ctx, identifier)
		if err != nil {
			return ipc.DownloadActionResponse{}, err
		}
		if blocked {
			return actionResponse(identifier, model.StatusPaused), nil
		}
	}
	if err := service.store.UpdateDownloadStatus(ctx, identifier, status, time.Now().UTC(), ""); err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	blocked, err := service.pauseAdmissionOnMetered(ctx, identifier)
	if err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	if blocked {
		return actionResponse(identifier, model.StatusPaused), nil
	}
	if err := service.enqueue(ctx, identifier, download.Priority); err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	service.forgetMeteredPause(identifier)
	_ = service.reconcileTrafficPolicy(context.WithoutCancel(ctx))

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
	_ = service.reconcileTrafficPolicy(context.WithoutCancel(ctx))

	return actionResponse(identifier, model.StatusCanceled), nil
}

func (service *Service) remove(
	ctx context.Context,
	payload json.RawMessage,
) (ipc.DownloadActionResponse, error) {
	identifier, download, err := service.actionDownload(ctx, payload)
	if err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	switch download.Status {
	case model.StatusCompleted:
	case model.StatusFailed, model.StatusCanceled:
		if service.partialCleaner == nil {
			return ipc.DownloadActionResponse{}, fmt.Errorf("download engine cannot clean partial state")
		}
		if err := service.partialCleaner.RemovePartial(identifier); err != nil {
			return ipc.DownloadActionResponse{}, err
		}
	case model.StatusQueued, model.StatusResolving, model.StatusDownloading, model.StatusPaused:
		return ipc.DownloadActionResponse{}, InvalidDownloadActionError{
			ID: identifier.String(), Action: "remove", Status: download.Status,
		}
	}
	if err := service.store.DeleteDownload(ctx, identifier); err != nil {
		return ipc.DownloadActionResponse{}, err
	}

	return ipc.DownloadActionResponse{ID: identifier.String(), Status: "removed"}, nil
}

func (service *Service) clearHistory(ctx context.Context) (ipc.ClearResponse, error) {
	downloads, err := service.store.Downloads(ctx)
	if err != nil {
		return ipc.ClearResponse{}, err
	}
	for _, download := range downloads {
		if download.Status != model.StatusFailed && download.Status != model.StatusCanceled {
			continue
		}
		if service.partialCleaner == nil {
			return ipc.ClearResponse{}, fmt.Errorf("download engine cannot clean partial state")
		}
		if err := service.partialCleaner.RemovePartial(download.ID); err != nil {
			return ipc.ClearResponse{}, err
		}
	}
	removed, err := service.store.ClearDownloadHistory(ctx)
	if err != nil {
		return ipc.ClearResponse{}, err
	}

	return ipc.ClearResponse{Removed: len(removed)}, nil
}

func (service *Service) retry(ctx context.Context, payload json.RawMessage) (ipc.AddResponse, error) {
	_, original, err := service.actionDownload(ctx, payload)
	if err != nil {
		return ipc.AddResponse{}, err
	}
	switch original.Status {
	case model.StatusCompleted, model.StatusFailed, model.StatusCanceled:
	case model.StatusQueued, model.StatusResolving, model.StatusDownloading, model.StatusPaused:
		return ipc.AddResponse{}, InvalidDownloadActionError{
			ID: original.ID.String(), Action: "retry", Status: original.Status,
		}
	}
	download, err := newDownload(ipc.AddRequest{
		URL: original.URL, Destination: original.Destination,
	}, original.Priority, service.defaultDestination)
	if err != nil {
		return ipc.AddResponse{}, err
	}

	return service.queueDownload(ctx, download)
}

func (service *Service) list(ctx context.Context, payload json.RawMessage) (ipc.ListResponse, error) {
	request := ipc.ListRequest{}
	if len(payload) > 0 {
		if err := decodePayload(payload, &request); err != nil {
			return ipc.ListResponse{}, InvalidDownloadActionError{Action: "list", Reason: err.Error()}
		}
	}
	if request.Limit == 0 {
		request.Limit = defaultListPageSize
	}
	if request.Limit < 1 || request.Limit > maximumListPageSize {
		return ipc.ListResponse{}, InvalidDownloadActionError{Action: "list", Reason: "limit must be between 1 and 200"}
	}
	cursor, err := decodeDownloadCursor(request.Cursor)
	if err != nil {
		return ipc.ListResponse{}, InvalidDownloadActionError{Action: "list", Reason: err.Error()}
	}
	downloads, next, more, err := service.store.DownloadPage(ctx, cursor, request.Limit)
	if err != nil {
		return ipc.ListResponse{}, err
	}
	response := ipc.ListResponse{Downloads: make([]ipc.Download, 0, len(downloads))}
	for _, download := range downloads {
		response.Downloads = append(response.Downloads, downloadSummaryResponse(download))
	}
	if more {
		response.NextCursor, err = encodeDownloadCursor(next)
		if err != nil {
			return ipc.ListResponse{}, err
		}
	}

	return response, nil
}

func encodeDownloadCursor(cursor model.DownloadCursor) (string, error) {
	encoded, err := json.Marshal(struct {
		CreatedAt time.Time `json:"created_at"`
		ID        string    `json:"id"`
	}{CreatedAt: cursor.CreatedAt, ID: cursor.ID.String()})
	if err != nil {
		return "", fmt.Errorf("encode download cursor: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeDownloadCursor(value string) (model.DownloadCursor, error) {
	if value == "" {
		return model.DownloadCursor{}, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return model.DownloadCursor{}, fmt.Errorf("cursor is invalid")
	}
	var cursor struct {
		CreatedAt time.Time `json:"created_at"`
		ID        string    `json:"id"`
	}
	if err := decodePayload(encoded, &cursor); err != nil || cursor.CreatedAt.IsZero() {
		return model.DownloadCursor{}, fmt.Errorf("cursor is invalid")
	}
	identifier, err := model.ParseDownloadID(cursor.ID)
	if err != nil {
		return model.DownloadCursor{}, fmt.Errorf("cursor is invalid")
	}

	return model.DownloadCursor{CreatedAt: cursor.CreatedAt, ID: identifier}, nil
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
	networkReady := false
	networkReadyChannel := (<-chan struct{})(service.networkReady)
	for {
		for networkReady && queue.Active() < maximumActive {
			identifier, available := queue.Next()
			if !available {
				break
			}
			service.start(identifier)
		}
		select {
		case <-service.ctx.Done():
			return
		case <-networkReadyChannel:
			networkReady = true
			networkReadyChannel = nil
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
	service.trafficPolicy = qos.Policy(profile.Policy)
	service.latencyPolicy = profile.LatencyPolicy
	service.profileMutex.Unlock()
	if err := service.updateSchedulerLimit(ctx, profile.MaximumConcurrentDownloads); err != nil {
		return ipc.ProfileResponse{}, err
	}
	if err := service.reconcileMeteredAdmission(ctx); err != nil {
		return ipc.ProfileResponse{}, err
	}
	qosError := service.reconcileTrafficPolicy(ctx)
	response := profileResponse(profile)
	if service.trafficController != nil {
		_, response.PolicyApplied = service.trafficController.Current()
	}
	if qosError != nil {
		response.QoSError = qosError.Error()
	}

	return response, nil
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
		_ = service.reconcileTrafficPolicy(service.ctx)
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
	blocked, err := service.pauseAdmissionOnMetered(service.ctx, identifier)
	if err != nil || blocked {
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
		URL:             diagnostic.Display(diagnostic.URL(download.URL)),
		Destination:     diagnostic.Display(download.Destination),
		Filename:        diagnostic.Display(download.Filename),
		TotalSize:       download.TotalSize,
		DownloadedBytes: download.DownloadedBytes,
		Status:          string(download.Status),
		Priority:        string(download.Priority),
		CreatedAt:       download.CreatedAt,
		UpdatedAt:       download.UpdatedAt,
		Error:           diagnostic.Display(diagnostic.Text(download.Error)),
	}
}

func downloadSummaryResponse(download model.Download) ipc.Download {
	response := downloadResponse(download)
	response.URL = ""
	response.Destination = ""
	response.Error = ""
	filename := []rune(response.Filename)
	if len(filename) > maximumSummaryFilename {
		response.Filename = string(filename[:maximumSummaryFilename-1]) + "…"
	}

	return response
}

func newDownload(request ipc.AddRequest, priority model.Priority, defaultDestination string) (model.Download, error) {
	parsedURL, err := url.Parse(request.URL)
	if err != nil {
		return model.Download{}, InvalidAddRequestError{Reason: "URL cannot be parsed"}
	}
	if (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return model.Download{}, InvalidAddRequestError{Reason: "URL must use HTTP or HTTPS and include a host"}
	}
	if request.Destination == "" {
		request.Destination = defaultDestination
	}
	if request.Destination == "" {
		return model.Download{}, InvalidAddRequestError{Reason: "destination is required and no default is configured"}
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
	filename = diagnostic.Filename(filename)
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
