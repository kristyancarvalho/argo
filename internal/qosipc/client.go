package qosipc

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

	"github.com/kristyancarvalho/argo/internal/qos"
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

func (client *Client) Apply(ctx context.Context, state qos.DesiredState) error {
	var status Status
	return client.call(ctx, OperationApply, ApplyRequest{State: state}, &status)
}

func (client *Client) Remove(ctx context.Context, interfaceName string) error {
	var status Status
	return client.call(ctx, OperationRemove, RemoveRequest{Interface: interfaceName}, &status)
}

func (client *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	if err := client.call(ctx, OperationStatus, nil, &status); err != nil {
		return Status{}, err
	}

	return status, nil
}

func (client *Client) call(ctx context.Context, operation Operation, payload any, result any) error {
	if err := unixsocket.Validate(filepath.Dir(client.SocketPath)); err != nil {
		return fmt.Errorf("validate QoS helper socket directory: %w", err)
	}
	identifier, err := requestID()
	if err != nil {
		return err
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode QoS helper request: %w", err)
	}
	request := Request{
		Version:   ProtocolVersion,
		ID:        identifier,
		Operation: operation,
		Payload:   encodedPayload,
	}
	dialer := net.Dialer{Timeout: client.Timeout}
	connection, err := dialer.DialContext(ctx, "unix", client.SocketPath)
	if err != nil {
		return fmt.Errorf("connect to QoS helper: %w", err)
	}
	defer func() {
		_ = connection.Close()
	}()
	if err := unixsocket.ValidateSocket(client.SocketPath); err != nil {
		return fmt.Errorf("validate QoS helper socket: %w", err)
	}
	if err := unixsocket.ValidatePeer(connection, client.ExpectedUID); err != nil {
		return fmt.Errorf("authenticate QoS helper: %w", err)
	}
	stopClose := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	defer stopClose()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set QoS helper deadline: %w", err)
		}
	} else if client.Timeout > 0 {
		if err := connection.SetDeadline(time.Now().Add(client.Timeout)); err != nil {
			return fmt.Errorf("set QoS helper deadline: %w", err)
		}
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("send QoS helper request: %w", err)
	}
	message, err := bufio.NewReader(io.LimitReader(connection, maximumMessageSize+1)).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read QoS helper response: %w", err)
	}
	if len(message) > maximumMessageSize {
		return fmt.Errorf("QoS helper response exceeds maximum message size")
	}
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("decode QoS helper response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("QoS helper response contains trailing data")
	}
	if response.Version != ProtocolVersion {
		return fmt.Errorf("QoS helper uses unsupported protocol version %d", response.Version)
	}
	if response.ID == "" && response.Error != nil && response.Error.Code == "server_busy" {
		return RemoteError{Code: response.Error.Code, Message: response.Error.Message}
	}
	if response.ID != identifier {
		return fmt.Errorf("QoS helper response identifier %q does not match request %q", response.ID, identifier)
	}
	if !response.OK {
		if response.Error == nil {
			return fmt.Errorf("QoS helper returned an unspecified error")
		}
		return RemoteError{Code: response.Error.Code, Message: response.Error.Message}
	}
	if response.Error != nil {
		return fmt.Errorf("successful QoS helper response contains an error")
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("decode QoS helper result: %w", err)
	}

	return nil
}

func requestID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate QoS helper request identifier: %w", err)
	}

	return hex.EncodeToString(value), nil
}
