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
	UpdateDownloadStatus(context.Context, model.DownloadID, model.Status, time.Time, string) error
}

type DownloadEngine interface {
	Download(context.Context, model.Download) error
}

type Service struct {
	store       Store
	engine      DownloadEngine
	status      *ipc.StatusHandler
	jobs        chan model.DownloadID
	ctx         context.Context
	cancel      context.CancelFunc
	waitGroup   sync.WaitGroup
	closeOnce   sync.Once
	closeResult error
}

func NewService(parent context.Context, store Store, engine DownloadEngine) *Service {
	ctx, cancel := context.WithCancel(parent)
	service := &Service{
		store:  store,
		engine: engine,
		status: ipc.NewStatusHandler(),
		jobs:   make(chan model.DownloadID, queueCapacity),
		ctx:    ctx,
		cancel: cancel,
	}
	service.waitGroup.Add(1)
	go service.runWorker()

	return service
}

func (service *Service) Handle(ctx context.Context, request ipc.Request) (any, error) {
	switch request.Operation {
	case ipc.OperationStatus:
		return service.status.Handle(ctx, request)
	case ipc.OperationAdd:
		return service.add(ctx, request.Payload)
	default:
		return nil, ipc.UnsupportedOperationError{Operation: request.Operation}
	}
}

func (service *Service) Close() error {
	service.closeOnce.Do(func() {
		service.cancel()
		service.waitGroup.Wait()
	})

	return service.closeResult
}

func (service *Service) add(ctx context.Context, payload json.RawMessage) (ipc.AddResponse, error) {
	var request ipc.AddRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return ipc.AddResponse{}, InvalidAddRequestError{Reason: "invalid payload"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ipc.AddResponse{}, InvalidAddRequestError{Reason: "payload contains trailing data"}
	}

	download, err := newDownload(request)
	if err != nil {
		return ipc.AddResponse{}, err
	}
	if err := service.store.CreateDownload(ctx, download); err != nil {
		return ipc.AddResponse{}, fmt.Errorf("persist added download: %w", err)
	}

	select {
	case service.jobs <- download.ID:
	case <-service.ctx.Done():
		return ipc.AddResponse{}, fmt.Errorf("daemon is shutting down")
	default:
		now := time.Now().UTC()
		err := service.store.UpdateDownloadStatus(
			context.WithoutCancel(ctx),
			download.ID,
			model.StatusCanceled,
			now,
			"download queue is full",
		)
		return ipc.AddResponse{}, errors.Join(QueueFullError{Capacity: queueCapacity}, err)
	}

	return ipc.AddResponse{
		ID:          download.ID.String(),
		Filename:    download.Filename,
		Destination: download.Destination,
		Status:      string(download.Status),
	}, nil
}

func (service *Service) runWorker() {
	defer service.waitGroup.Done()
	for {
		select {
		case <-service.ctx.Done():
			return
		case identifier := <-service.jobs:
			download, err := service.store.Download(service.ctx, identifier)
			if err != nil {
				continue
			}
			_ = service.engine.Download(service.ctx, download)
		}
	}
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
