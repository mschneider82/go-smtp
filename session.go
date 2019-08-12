package smtp

import "io"

// A DefaultSession is returned after successful login or anonymous DefaultSession
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
