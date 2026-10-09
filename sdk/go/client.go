package pgdock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Version is the SDK's version, sent as X-Client-Info.
const Version = "0.1.0"

// Client is a project's client. With the secret key it acts as the
// project's service role (row-level security doesn't apply); with the
// publishable key as anon, or as a signed-in user once Auth has a
// session. WithToken makes a copy that acts as a user from their access
// token, which is how a server handles a request on a user's behalf.
type Client struct {
	URL string
	key string
	hc  *http.Client
	hdr http.Header

	Auth     *Auth
	Data     *Data
	Storage  *Storage
	Realtime *Realtime

	// token is a fixed user token (WithToken); empty uses Auth's session.
	token string
}

// Option configures New.
type Option func(*Client)

// WithHTTPClient sets the HTTP client (default: 30-second timeout).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.hc = h } }

// WithHeader adds a header to every request.
func WithHeader(k, v string) Option { return func(c *Client) { c.hdr.Set(k, v) } }

// New makes a client from the project URL (https://<ref>.<domain>) and a key.
func New(projectURL, key string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(projectURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("pgdock: the project URL is https://<ref>.<domain>, not %q", projectURL)
	}
	if key == "" {
		return nil, errors.New("pgdock: a key is required")
	}
	c := &Client{URL: u.String(), key: key, hc: &http.Client{Timeout: 30 * time.Second}, hdr: http.Header{}}
	c.hdr.Set("X-Client-Info", "pgdock-go/"+Version)
	for _, o := range opts {
		o(c)
	}
	c.Auth = &Auth{c: c}
	c.Data = &Data{c: c, schema: "public"}
	c.Storage = &Storage{c: c}
	c.Realtime = newRealtime(c)
	return c, nil
}

// WithToken is a copy of c that acts as the user whose access token this
// is (verify it first with Auth.VerifyToken when it came from a request).
// Make c with the publishable key: with the secret key, requests act as
// the service role whatever token they carry.
func (c *Client) WithToken(accessToken string) *Client {
	cc := *c
	cc.token = accessToken
	cc.Auth = &Auth{c: &cc}
	cc.Data = &Data{c: &cc, schema: c.Data.schema}
	cc.Storage = &Storage{c: &cc}
	cc.Realtime = newRealtime(&cc)
	return &cc
}

// accessToken is the token requests carry: WithToken's, or the session's
// (refreshed first when it is about to expire).
func (c *Client) accessToken(ctx context.Context) (string, error) {
	if c.token != "" {
		return c.token, nil
	}
	return c.Auth.currentToken(ctx)
}

type request struct {
	method  string
	path    string
	query   url.Values
	body    any       // JSON
	raw     io.Reader // a file
	size    int64
	header  http.Header
	token   *string // override the token ("" for none)
	noToken bool
}

func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	u := c.URL + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	var body io.Reader = r.raw
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	method := r.method
	if method == "" {
		method = http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if r.raw != nil && r.size > 0 {
		req.ContentLength = r.size
	}
	for k, v := range c.hdr {
		req.Header[k] = v
	}
	req.Header.Set("apikey", c.key)
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	tok := ""
	switch {
	case r.token != nil:
		tok = *r.token
	case !r.noToken:
		if tok, err = c.accessToken(ctx); err != nil {
			return nil, err
		}
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, apiError(res)
	}
	return res, nil
}

// doJSON makes a request and decodes the answer into out (when not nil).
func (c *Client) doJSON(ctx context.Context, r request, out any) error {
	res, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("pgdock: decoding the answer: %w", err)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
