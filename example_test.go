// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package smtp_test

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/mschneider82/go-smtp"
	"github.com/mschneider82/go-smtp/smtpclient"
)

func ExampleDial() {
	// Connect to the remote SMTP server.
	c, err := smtpclient.Dial("mail.example.com:25")
	if err != nil {
		log.Fatal(err)
	}

	// Set the sender and recipient first
	if err := c.Mail("sender@example.org"); err != nil {
		log.Fatal(err)
	}
	if err := c.Rcpt("recipient@example.net"); err != nil {
		log.Fatal(err)
	}

	// Send the email body.
	wc, err := c.Data()
	if err != nil {
		log.Fatal(err)
	}
	_, err = fmt.Fprintf(wc, "This is the email body")
	if err != nil {
		log.Fatal(err)
	}
	err = wc.Close()
	if err != nil {
		log.Fatal(err)
	}

	// Send the QUIT command and close the connection.
	err = c.Quit()
	if err != nil {
		log.Fatal(err)
	}
}

// variables to make ExamplePlainAuth compile, without adding
// unnecessary noise there.
var (
	from       = "gopher@example.net"
	msg        = strings.NewReader("dummy message")
	recipients = []string{"foo@example.com"}
)

func ExampleSendMail_PlainAuth() {
	// hostname is used by PlainAuth to validate the TLS certificate.
	hostname := "mail.example.com"
	auth := sasl.NewPlainClient("", "user@example.com", "password")

	err := smtpclient.SendMail(hostname+":25", auth, from, recipients, msg)
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleSendMail() {
	// Set up authentication information.
	auth := sasl.NewPlainClient("", "user@example.com", "password")

	// Connect to the server, authenticate, set the sender and recipient,
	// and send the email all in one step.
	to := []string{"recipient@example.net"}
	msg := strings.NewReader("To: recipient@example.net\r\n" +
		"Subject: discount Gophers!\r\n" +
		"\r\n" +
		"This is the email body.\r\n")
	err := smtpclient.SendMail("mail.example.com:25", auth, "sender@example.org", to, msg)
	if err != nil {
		log.Fatal(err)
	}
}

// A Session is returned after successful login.
type Session struct {
	smtp.DefaultSession
}

func (s *Session) Data(r io.Reader, sc smtp.DataContext) error {
	if b, err := ioutil.ReadAll(r); err != nil {
		return err
	} else {
		log.Println("Data:", string(b))
	}
	return nil
}

func (s *Session) Reset() {}

func (s *Session) Logout() error {
	return nil
}

type sessionFactory struct {
}

func (s *sessionFactory) New() smtp.Session {
	return &Session{}
}

func ExampleNew() {
	err := smtp.NewServer(
		smtp.NewDefaultBackend(&sessionFactory{}),
		smtp.Addr(":1025"),
		smtp.Domain("localhost"),
		smtp.WriteTimeout(10*time.Second),
		smtp.ReadTimeout(10*time.Second),
		smtp.MaxMessageBytes(1024*1024),
		smtp.MaxRecipients(50),
		smtp.AllowInsecureAuth(),
		smtp.DisableAuth(),
	).ListenAndServe()

	if err != nil {
		log.Fatal(err)
	}
}
