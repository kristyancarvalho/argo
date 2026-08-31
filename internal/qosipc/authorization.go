package qosipc

import (
	"fmt"
	"net"
	"syscall"
)

type Authorizer interface {
	Authorize(*net.UnixConn) error
}

type PeerUIDAuthorizer struct {
	allowed map[uint32]struct{}
}

func NewPeerUIDAuthorizer(allowed ...uint32) (*PeerUIDAuthorizer, error) {
	if len(allowed) == 0 {
		return nil, fmt.Errorf("at least one authorized UID is required")
	}
	identifiers := make(map[uint32]struct{}, len(allowed))
	for _, identifier := range allowed {
		identifiers[identifier] = struct{}{}
	}

	return &PeerUIDAuthorizer{allowed: identifiers}, nil
}

func (authorizer *PeerUIDAuthorizer) Authorize(connection *net.UnixConn) error {
	raw, err := connection.SyscallConn()
	if err != nil {
		return fmt.Errorf("access peer socket: %w", err)
	}
	var credentials *syscall.Ucred
	var credentialError error
	if err := raw.Control(func(descriptor uintptr) {
		credentials, credentialError = syscall.GetsockoptUcred(
			int(descriptor),
			syscall.SOL_SOCKET,
			syscall.SO_PEERCRED,
		)
	}); err != nil {
		return fmt.Errorf("inspect peer socket: %w", err)
	}
	if credentialError != nil {
		return fmt.Errorf("read peer credentials: %w", credentialError)
	}
	if credentials == nil {
		return fmt.Errorf("peer credentials are unavailable")
	}
	if _, allowed := authorizer.allowed[credentials.Uid]; !allowed {
		return fmt.Errorf("peer UID %d is not authorized", credentials.Uid)
	}

	return nil
}
