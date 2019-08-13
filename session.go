package smtp

import "io"

// A DefaultSession can be used as a basic implementation for Session Interface
// It has already a From and Rcpts[] field, mostly used to embett in your own
// Session struct.
type DefaultSession struct {
	From  string
	Rcpts []string
}

func (s *DefaultSession) Mail(from string) error {
	s.From = from
	return nil
}

func (s *DefaultSession) Rcpt(to string) error {
	s.Rcpts = append(s.Rcpts, to)
	return nil
}

func (s *DefaultSession) Data(r io.Reader, sc DataContext) error {
	return nil
}

func (s *DefaultSession) Reset() {
	s.From = ""
	s.Rcpts = []string{}
}

func (s *DefaultSession) Logout() error {
	return nil
}
