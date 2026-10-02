// Command fakesmtp is a catch-all SMTP server for the e2e bundle. It keeps
// every message in memory and lists them as JSON over HTTP, so browser
// tests can follow verification and invitation links.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

// Message is one received email.
type Message struct {
	From       string    `json:"from"`
	To         []string  `json:"to"`
	Data       string    `json:"data"`
	ReceivedAt time.Time `json:"received_at"`
}

type inbox struct {
	mu   sync.Mutex
	msgs []Message
}

func (b *inbox) add(m Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, m)
}

func (b *inbox) list(to string) []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Message{}
	for _, m := range b.msgs {
		for _, r := range m.To {
			if to == "" || strings.EqualFold(r, to) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

func serve(c net.Conn, box *inbox) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Minute))
	tp := textproto.NewConn(c)
	_ = tp.PrintfLine("220 fakesmtp ESMTP")
	var m Message
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			_ = tp.PrintfLine("250-fakesmtp")
			_ = tp.PrintfLine("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(cmd, "AUTH"):
			_ = tp.PrintfLine("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			m = Message{From: strings.Trim(line[len("MAIL FROM:"):], "<> ")}
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
			m.Data, m.ReceivedAt = strings.Join(data, "\n"), time.Now().UTC()
			box.add(m)
			_ = tp.PrintfLine("250 queued")
		case cmd == "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("250 ok")
		}
	}
}

func main() {
	smtpAddr := flag.String("smtp", ":2525", "SMTP listen address")
	httpAddr := flag.String("http", ":8025", "HTTP listen address (GET /messages?to=)")
	flag.Parse()
	box := &inbox{}
	ln, err := net.Listen("tcp", *smtpAddr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Fatal(err)
			}
			go serve(c, box)
		}
	}()
	http.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(box.list(r.URL.Query().Get("to")))
	})
	log.Printf("fakesmtp: SMTP on %s, HTTP on %s", *smtpAddr, *httpAddr)
	srv := &http.Server{Addr: *httpAddr, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
