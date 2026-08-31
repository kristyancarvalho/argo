package ipc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

type Client struct {
	SocketPath string
	Timeout    time.Duration
}

func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath, Timeout: connectionTimeout}
}

func (client *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	if err := client.Call(ctx, OperationStatus, nil, &status); err != nil {
		return Status{}, err
	}

	return status, nil
}

func (client *Client) Add(ctx context.Context, rawURL, destination string) (AddResponse, error) {
	if destination == "" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return AddResponse{}, fmt.Errorf("determine download destination: %w", err)
		}
		destination = workingDirectory
	}

	var response AddResponse
	if err := client.Call(ctx, OperationAdd, AddRequest{
		URL:         rawURL,
		Destination: destination,
	}, &response); err != nil {
		return AddResponse{}, err
	}

	return response, nil
}

func (client *Client) Pause(ctx context.Context, id string) (DownloadActionResponse, error) {
	return client.downloadAction(ctx, OperationPause, id)
}

func (client *Client) Resume(ctx context.Context, id string) (DownloadActionResponse, error) {
	return client.downloadAction(ctx, OperationResume, id)
}

func (client *Client) Cancel(ctx context.Context, id string) (DownloadActionResponse, error) {
	return client.downloadAction(ctx, OperationCancel, id)
}

func (client *Client) Priority(ctx context.Context, id, priority string) (PriorityResponse, error) {
	var response PriorityResponse
	if err := client.Call(ctx, OperationPriority, PriorityRequest{
		ID:       id,
		Priority: priority,
	}, &response); err != nil {
		return PriorityResponse{}, err
	}

	return response, nil
}

func (client *Client) List(ctx context.Context) ([]Download, error) {
	var response ListResponse
	if err := client.Call(ctx, OperationList, nil, &response); err != nil {
		return nil, err
	}

	return response.Downloads, nil
}

func (client *Client) Show(ctx context.Context, id string) (Download, error) {
	var response Download
	if err := client.Call(ctx, OperationShow, ShowRequest{ID: id}, &response); err != nil {
		return Download{}, err
	}

	return response, nil
}

func (client *Client) Profile(ctx context.Context, name string) (ProfileResponse, error) {
	var response ProfileResponse
	if err := client.Call(ctx, OperationProfile, ProfileRequest{Name: name}, &response); err != nil {
		return ProfileResponse{}, err
	}

	return response, nil
}

func (client *Client) downloadAction(
	ctx context.Context,
	operation Operation,
	id string,
) (DownloadActionResponse, error) {
	var response DownloadActionResponse
	if err := client.Call(ctx, operation, DownloadActionRequest{ID: id}, &response); err != nil {
		return DownloadActionResponse{}, err
	}

	return response, nil
}

func (client *Client) Call(ctx context.Context, operation Operation, payload any, result any) error {
	requestID, err := newRequestID()
	if err != nil {
		return err
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode IPC request payload: %w", err)
	}
	request := Request{
		Version:   ProtocolVersion,
		ID:        requestID,
		Operation: operation,
		Payload:   encodedPayload,
	}

	dialer := net.Dialer{Timeout: client.Timeout}
	connection, err := dialer.DialContext(ctx, "unix", client.SocketPath)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer func() {
		_ = connection.Close()
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set IPC deadline: %w", err)
		}
	} else if client.Timeout > 0 {
		if err := connection.SetDeadline(time.Now().Add(client.Timeout)); err != nil {
			return fmt.Errorf("set IPC deadline: %w", err)
		}
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("send IPC request: %w", err)
	}

	reader := bufio.NewReader(io.LimitReader(connection, maximumMessageSize+1))
	message, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read IPC response: %w", err)
	}
	if len(message) > maximumMessageSize {
		return fmt.Errorf("IPC response exceeds maximum message size")
	}
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("decode IPC response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("IPC response contains trailing data")
	}
	if response.Version != ProtocolVersion {
		return fmt.Errorf("daemon uses unsupported protocol version %d", response.Version)
	}
	if response.ID != requestID {
		return fmt.Errorf("IPC response identifier %q does not match request %q", response.ID, requestID)
	}
	if !response.OK {
		if response.Error == nil {
			return fmt.Errorf("daemon returned an unspecified error")
		}
		return RemoteError{Code: response.Error.Code, Message: response.Error.Message}
	}
	if response.Error != nil {
		return fmt.Errorf("successful IPC response contains an error")
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("decode IPC response result: %w", err)
	}

	return nil
}

func newRequestID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate IPC request identifier: %w", err)
	}

	return hex.EncodeToString(value), nil
}
