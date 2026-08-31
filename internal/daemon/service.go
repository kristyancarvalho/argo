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
)

const queueCapacity = 64

type Store interface {
	CreateDownload(context.Context, model.Download) error
	Download(context.Context, model.DownloadID) (model.Download, error)
	Downloads(context.Context) ([]model.Download, error)
	RecoverActiveDownloads(context.Context, time.Time) error
	UpdateDownloadStatus(context.Context, model.DownloadID, model.Status, time.Time, string) error
}

type DownloadEngine interface {
	Download(context.Context, model.Download) error
}

type Service struct {
	store        Store
	engine       DownloadEngine
	status       *ipc.StatusHandler
	jobs         chan model.DownloadID
	initial      []model.DownloadID
	ctx          context.Context
	cancel       context.CancelFunc
	waitGroup    sync.WaitGroup
	closeOnce    sync.Once
	activeMutex  sync.Mutex
	activeID     model.DownloadID
	activeCancel context.CancelFunc
}

func NewService(parent context.Context, store Store, engine DownloadEngine) (*Service, error) {
	if err := store.RecoverActiveDownloads(parent, time.Now().UTC()); err != nil {
		return nil, err
	}
	downloads, err := store.Downloads(parent)
	if err != nil {
		return nil, err
	}
	initial := make([]model.DownloadID, 0)
	for _, download := range downloads {
		if download.Status == model.StatusQueued {
			initial = append(initial, download.ID)
		}
	}

	ctx, cancel := context.WithCancel(parent)
	service := &Service{
		store:   store,
		engine:  engine,
		status:  ipc.NewStatusHandler(),
		jobs:    make(chan model.DownloadID, queueCapacity),
		initial: initial,
		ctx:     ctx,
		cancel:  cancel,
	}
	service.waitGroup.Add(1)
	go service.runWorker()

	return service, nil
}

func (service *Service) Handle(ctx context.Context, request ipc.Request) (any, error) {
	switch request.Operation {
	case ipc.OperationStatus:
		return service.status.Handle(ctx, request)
	case ipc.OperationAdd:
		return service.add(ctx, request.Payload)
	case ipc.OperationPause:
		return service.pause(ctx, request.Payload)
	case ipc.OperationResume:
		return service.resume(ctx, request.Payload)
	default:
		return nil, ipc.UnsupportedOperationError{Operation: request.Operation}
	}
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

	download, err := newDownload(request)
	if err != nil {
		return ipc.AddResponse{}, err
	}
	if err := service.store.CreateDownload(ctx, download); err != nil {
		return ipc.AddResponse{}, fmt.Errorf("persist added download: %w", err)
	}
	if err := service.enqueue(ctx, download.ID); err != nil {
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

	service.activeMutex.Lock()
	if service.activeID == identifier && service.activeCancel != nil {
		service.activeCancel()
	}
	service.activeMutex.Unlock()

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
	if err := service.enqueue(ctx, identifier); err != nil {
		return ipc.DownloadActionResponse{}, err
	}

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

func (service *Service) enqueue(ctx context.Context, identifier model.DownloadID) error {
	select {
	case service.jobs <- identifier:
		return nil
	case <-service.ctx.Done():
		return fmt.Errorf("daemon is shutting down")
	case <-ctx.Done():
		return ctx.Err()
	default:
		return QueueFullError{Capacity: queueCapacity}
	}
}

func (service *Service) runWorker() {
	defer service.waitGroup.Done()
	for _, identifier := range service.initial {
		if service.ctx.Err() != nil {
			return
		}
		service.process(identifier)
	}
	for {
		select {
		case <-service.ctx.Done():
			return
		case identifier := <-service.jobs:
			service.process(identifier)
		}
	}
}

func (service *Service) process(identifier model.DownloadID) {
	download, err := service.store.Download(service.ctx, identifier)
	if err != nil {
		return
	}
	if download.Status != model.StatusQueued && download.Status != model.StatusDownloading {
		return
	}

	downloadContext, cancel := context.WithCancel(service.ctx)
	service.activeMutex.Lock()
	service.activeID = identifier
	service.activeCancel = cancel
	service.activeMutex.Unlock()

	_ = service.engine.Download(downloadContext, download)
	cancel()

	service.activeMutex.Lock()
	if service.activeID == identifier {
		service.activeID = ""
		service.activeCancel = nil
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

func newDownload(request ipc.AddRequest) (model.Download, error) {
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
		Priority:        model.PriorityNormal,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}
