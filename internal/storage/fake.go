package storage

import (
	"net"
	"net/http/httptest"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// Fake is an in-memory S3 server for tests and local development.
type Fake struct {
	*httptest.Server
	backend *s3mem.Backend
}

// NewFake starts an in-memory S3 server with bucket created.
func NewFake(bucket string) (*Fake, error) { return NewFakeAt(bucket, "") }

// NewFakeAt is NewFake listening on addr (host:port; empty means a
// loopback port), e.g. an address containers can reach too.
func NewFakeAt(bucket, addr string) (*Fake, error) {
	be := s3mem.New()
	if err := be.CreateBucket(bucket); err != nil {
		return nil, err
	}
	srv := httptest.NewUnstartedServer(gofakes3.New(be).Server())
	if addr != "" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		_ = srv.Listener.Close()
		srv.Listener = ln
	}
	srv.Start()
	return &Fake{Server: srv, backend: be}, nil
}

// Objects lists the keys stored in bucket.
func (f *Fake) Objects(bucket string) ([]string, error) {
	res, err := f.backend.ListBucket(bucket, nil, gofakes3.ListBucketPage{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range res.Contents {
		out = append(out, c.Key)
	}
	return out, nil
}

// Target returns a target for the fake.
func (f *Fake) Target(bucket string) Target {
	return Target{Endpoint: f.URL, Region: "us-east-1", Bucket: bucket, AccessKey: "test", SecretKey: "test", PathStyle: true}
}
