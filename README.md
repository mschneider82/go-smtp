# go-smtp

[![GoDoc](https://godoc.org/github.com/mschneider82/go-smtp?status.svg)](https://godoc.org/github.com/mschneider82/go-smtp)
[![codecov](https://codecov.io/gh/mschneider82/go-smtp/branch/master/graph/badge.svg)](https://codecov.io/gh/mschneider82/go-smtp)

This is a forked version of go-smtp, changed API to function options.
Added NewDefaultBackend and DefaultSession for easier usage.
Client has a own package. Added XForward support

## Features

* ESMTP client & server implementing [RFC 5321](https://tools.ietf.org/html/rfc5321)
* Support for SMTP [AUTH](https://tools.ietf.org/html/rfc4954) and [PIPELINING](https://tools.ietf.org/html/rfc2920)
* UTF-8 support for subject and message
* [LMTP](https://tools.ietf.org/html/rfc2033) support
* [XFORWARD](http://www.postfix.org/XFORWARD_README.html) support
* Since v1.1.2: Keep \r\n in Data Reader (textproto DotReader replaces it to \n)

### SMTP Server

```go
package main

import (
	"io"
	"io/ioutil"
	"log"
	"time"

	"github.com/mschneider82/go-smtp"
)

// A Session is returned after successful login.
type Session struct{
	smtp.DefaultSession
}


func (s *Session) Data(r io.Reader, d smtp.DataContext) error {
	if b, err := ioutil.ReadAll(r); err != nil {
		return err
	} else {
		log.Println("Data:", string(b))
	}
	return nil
}

type sessionFactory struct{}

func (s *sessionFactory) New() smtp.Session {
	return &Session{}
}

func main() {
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
```

You can use the server manually with `telnet`:
```
$ telnet localhost 1025
EHLO localhost
AUTH PLAIN
AHVzZXJuYW1lAHBhc3N3b3Jk
MAIL FROM:<root@nsa.gov>
RCPT TO:<root@gchq.gov.uk>
DATA
Hey <3
.
```

### LMTP Server

```go
package main

import (
	"context"
	"io"
	"io/ioutil"
	"log"
	"time"
	"fmt"

	"github.com/mschneider82/go-smtp"
)

// Session is returned for every connection
type Session struct{
	smtp.DefaultSession
}

func (s *Session) Data(r io.Reader, dataContext smtp.DataContext) error {
	mailBytes, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	} 
	
	globalCtx := context.Background()

	for i, rcpt := range s.Rcpts {
		rcptCtx, _ := context.WithTimeout(globalCtx, 2*time.Second)
		// we have to assing i and rcpt new to access them in the go() routine
		rcpt := rcpt
		i := i

		dataContext.StartDelivery(rcptCtx, rcpt)
		go func() {
			// normaly we would deliver the mailBytes to our Maildir/HTTP backend
			// in this case we just do a sleep 
			time.Sleep(time.Duration(2+i) * time.Second)
			fmt.Println(string(mailBytes))
            // Lets finish with OK (if the request wasn't canceled because of the ctx timeout)
			dataContext.SetStatus(rcpt, &smtp.SMTPError{
				Code: 250,
				EnhancedCode:  smtp.EnhancedCode{2, 0, 0},
				Message: "Finished",
			})
		}()

	}
	// we always return nil in LMTP because every rcpt return code was set with dataContext.SetStatus()
	return nil
}

func main() {
	err := smtp.NewServer(
		smtp.NewDefaultBackend(&Session{}),
		smtp.Addr(":1025"),
		smtp.Domain("localhost"),
		smtp.WriteTimeout(10*time.Second),
		smtp.ReadTimeout(10*time.Second),
		smtp.MaxMessageBytes(1024*1024),
		smtp.MaxRecipients(50),
		smtp.AllowInsecureAuth(),
		smtp.DisableAuth(),
		smtp.LMTP(),
	).ListenAndServe()

	if err != nil {
		log.Fatal(err)
	}
}
```

You can use the server manually with `telnet`:
```
$ telnet localhost 1025
LHLO localhost
MAIL FROM:<from@example.com>
RCPT TO:<rcpt1@example.com>
RCPT TO:<rcpt2@example.com>
RCPT TO:<rcpt3@example.com>
DATA
Hey <3
.
250 2.0.0 <rcpt1@example.com> Finished
420 4.4.7 <rcpt2@example.com> Error: timeout reached
420 4.4.7 <rcpt3@example.com> Error: timeout reached
```


## Licence

MIT
