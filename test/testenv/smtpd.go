package testenv

import (
	"encoding/base64"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
)

// Mail is a message the test SMTP server received.
type Mail struct {
	From string
	To   []string
	Data string
	Auth string // the AUTH PLAIN credentials (decoded), if any
}

// SMTPServer is a minimal plain-text SMTP server (no TLS) on 127.0.0.1.
type SMTPServer struct {
	Addr string
	mu   sync.Mutex
	mail []Mail
	ln   net.Listener
}

// StartSMTP starts the test SMTP server; it stops when the test ends.
func StartSMTP(t testing.TB) *SMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &SMTPServer{Addr: ln.Addr().String(), ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// Mail returns the messages received so far.
func (s *SMTPServer) Mail() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.mail...)
}

func (s *SMTPServer) serve(c net.Conn) {
	defer c.Close()
	tp := textproto.NewConn(c)
	_ = tp.PrintfLine("220 pgdock-test ESMTP")
	var m Mail
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			_ = tp.PrintfLine("250-pgdock-test")
			_ = tp.PrintfLine("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			m.Auth = decodePlain(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			_ = tp.PrintfLine("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			m.From = strings.Trim(line[len("MAIL FROM:"):], "<> ")
			_ = tp.PrintfLine("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			m.To = append(m.To, strings.Trim(line[len("RCPT TO:"):], "<> "))
			_ = tp.PrintfLine("250 ok")
		case cmd == "DATA":
			_ = tp.PrintfLine("354 go ahead")
			data, err := tp.ReadDotLines()
			if err != nil {
				return
			}
			m.Data = strings.Join(data, "\n")
			s.mu.Lock()
			s.mail = append(s.mail, m)
			s.mu.Unlock()
			m = Mail{Auth: m.Auth}
			_ = tp.PrintfLine("250 queued")
		case cmd == "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("250 ok")
		}
	}
}

func decodePlain(s string) string {
	b, err := base64Decode(s)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(b), "\x00", "|")
}

func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// Count is how many messages to addr contain substr.
func (s *SMTPServer) Count(addr, substr string) int {
	n := 0
	for _, m := range s.Mail() {
		for _, to := range m.To {
			if strings.EqualFold(to, addr) && strings.Contains(m.Data, substr) {
				n++
				break
			}
		}
	}
	return n
}
