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
	s Session
}

func NewDefaultBackend(s Session) *DefaultBackend {
	return &DefaultBackend{s: s}
}

// Login returns a session
func (be *DefaultBackend) Login(state *ConnectionState, username, password string) (Session, error) {
	return be.s, nil
}

// AnonymousLogin is not implemented in default backend
func (be *DefaultBackend) AnonymousLogin(state *ConnectionState) (Session, error) {
	return be.s, nil
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
	SetStatus(rcpt string, status *SMTPError)
	StartDelivery(ctx context.Context, rcpt string)
}
