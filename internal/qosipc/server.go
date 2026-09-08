package qosipc

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

	"github.com/kristyancarvalho/argo/internal/unixsocket"
)

const (
	maximumMessageSize = 64 * 1024
	maximumConnections = 32
	acceptInterval     = 250 * time.Millisecond
	connectionTimeout  = 5 * time.Second
	DefaultSocketPath  = "/run/argo/argo-qosd.sock"
	DefaultStatePath   = "/run/argo/argo-qosd.state"
)

type Handler interface {
	Handle(context.Context, Request) (any, error)
}

type Server struct {
	listener    *net.UnixListener
	handler     Handler
	authorizer  Authorizer
	socketPath  string
	closeOnce   sync.Once
	closeError  error
	workers     sync.WaitGroup
	connections chan struct{}
	activeMutex sync.Mutex
	active      map[*net.UnixConn]struct{}
	closing     bool
}

func Listen(socketPath string, handler Handler, authorizer Authorizer) (*Server, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("QoS helper socket path is empty")
	}
	if handler == nil {
		return nil, fmt.Errorf("QoS helper handler is required")
	}
	if authorizer == nil {
		return nil, fmt.Errorf("QoS helper authorizer is required")
	}
	if err := unixsocket.Prepare(filepath.Dir(socketPath)); err != nil {
		return nil, fmt.Errorf("secure QoS helper socket directory: %w", err)
	}
	if err := removeStaleSocket(socketPath); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on QoS helper socket %s: %w", socketPath, err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("secure QoS helper socket: %w", err), listener.Close())
	}

	return &Server{
		listener:    listener,
		handler:     handler,
		authorizer:  authorizer,
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
		if ctx.Err() != nil {
			return nil
		}
		if err := server.listener.SetDeadline(time.Now().Add(acceptInterval)); err != nil {
			return fmt.Errorf("set QoS helper accept deadline: %w", err)
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
			return fmt.Errorf("accept QoS helper connection: %w", err)
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
		return fmt.Errorf("close QoS helper server: %w", server.closeError)
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
	if err := server.authorizer.Authorize(connection); err != nil {
		server.writeError(connection, "", "unauthorized", "peer is not authorized")
		return
	}
	message, err := bufio.NewReader(io.LimitReader(connection, maximumMessageSize+1)).ReadBytes('\n')
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
		var requestError RequestError
		if errors.As(err, &requestError) {
			server.writeError(connection, request.ID, requestError.ErrorCode, requestError.Message)
			return
		}
		server.writeError(connection, request.ID, "internal_error", err.Error())
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		server.writeError(connection, request.ID, "internal_error", "encode response")
		return
	}
	server.writeResponse(connection, Response{
		Version: ProtocolVersion,
		ID:      request.ID,
		OK:      true,
		Result:  encoded,
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
		Error:   &ResponseError{Code: code, Message: message},
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
		return fmt.Errorf("inspect QoS helper socket %s: %w", socketPath, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refuse to replace non-socket path %s", socketPath)
	}
	if err := unixsocket.ValidateSocket(socketPath); err != nil {
		return fmt.Errorf("refuse to replace unsafe QoS helper socket %s: %w", socketPath, err)
	}
	connection, dialError := net.DialTimeout("unix", socketPath, acceptInterval)
	if dialError == nil {
		_ = connection.Close()
		return fmt.Errorf("QoS helper socket %s is already active", socketPath)
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale QoS helper socket %s: %w", socketPath, err)
	}

	return nil
}
