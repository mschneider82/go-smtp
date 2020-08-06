package smtp

import (
	"context"
	"errors"
	"io"
)

var (
	ErrAuthRequired    = errors.New("Please authenticate first")
	ErrAuthUnsupported = errors.New("Authentication not supported")
)

// The DefaultBackend
type DefaultBackend struct {
	s SessionFactory
}

// SessionFactory Creates a New session for each Connection
type SessionFactory interface {
	New() Session
}

// NewDefaultBackend creates a backend without Authentication
func NewDefaultBackend(s SessionFactory) Backend {
	return &DefaultBackend{s: s}
}

// Login returns a session
func (be *DefaultBackend) Login(state *ConnectionState, username, password string) (Session, error) {
	return be.s.New(), nil
}

// AnonymousLogin is not implemented in default backend
func (be *DefaultBackend) AnonymousLogin(state *ConnectionState) (Session, error) {
	return be.s.New(), nil
}

// A SMTP server backend.
type Backend interface {
	// Authenticate a user. Return smtp.ErrAuthUnsupported if you don't want to
	// support this.
	Login(state *ConnectionState, username, password string) (Session, error)

	// Called if the client attempts to send mail without logging in first.
	// Return smtp.ErrAuthRequired if you don't want to support this.
	AnonymousLogin(state *ConnectionState) (Session, error)
}

type Session interface {
	// Discard currently processed message.
	Reset()

	// Free all resources associated with session.
	Logout() error

	// Set return path for currently processed message.
	Mail(from string) error
	// Add recipient for currently processed message.
	Rcpt(to string) error
	// Set currently processed message contents and send it.
	Data(r io.Reader, d DataContext) error
}

type DataContext interface {
	// SetStatus is used for LMTP only to set the answer for an Recipient
	SetStatus(rcpt string, status *SMTPError)
	// SetSMTPResponse can be used to overwrite default SMTP Accept Message after DATA finished (not for LMTP)
	SetSMTPResponse(response *SMTPError)
	StartDelivery(ctx context.Context, rcpt string)
	GetXForward() XForward
	GetHelo() string
}
