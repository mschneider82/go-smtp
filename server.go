package smtp

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
)

// A function that creates SASL servers.
type SaslServerFactory func(conn *Conn) sasl.Server

// Logger interface is used by Server to report unexpected internal errors.
type Logger interface {
	Printf(format string, v ...interface{})
	Println(v ...interface{})
}

// An Option configures a Server using functional options
type Option interface {
	apply(*Server)
}

type optionFunc func(*Server)

func (f optionFunc) apply(server *Server) { f(server) }

// New generates a new Server
func NewServer(be Backend, opts ...Option) *Server {
	server := newServer(be)
	for _, opt := range opts {
		opt.apply(server)
	}
	return server
}

func Addr(addr string) Option {
	return optionFunc(func(server *Server) {
		server.addr = addr
	})
}

func TLSConfig(tlsconfig *tls.Config) Option {
	return optionFunc(func(server *Server) {
		server.tlsconfig = tlsconfig
	})
}

func LMTP() Option {
	return optionFunc(func(server *Server) {
		server.lmtp = true
	})
}

func UnixSocket() Option {
	return optionFunc(func(server *Server) {
		server.network = "unix"
	})
}

func Domain(domain string) Option {
	return optionFunc(func(server *Server) {
		server.domain = domain
	})
}

func MaxRecipients(maxRcpts int) Option {
	return optionFunc(func(server *Server) {
		server.maxRecipients = maxRcpts
	})
}

func MaxMessageBytes(maxMsgBytes int) Option {
	return optionFunc(func(server *Server) {
		server.maxMessageBytes = maxMsgBytes
	})
}

func AllowInsecureAuth() Option {
	return optionFunc(func(server *Server) {
		server.allowInsecureAuth = true
	})
}

func AllowXForward() Option {
	return optionFunc(func(server *Server) {
		server.allowXForward = true
	})
}

func StrictMode() Option {
	return optionFunc(func(server *Server) {
		server.strict = true
	})
}

func DebugToWriter(i io.Writer) Option {
	return optionFunc(func(server *Server) {
		server.debug = i
	})
}

func ErrorLogger(l Logger) Option {
	return optionFunc(func(server *Server) {
		server.errorLog = l
	})
}

func ReadTimeout(t time.Duration) Option {
	return optionFunc(func(server *Server) {
		server.readTimeout = t
	})
}

func WriteTimeout(t time.Duration) Option {
	return optionFunc(func(server *Server) {
		server.writeTimeout = t
	})
}

func DisableAuth() Option {
	return optionFunc(func(server *Server) {
		server.authDisabled = true
	})
}

// A SMTP server.
type Server struct {
	// TCP or Unix address to listen on.
	addr string
	// The server TLS configuration.
	tlsconfig *tls.Config
	// Enable LMTP mode, as defined in RFC 2033.
	lmtp bool
	// Network defines if tcp or unix socket. default tcp
	network string

	domain            string
	maxRecipients     int
	maxMessageBytes   int
	allowInsecureAuth bool
	allowXForward     bool
	strict            bool
	debug             io.Writer
	errorLog          Logger
	readTimeout       time.Duration
	writeTimeout      time.Duration

	// If set, the AUTH command will not be advertised and authentication
	// attempts will be rejected. This setting overrides AllowInsecureAuth.
	authDisabled bool

	// The server backend.
	backend Backend

	listener net.Listener
	caps     []string
	auths    map[string]SaslServerFactory
	done     chan struct{}
	locker   sync.Mutex
	conns    map[*Conn]struct{}
}

// new creates a new SMTP server.
func newServer(be Backend) *Server {
	return &Server{
		backend:  be,
		done:     make(chan struct{}, 1),
		errorLog: log.New(os.Stderr, "smtp/server ", log.LstdFlags),
		caps:     []string{"PIPELINING", "8BITMIME", "ENHANCEDSTATUSCODES"},
		auths: map[string]SaslServerFactory{
			sasl.Plain: func(conn *Conn) sasl.Server {
				return sasl.NewPlainServer(func(identity, username, password string) error {
					if identity != "" && identity != username {
						return errors.New("Identities not supported")
					}

					state := conn.State()
					session, err := be.Login(&state, username, password)
					if err != nil {
						return err
					}

					conn.SetSession(session)
					return nil
				})
			},
		},
		conns: make(map[*Conn]struct{}),
	}
}

// Serve accepts incoming connections on the Listener l.
func (s *Server) Serve(l net.Listener) error {
	s.listener = l
	defer s.Close()

	for {
		c, err := l.Accept()
		if err != nil {
			select {
			case <-s.done:
				// we called Close()
				return nil
			default:
				return err
			}
		}

		go s.handleConn(newConn(c, s))
	}
}

func (s *Server) handleConn(c *Conn) error {
	s.locker.Lock()
	s.conns[c] = struct{}{}
	s.locker.Unlock()

	defer func() {
		c.Close()

		s.locker.Lock()
		delete(s.conns, c)
		s.locker.Unlock()
	}()

	c.greet()

	for {
		line, err := c.ReadLine()
		if err == nil {
			cmd, arg, err := parseCmd(line)
			if err != nil {
				c.nbrErrors++
				c.WriteResponse(501, EnhancedCode{5, 5, 2}, "Bad command")
				continue
			}

			c.handle(cmd, arg)
		} else {
			if err == io.EOF {
				return nil
			}

			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				c.WriteResponse(221, EnhancedCode{2, 4, 2}, "Idle timeout, bye bye")
				return nil
			}

			c.WriteResponse(221, EnhancedCode{2, 4, 0}, "Connection error, sorry")
			return err
		}
	}
}

// ListenAndServe listens on the network address s.Addr and then calls Serve
// to handle requests on incoming connections.
//
// If s.Addr is blank ":smtp" is used.
func (s *Server) ListenAndServe() error {
	network := "tcp"
	if s.network == "unix" {
		network = "unix"
	}

	addr := s.addr
	if addr == "" {
		addr = ":smtp"
	}

	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}

	return s.Serve(l)
}

// ListenAndServeTLS listens on the TCP network address s.Addr and then calls
// Serve to handle requests on incoming TLS connections.
//
// If s.Addr is blank, ":smtps" is used.
func (s *Server) ListenAndServeTLS() error {
	addr := s.addr
	if addr == "" {
		addr = ":smtps"
	}

	l, err := tls.Listen("tcp", addr, s.tlsconfig)
	if err != nil {
		return err
	}

	return s.Serve(l)
}

// Close stops the server.
func (s *Server) Close() {
	s.done <- struct{}{}
	s.listener.Close()

	s.locker.Lock()
	defer s.locker.Unlock()

	for conn := range s.conns {
		conn.Close()
	}
}

// EnableAuth enables an authentication mechanism on this server.
//
// This function should not be called directly, it must only be used by
// libraries implementing extensions of the SMTP protocol.
func (s *Server) EnableAuth(name string, f SaslServerFactory) {
	s.auths[name] = f
}

// ForEachConn iterates through all opened connections.
func (s *Server) ForEachConn(f func(*Conn)) {
	s.locker.Lock()
	defer s.locker.Unlock()
	for conn := range s.conns {
		f(conn)
	}
}
