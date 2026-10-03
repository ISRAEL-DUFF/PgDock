package storage

import "testing"

func TestTransportBoundsConnectAndReply(t *testing.T) {
	tr := transport()
	if tr.TLSHandshakeTimeout != tlsHandshakeTimeout || tr.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Fatalf("timeouts not set: %v %v", tr.TLSHandshakeTimeout, tr.ResponseHeaderTimeout)
	}
	if tr.DialContext == nil {
		t.Fatal("no dial timeout")
	}
}
