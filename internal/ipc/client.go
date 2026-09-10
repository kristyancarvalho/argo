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
	"path/filepath"
	"time"

	"github.com/kristyancarvalho/argo/internal/unixsocket"
)

type Client struct {
	SocketPath  string
	Timeout     time.Duration
	ExpectedUID uint32
}

func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath, Timeout: connectionTimeout, ExpectedUID: uint32(os.Geteuid())}
}

func (client *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	if err := client.Call(ctx, OperationStatus, nil, &status); err != nil {
		return Status{}, err
	}

	return status, nil
}

func (client *Client) Add(ctx context.Context, rawURL, destination string) (AddResponse, error) {
	return client.AddWithChecksum(ctx, rawURL, destination, "")
}

func (client *Client) AddWithChecksum(ctx context.Context, rawURL, destination, checksum string) (AddResponse, error) {
	var response AddResponse
	if err := client.Call(ctx, OperationAdd, AddRequest{
		URL:         rawURL,
		Destination: destination,
		Checksum:    checksum,
	}, &response); err != nil {
		return AddResponse{}, err
	}

	return response, nil
}

func (client *Client) Verify(ctx context.Context, id string) (VerifyResponse, error) {
	var response VerifyResponse
	if err := client.Call(ctx, OperationVerify, DownloadActionRequest{ID: id}, &response); err != nil {
		return VerifyResponse{}, err
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

func (client *Client) Remove(ctx context.Context, id string) (DownloadActionResponse, error) {
	return client.downloadAction(ctx, OperationRemove, id)
}

func (client *Client) Clear(ctx context.Context) (ClearResponse, error) {
	var response ClearResponse
	if err := client.Call(ctx, OperationClear, nil, &response); err != nil {
		return ClearResponse{}, err
	}

	return response, nil
}

func (client *Client) Retry(ctx context.Context, id string) (AddResponse, error) {
	var response AddResponse
	if err := client.Call(ctx, OperationRetry, DownloadActionRequest{ID: id}, &response); err != nil {
		return AddResponse{}, err
	}

	return response, nil
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
	downloads := make([]Download, 0)
	cursor := ""
	seen := make(map[string]struct{})
	for {
		var response ListResponse
		if err := client.Call(ctx, OperationList, ListRequest{Cursor: cursor}, &response); err != nil {
			return nil, err
		}
		downloads = append(downloads, response.Downloads...)
		if response.NextCursor == "" {
			return downloads, nil
		}
		if response.NextCursor == cursor {
			return nil, fmt.Errorf("daemon returned a repeated download cursor")
		}
		if _, exists := seen[response.NextCursor]; exists {
			return nil, fmt.Errorf("daemon returned a cyclic download cursor")
		}
		seen[response.NextCursor] = struct{}{}
		cursor = response.NextCursor
	}
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

func (client *Client) Policy(ctx context.Context, policy string) (PolicyResponse, error) {
	var response PolicyResponse
	if err := client.Call(ctx, OperationPolicy, PolicyRequest{Policy: policy}, &response); err != nil {
		return PolicyResponse{}, err
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
	if err := unixsocket.Validate(filepath.Dir(client.SocketPath)); err != nil {
		return fmt.Errorf("validate daemon socket directory: %w", err)
	}
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
	if err := unixsocket.ValidateSocket(client.SocketPath); err != nil {
		return fmt.Errorf("validate daemon socket: %w", err)
	}
	if err := unixsocket.ValidatePeer(connection, client.ExpectedUID); err != nil {
		return fmt.Errorf("authenticate daemon: %w", err)
	}
	stopClose := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	defer stopClose()
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
	if response.ID == "" && response.Error != nil && response.Error.Code == "server_busy" {
		return RemoteError{Code: response.Error.Code, Message: response.Error.Message}
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
