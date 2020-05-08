package smtp

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ConnectionState struct {
	Hostname   string
	RemoteAddr net.Addr
	TLS        tls.ConnectionState
}

type Conn struct {
	conn          net.Conn
	text          *TextConn
	server        *Server
	helo          string
	nbrErrors     int
	session       Session
	locker        sync.Mutex
	XForward      *XForward
	fromReceived  bool
	recipients    []string
	recipientsmap map[string]struct{}
}

type XForward struct {
	Name, Addr, Proto, Helo string
}

func newConn(c net.Conn, s *Server) *Conn {
	sc := &Conn{
		server:        s,
		conn:          c,
		recipientsmap: make(map[string]struct{}),
		XForward:      new(XForward),
	}

	sc.init()
	return sc
}

func (c *Conn) init() {
	var rwc io.ReadWriteCloser = c.conn
	if c.server.debug != nil {
		rwc = struct {
			io.Reader
			io.Writer
			io.Closer
		}{
			io.TeeReader(c.conn, c.server.debug),
			io.MultiWriter(c.conn, c.server.debug),
			c.conn,
		}
	}

	//c.text = textproto.NewConn(rwc)
	c.text = NewTextConn(rwc)
}

func (c *Conn) unrecognizedCommand(cmd string) {
	c.WriteResponse(500, EnhancedCode{5, 5, 2}, fmt.Sprintf("Syntax error, %v command unrecognized", cmd))

	c.nbrErrors++
	if c.nbrErrors > 3 {
		c.WriteResponse(500, EnhancedCode{5, 5, 2}, "Too many unrecognized commands")
		c.Close()
	}
}

// Commands are dispatched to the appropriate handler functions.
func (c *Conn) handle(cmd string, arg string) {
	// If panic happens during command handling - send 421 response
	// and close connection.
	defer func() {
		if err := recover(); err != nil {
			c.WriteResponse(421, EnhancedCode{4, 0, 0}, "Internal server error")
			c.Close()

			stack := debug.Stack()
			c.server.errorLog.Printf("panic serving %v: %v\n%s", c.State().RemoteAddr, err, stack)
		}
	}()

	if cmd == "" {
		c.WriteResponse(500, EnhancedCode{5, 5, 2}, "Speak up")
		return
	}

	cmd = strings.ToUpper(cmd)
	switch cmd {
	case "SEND", "SOML", "SAML", "EXPN", "HELP", "TURN":
		// These commands are not implemented in any state
		c.WriteResponse(502, EnhancedCode{5, 5, 1}, fmt.Sprintf("%v command not implemented", cmd))
	case "HELO", "EHLO", "LHLO":
		lmtp := cmd == "LHLO"
		enhanced := lmtp || cmd == "EHLO"
		if c.server.lmtp && !lmtp {
			c.WriteResponse(500, EnhancedCode{5, 5, 1}, "This is a LMTP server, use LHLO")
		}
		if !c.server.lmtp && lmtp {
			c.WriteResponse(500, EnhancedCode{5, 5, 1}, "This is not a LMTP server")
		}
		c.handleGreet(enhanced, arg)
	case "XFORWARD":
		if !c.server.allowXForward {
			c.unrecognizedCommand(cmd)
		} else {
			c.handleXForward(arg)
		}
	case "MAIL":
		c.handleMail(arg)
	case "RCPT":
		c.handleRcpt(arg)
	case "VRFY":
		c.WriteResponse(252, EnhancedCode{2, 5, 0}, "Cannot VRFY user, but will accept message")
	case "NOOP":
		c.WriteResponse(250, EnhancedCode{2, 0, 0}, "I have sucessfully done nothing")
	case "RSET": // Reset session
		c.reset()
		c.WriteResponse(250, EnhancedCode{2, 0, 0}, "Session reset")
	case "DATA":
		c.handleData(arg)
	case "QUIT":
		c.WriteResponse(221, EnhancedCode{2, 0, 0}, "Goodnight and good luck")
		c.Close()
	case "AUTH":
		if c.server.authDisabled {
			c.unrecognizedCommand(cmd)
		} else {
			c.handleAuth(arg)
		}
	case "STARTTLS":
		c.handleStartTLS()
	default:
		c.unrecognizedCommand(cmd)
	}
}

func (c *Conn) Server() *Server {
	return c.server
}

func (c *Conn) Session() Session {
	c.locker.Lock()
	defer c.locker.Unlock()
	return c.session
}

// Setting the user resets any message being generated
func (c *Conn) SetSession(session Session) {
	c.locker.Lock()
	defer c.locker.Unlock()
	c.session = session
}

func (c *Conn) Close() error {
	if session := c.Session(); session != nil {
		session.Logout()
	}

	return c.conn.Close()
}

// TLSConnectionState returns the connection's TLS connection state.
// Zero values are returned if the connection doesn't use TLS.
func (c *Conn) TLSConnectionState() (state tls.ConnectionState, ok bool) {
	tc, ok := c.conn.(*tls.Conn)
	if !ok {
		return
	}
	return tc.ConnectionState(), true
}

func (c *Conn) State() ConnectionState {
	state := ConnectionState{}
	tlsState, ok := c.TLSConnectionState()
	if ok {
		state.TLS = tlsState
	}

	state.Hostname = c.helo
	state.RemoteAddr = c.conn.RemoteAddr()

	return state
}

func (c *Conn) authAllowed() bool {
	_, isTLS := c.TLSConnectionState()
	return !c.server.authDisabled && (isTLS || c.server.allowInsecureAuth)
}

// GREET state -> waiting for HELO
func (c *Conn) handleGreet(enhanced bool, arg string) {
	if !enhanced {
		domain, err := parseHelloArgument(arg)
		if err != nil {
			c.WriteResponse(501, EnhancedCode{5, 5, 2}, "Domain/address argument required for HELO")
			return
		}
		c.helo = domain

		c.WriteResponse(250, EnhancedCode{2, 0, 0}, fmt.Sprintf("Hello %s", domain))
	} else {
		domain, err := parseHelloArgument(arg)
		if err != nil {
			c.WriteResponse(501, EnhancedCode{5, 5, 2}, "Domain/address argument required for EHLO")
			return
		}

		c.helo = domain

		caps := []string{}
		caps = append(caps, c.server.caps...)
		if _, isTLS := c.TLSConnectionState(); c.server.tlsconfig != nil && !isTLS {
			caps = append(caps, "STARTTLS")
		}
		if c.authAllowed() {
			authCap := "AUTH"
			for name := range c.server.auths {
				authCap += " " + name
			}

			caps = append(caps, authCap)
		}
		if c.server.maxMessageBytes > 0 {
			caps = append(caps, fmt.Sprintf("SIZE %v", c.server.maxMessageBytes))
		}
		if c.server.allowXForward {
			caps = append(caps, "XFORWARD NAME ADDR PROTO HELO")
		}

		args := []string{"Hello " + domain}
		args = append(args, caps...)
		c.WriteResponse(250, NoEnhancedCode, args...)
	}
}

// handleXForward client send xforward infos
func (c *Conn) handleXForward(arg string) {
	// arg can be          NAME=example.com ADDR=192.168.0.1 PROTO=ESMTP
	// or/and just         HELO=mail.example.com
	args := strings.Split(arg, " ")
	for _, a := range args {
		kv := strings.Split(a, "=")
		switch strings.ToUpper(kv[0]) {
		case "NAME":
			c.XForward.Name = kv[1]
		case "ADDR":
			c.XForward.Addr = kv[1]
		case "PROTO":
			c.XForward.Proto = kv[1]
		case "HELO":
			c.XForward.Helo = kv[1]
		default:
			c.WriteResponse(501, EnhancedCode{2, 5, 1}, "Bad command parameter syntax")
			return
		}
	}
	c.WriteResponse(250, EnhancedCode{2, 0, 0}, "Ok")
}

// READY state -> waiting for MAIL
func (c *Conn) handleMail(arg string) {
	if c.helo == "" {
		c.WriteResponse(502, EnhancedCode{2, 5, 1}, "Please introduce yourself first.")
		return
	}

	if c.Session() == nil {
		state := c.State()
		session, err := c.server.backend.AnonymousLogin(&state)
		if err != nil {
			if smtpErr, ok := err.(*SMTPError); ok {
				c.WriteResponse(smtpErr.Code, smtpErr.EnhancedCode, smtpErr.Message)
			} else {
				c.WriteResponse(502, EnhancedCode{5, 7, 0}, err.Error())
			}
			return
		}

		c.SetSession(session)
	}

	if len(arg) < 6 || strings.ToUpper(arg[0:5]) != "FROM:" {
		c.WriteResponse(501, EnhancedCode{5, 5, 2}, "Was expecting MAIL arg syntax of FROM:<address>")
		return
	}
	fromArgs := strings.Split(strings.Trim(arg[5:], " "), " ")
	if c.server.strict {
		if !strings.HasPrefix(fromArgs[0], "<") || !strings.HasSuffix(fromArgs[0], ">") {
			c.WriteResponse(501, EnhancedCode{5, 5, 2}, "Was expecting MAIL arg syntax of FROM:<address>")
			return
		}
	}
	from := fromArgs[0]
	if from == "" {
		c.WriteResponse(501, EnhancedCode{5, 5, 2}, "Was expecting MAIL arg syntax of FROM:<address>")
		return
	}
	from = strings.Trim(from, "<>")

	// This is where the Conn may put BODY=8BITMIME, but we already
	// read the DATA as bytes, so it does not effect our processing.
	if len(fromArgs) > 1 {
		args, err := parseArgs(fromArgs[1:])
		if err != nil {
			c.WriteResponse(501, EnhancedCode{5, 5, 4}, "Unable to parse MAIL ESMTP parameters")
			return
		}

		if args["SIZE"] != "" {
			size, err := strconv.ParseInt(args["SIZE"], 10, 32)
			if err != nil {
				c.WriteResponse(501, EnhancedCode{5, 5, 4}, "Unable to parse SIZE as an integer")
				return
			}

			if c.server.maxMessageBytes > 0 && int(size) > c.server.maxMessageBytes {
				c.WriteResponse(552, EnhancedCode{5, 3, 4}, "Max message size exceeded")
				return
			}
		}
	}

	if err := c.Session().Mail(from); err != nil {
		if smtpErr, ok := err.(*SMTPError); ok {
			c.WriteResponse(smtpErr.Code, smtpErr.EnhancedCode, smtpErr.Message)
			return
		}
		c.WriteResponse(451, EnhancedCode{4, 0, 0}, err.Error())
		return
	}

	c.WriteResponse(250, EnhancedCode{2, 0, 0}, fmt.Sprintf("Roger, accepting mail from <%v>", from))
	c.fromReceived = true
}

// MAIL state -> waiting for RCPTs followed by DATA
func (c *Conn) handleRcpt(arg string) {
	if !c.fromReceived {
		c.WriteResponse(502, EnhancedCode{5, 5, 1}, "Missing MAIL FROM command.")
		return
	}

	if (len(arg) < 4) || (strings.ToUpper(arg[0:3]) != "TO:") {
		c.WriteResponse(501, EnhancedCode{5, 5, 2}, "Was expecting RCPT arg syntax of TO:<address>")
		return
	}

	// TODO: This trim is probably too forgiving
	recipient := strings.Trim(arg[3:], "<> ")

	if c.server.maxRecipients > 0 && len(c.recipients) >= c.server.maxRecipients {
		c.WriteResponse(552, EnhancedCode{5, 5, 3}, fmt.Sprintf("Maximum limit of %v recipients reached", c.server.maxRecipients))
		return
	}

	//
	if c.server.lmtp {
		if _, ok := c.recipientsmap[strings.ToLower(recipient)]; ok {
			c.WriteResponse(451, EnhancedCode{4, 0, 0}, fmt.Sprintf("Duplicate RCPT TO:<%s>. Please try again later.", recipient))
			return
		}
	}

	if err := c.Session().Rcpt(recipient); err != nil {
		if smtpErr, ok := err.(*SMTPError); ok {
			c.WriteResponse(smtpErr.Code, smtpErr.EnhancedCode, smtpErr.Message)
			return
		}
		c.WriteResponse(451, EnhancedCode{4, 0, 0}, err.Error())
		return
	}
	c.recipients = append(c.recipients, strings.ToLower(recipient))
	c.recipientsmap[strings.ToLower(recipient)] = struct{}{}
	c.WriteResponse(250, EnhancedCode{2, 0, 0}, fmt.Sprintf("I'll make sure <%v> gets this", recipient))
}

func (c *Conn) handleAuth(arg string) {
	if c.helo == "" {
		c.WriteResponse(502, EnhancedCode{5, 5, 1}, "Please introduce yourself first.")
		return
	}

	parts := strings.Fields(arg)
	if len(parts) == 0 {
		c.WriteResponse(502, EnhancedCode{5, 5, 4}, "Missing parameter")
		return
	}

	mechanism := strings.ToUpper(parts[0])

	// Parse client initial response if there is one
	var ir []byte
	if len(parts) > 1 {
		var err error
		ir, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return
		}
	}

	newSasl, ok := c.server.auths[mechanism]
	if !ok {
		c.WriteResponse(504, EnhancedCode{5, 7, 4}, "Unsupported authentication mechanism")
		return
	}

	sasl := newSasl(c)

	response := ir
	for {
		challenge, done, err := sasl.Next(response)
		if err != nil {
			if smtpErr, ok := err.(*SMTPError); ok {
				c.WriteResponse(smtpErr.Code, smtpErr.EnhancedCode, smtpErr.Message)
				return
			}
			c.WriteResponse(454, EnhancedCode{4, 7, 0}, err.Error())
			return
		}

		if done {
			break
		}

		encoded := ""
		if len(challenge) > 0 {
			encoded = base64.StdEncoding.EncodeToString(challenge)
		}
		c.WriteResponse(334, NoEnhancedCode, encoded)

		encoded, err = c.ReadLine()
		if err != nil {
			return // TODO: error handling
		}

		response, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			c.WriteResponse(454, EnhancedCode{4, 7, 0}, "Invalid base64 data")
			return
		}
	}

	if c.Session() != nil {
		c.WriteResponse(235, EnhancedCode{2, 0, 0}, "Authentication succeeded")
	}
}

func (c *Conn) handleStartTLS() {
	if _, isTLS := c.TLSConnectionState(); isTLS {
		c.WriteResponse(502, EnhancedCode{5, 5, 1}, "Already running in TLS")
		return
	}

	if c.server.tlsconfig == nil {
		c.WriteResponse(502, EnhancedCode{5, 5, 1}, "TLS not supported")
		return
	}

	c.WriteResponse(220, EnhancedCode{2, 0, 0}, "Ready to start TLS")

	// Upgrade to TLS
	var tlsConn *tls.Conn
	tlsConn = tls.Server(c.conn, c.server.tlsconfig)

	if err := tlsConn.Handshake(); err != nil {
		c.WriteResponse(550, EnhancedCode{5, 0, 0}, "Handshake error")
	}

	c.conn = tlsConn
	c.init()

	// Reset envelope as a new EHLO/HELO is required after STARTTLS
	c.reset()
}

// DATA
func (c *Conn) handleData(arg string) {
	if arg != "" {
		c.WriteResponse(501, EnhancedCode{5, 5, 4}, "DATA command should not have any arguments")
		return
	}

	if !c.fromReceived || len(c.recipients) == 0 {
		c.WriteResponse(502, EnhancedCode{5, 5, 1}, "Missing RCPT TO command.")
		return
	}

	// We have recipients, go to accept data
	c.WriteResponse(354, EnhancedCode{2, 0, 0}, "Go ahead. End your data with <CR><LF>.<CR><LF>")

	var (
		code         int
		enhancedCode EnhancedCode
		msg          string
	)
	r := newDataReader(c)
	dataContext := newdataContext(c.XForward)
	dataContext.helo = c.helo
	err := c.Session().Data(r, dataContext)
	io.Copy(ioutil.Discard, r) // Make sure all the data has been consumed
	if err != nil {
		if smtperr, ok := err.(*SMTPError); ok {
			code = smtperr.Code
			enhancedCode = smtperr.EnhancedCode
			msg = smtperr.Message
		} else {
			code = 554
			enhancedCode = EnhancedCode{5, 0, 0}
			msg = "Error: transaction failed, blame it on the weather: " + err.Error()
		}
	} else {
		if dataContext.smtpresponse == nil {
			code = 250
			enhancedCode = EnhancedCode{2, 0, 0}
			msg = "OK: queued"
		} else {
			code, enhancedCode, msg = dataContext.smtpresponse.Code, dataContext.smtpresponse.EnhancedCode, dataContext.smtpresponse.Message
		}
	}

	if c.server.lmtp {
		for _, rcpt := range c.recipients {
			var status *SMTPError
			rcptStatus := dataContext.rcptStatus[rcpt]
			select {
			case <-rcptStatus.ctx.Done():
				c.Server().errorLog.Printf("Context Error: %s - tempfailing", rcptStatus.ctx.Err())
				status = &SMTPError{
					Code:         420,
					EnhancedCode: EnhancedCode{4, 4, 7},
					Message:      "Error: timeout reached",
				}
			case status = <-rcptStatus.ch:
			}
			c.WriteResponse(status.Code, status.EnhancedCode, "<"+rcpt+"> "+status.Message)
		}

	} else {
		c.WriteResponse(code, enhancedCode, msg)
	}

	c.reset()
}

type rcptStatus struct {
	ctx context.Context
	ch  chan *SMTPError
}

type dataContext struct {
	rcptStatus   map[string]*rcptStatus
	xforwarded   *XForward
	helo         string
	smtpresponse *SMTPError
}

func newdataContext(xforwarded *XForward) *dataContext {
	return &dataContext{
		rcptStatus: make(map[string]*rcptStatus),
		xforwarded: xforwarded,
	}
}

func (s *dataContext) SetSMTPResponse(response *SMTPError) {
	s.smtpresponse = response
}

func (s *dataContext) SetStatus(rcpt string, status *SMTPError) {
	rcpt = strings.ToLower(rcpt)
	s.rcptStatus[rcpt].ch <- status
}

func (s *dataContext) StartDelivery(ctx context.Context, rcpt string) {
	rcpt = strings.ToLower(rcpt)
	s.rcptStatus[rcpt] = &rcptStatus{
		ch:  make(chan *SMTPError, 1),
		ctx: ctx,
	}
}

func (s *dataContext) GetXForward() XForward {
	return *s.xforwarded
}

func (s *dataContext) GetHelo() string {
	return s.helo
}

func (c *Conn) Reject() {
	c.WriteResponse(421, EnhancedCode{4, 4, 5}, "Too busy. Try again later.")
	c.Close()
}

func (c *Conn) greet() {
	c.WriteResponse(220, NoEnhancedCode, fmt.Sprintf("%v ESMTP Service Ready", c.server.domain))
}

func (c *Conn) WriteResponse(code int, enhCode EnhancedCode, text ...string) {
	// TODO: error handling
	if c.server.writeTimeout != 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.server.writeTimeout))
	}

	// All responses must include an enhanced code, if it is missing - use
	// a generic code X.0.0.
	if enhCode == EnhancedCodeNotSet {
		cat := code / 100
		switch cat {
		case 2, 4, 5:
			enhCode = EnhancedCode{cat, 0, 0}
		default:
			enhCode = NoEnhancedCode
		}
	}

	for i := 0; i < len(text)-1; i++ {
		c.text.PrintfLine("%v-%v", code, text[i])
	}
	if enhCode == NoEnhancedCode {
		c.text.PrintfLine("%v %v", code, text[len(text)-1])
	} else {
		c.text.PrintfLine("%v %v.%v.%v %v", code, enhCode[0], enhCode[1], enhCode[2], text[len(text)-1])
	}
}

// Reads a line of input
func (c *Conn) ReadLine() (string, error) {
	if c.server.readTimeout != 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(c.server.readTimeout)); err != nil {
			return "", err
		}
	}

	return c.text.ReadLine()
}

func (c *Conn) reset() {
	c.locker.Lock()
	defer c.locker.Unlock()

	if c.session != nil {
		c.session.Reset()
	}
	c.fromReceived = false
	c.recipients = nil
	c.recipientsmap = make(map[string]struct{})
	c.XForward = new(XForward)
}
