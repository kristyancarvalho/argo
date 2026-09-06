package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maximumMessageSize = 1 << 20
	maximumConnections = 64
	acceptInterval     = 250 * time.Millisecond
	connectionTimeout  = 5 * time.Second
)

type Server struct {
	listener    *net.UnixListener
	handler     Handler
	socketPath  string
	closeOnce   sync.Once
	closeError  error
	workers     sync.WaitGroup
	connections chan struct{}
	activeMutex sync.Mutex
	active      map[*net.UnixConn]struct{}
	closing     bool
}

func Listen(socketPath string, handler Handler) (*Server, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("socket path is empty")
	}
	if handler == nil {
		return nil, fmt.Errorf("IPC handler is nil")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	if err := removeStaleSocket(socketPath); err != nil {
		return nil, err
	}

	address := &net.UnixAddr{Name: socketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return nil, fmt.Errorf("listen on Unix socket %s: %w", socketPath, err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		closeErr := listener.Close()
		return nil, errors.Join(fmt.Errorf("secure Unix socket: %w", err), closeErr)
	}

	return &Server{
		listener:    listener,
		handler:     handler,
		socketPath:  socketPath,
		connections: make(chan struct{}, maximumConnections),
		active:      make(map[*net.UnixConn]struct{}),
	}, nil
}

func (server *Server) Serve(ctx context.Context) (serveError error) {
	defer func() {
		serveError = errors.Join(serveError, server.Close())
		server.workers.Wait()
	}()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := server.listener.SetDeadline(time.Now().Add(acceptInterval)); err != nil {
			return fmt.Errorf("set IPC accept deadline: %w", err)
		}
		connection, err := server.listener.AcceptUnix()
		if err != nil {
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) && ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept IPC connection: %w", err)
		}
		select {
		case server.connections <- struct{}{}:
			server.activeMutex.Lock()
			if server.closing {
				server.activeMutex.Unlock()
				<-server.connections
				_ = connection.Close()
				continue
			}
			server.active[connection] = struct{}{}
			server.activeMutex.Unlock()
			server.workers.Add(1)
			go func() {
				defer server.workers.Done()
				defer func() { <-server.connections }()
				defer server.forgetConnection(connection)
				server.handleConnection(ctx, connection)
			}()
		default:
			_ = connection.SetDeadline(time.Now().Add(connectionTimeout))
			server.writeError(connection, "", "server_busy", "too many concurrent requests")
			_ = connection.Close()
		}
	}
}

func (server *Server) Close() error {
	server.closeOnce.Do(func() {
		server.closeError = server.listener.Close()
		server.activeMutex.Lock()
		server.closing = true
		connections := make([]*net.UnixConn, 0, len(server.active))
		for connection := range server.active {
			connections = append(connections, connection)
		}
		server.activeMutex.Unlock()
		for _, connection := range connections {
			server.closeError = errors.Join(server.closeError, connection.Close())
		}
	})
	if server.closeError != nil && !errors.Is(server.closeError, net.ErrClosed) {
		return fmt.Errorf("close IPC server: %w", server.closeError)
	}

	return nil
}

func (server *Server) SocketPath() string {
	return server.socketPath
}

func (server *Server) handleConnection(ctx context.Context, connection *net.UnixConn) {
	defer func() {
		_ = connection.Close()
	}()
	if err := connection.SetDeadline(time.Now().Add(connectionTimeout)); err != nil {
		return
	}

	reader := bufio.NewReader(io.LimitReader(connection, maximumMessageSize+1))
	message, err := reader.ReadBytes('\n')
	if len(message) > maximumMessageSize {
		server.writeError(connection, "", "request_too_large", "request exceeds maximum message size")
		return
	}
	if err != nil {
		server.writeError(connection, "", "malformed_request", "request must be newline terminated")
		return
	}

	request, responseError := decodeRequest(message)
	if responseError != nil {
		server.writeResponse(connection, Response{
			Version: ProtocolVersion,
			ID:      request.ID,
			OK:      false,
			Error:   responseError,
		})
		return
	}

	requestContext, cancel := context.WithTimeout(ctx, connectionTimeout)
	stopClose := context.AfterFunc(requestContext, func() {
		_ = connection.Close()
	})
	defer func() {
		stopClose()
		cancel()
	}()
	peerClosed := make(chan struct{})
	go func() {
		var buffer [1]byte
		_, _ = connection.Read(buffer[:])
		close(peerClosed)
	}()
	go func() {
		select {
		case <-peerClosed:
			cancel()
		case <-requestContext.Done():
		}
	}()
	result, err := server.handler.Handle(requestContext, request)
	if err != nil {
		var unsupported UnsupportedOperationError
		if errors.As(err, &unsupported) {
			server.writeError(connection, request.ID, "unsupported_operation", err.Error())
			return
		}
		var codedError CodedError
		if errors.As(err, &codedError) {
			server.writeError(connection, request.ID, codedError.Code(), err.Error())
			return
		}
		server.writeError(connection, request.ID, "internal_error", err.Error())
		return
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		server.writeError(connection, request.ID, "internal_error", "encode response result")
		return
	}
	server.writeResponse(connection, Response{
		Version: ProtocolVersion,
		ID:      request.ID,
		OK:      true,
		Result:  encodedResult,
	})
}

func (server *Server) forgetConnection(connection *net.UnixConn) {
	server.activeMutex.Lock()
	delete(server.active, connection)
	server.activeMutex.Unlock()
}

func decodeRequest(message []byte) (Request, *ResponseError) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, &ResponseError{Code: "malformed_request", Message: "invalid request envelope"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request, &ResponseError{Code: "malformed_request", Message: "request contains trailing data"}
	}
	if request.Version != ProtocolVersion {
		return request, &ResponseError{Code: "unsupported_version", Message: "unsupported protocol version"}
	}
	if request.ID == "" {
		return request, &ResponseError{Code: "malformed_request", Message: "request identifier is required"}
	}
	if request.Operation == "" {
		return request, &ResponseError{Code: "malformed_request", Message: "operation is required"}
	}

	return request, nil
}

func (server *Server) writeError(connection net.Conn, id, code, message string) {
	server.writeResponse(connection, Response{
		Version: ProtocolVersion,
		ID:      id,
		OK:      false,
		Error: &ResponseError{
			Code:    code,
			Message: message,
		},
	})
}

func (server *Server) writeResponse(connection net.Conn, response Response) {
	_ = json.NewEncoder(connection).Encode(response)
}

func removeStaleSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Unix socket %s: %w", socketPath, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refuse to replace non-socket path %s", socketPath)
	}

	connection, dialErr := net.DialTimeout("unix", socketPath, acceptInterval)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("unix socket %s is already active", socketPath)
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale Unix socket %s: %w", socketPath, err)
	}

	return nil
}
