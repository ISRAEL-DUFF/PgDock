package pgdock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Storage is buckets and files.
type Storage struct{ c *Client }

// Bucket is one bucket's files.
func (s *Storage) Bucket(id string) *Bucket { return &Bucket{c: s.c, ID: id} }

// FileObject is a stored file's metadata.
type FileObject struct {
	ID           string         `json:"id"`
	Bucket       string         `json:"bucket"`
	Path         string         `json:"path"`
	Version      string         `json:"version,omitempty"`
	Size         int64          `json:"size"`
	MIMEType     string         `json:"mime_type"`
	ETag         string         `json:"etag"`
	Checksum     *string        `json:"checksum,omitempty"`
	Owner        *string        `json:"owner,omitempty"`
	UserMetadata map[string]any `json:"user_metadata,omitempty"`
	CreatedAt    *time.Time     `json:"created_at,omitempty"`
	UpdatedAt    *time.Time     `json:"updated_at,omitempty"`
}

// FileEntry is a listed file or folder.
type FileEntry struct {
	Name   string      `json:"name"`
	Path   string      `json:"path"`
	Folder bool        `json:"folder"`
	Object *FileObject `json:"object,omitempty"`
}

// BucketInfo is a bucket's settings.
type BucketInfo struct {
	ID               string   `json:"id"`
	Public           bool     `json:"public"`
	FileSizeLimit    *int64   `json:"file_size_limit,omitempty"`
	AllowedMIMETypes []string `json:"allowed_mime_types,omitempty"`
	CacheSeconds     *int     `json:"cache_seconds,omitempty"`
}

// Bucket is one bucket.
type Bucket struct {
	c  *Client
	ID string
}

func (b *Bucket) objectPath(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return url.PathEscape(b.ID) + "/" + strings.Join(segs, "/")
}

// UploadOptions are an upload's type and whether it may overwrite.
type UploadOptions struct {
	ContentType string
	Upsert      bool
	Metadata    map[string]any
}

// Upload stores size bytes from r at path (up to 50 MB).
func (b *Bucket) Upload(ctx context.Context, path string, r io.Reader, size int64, o UploadOptions) (*FileObject, error) {
	h := http.Header{}
	if o.ContentType != "" {
		h.Set("Content-Type", o.ContentType)
	}
	if o.Upsert {
		h.Set("x-upsert", "true")
	}
	if o.Metadata != nil {
		m, _ := json.Marshal(o.Metadata)
		h.Set("x-metadata", string(m))
	}
	var f FileObject
	if err := b.c.doJSON(ctx, request{method: http.MethodPost, path: "/storage/v1/object/" + b.objectPath(path), raw: r, size: size, header: h}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Download opens a file (as the caller's policies allow); rng is a byte
// range ("bytes=0-99") or "". The caller closes the body.
func (b *Bucket) Download(ctx context.Context, path, rng string) (io.ReadCloser, http.Header, error) {
	h := http.Header{}
	if rng != "" {
		h.Set("Range", rng)
	}
	res, err := b.c.do(ctx, request{path: "/storage/v1/object/" + b.objectPath(path), header: h})
	if err != nil {
		return nil, nil, err
	}
	return res.Body, res.Header, nil
}

// Info is a file's metadata.
func (b *Bucket) Info(ctx context.Context, path string) (*FileObject, error) {
	var f FileObject
	if err := b.c.doJSON(ctx, request{path: "/storage/v1/object/info/" + b.objectPath(path)}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// List is the files and folders under prefix ("folder/"), a page at a time.
func (b *Bucket) List(ctx context.Context, prefix, cursor string, limit int) ([]FileEntry, string, error) {
	v := url.Values{}
	if prefix != "" {
		v.Set("prefix", prefix)
	}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Items      []FileEntry `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	err := b.c.doJSON(ctx, request{path: "/storage/v1/list/" + url.PathEscape(b.ID), query: v}, &out)
	return out.Items, out.NextCursor, err
}

// Remove deletes files; it returns those deleted.
func (b *Bucket) Remove(ctx context.Context, paths ...string) ([]FileObject, error) {
	var out []FileObject
	err := b.c.doJSON(ctx, request{method: http.MethodDelete, path: "/storage/v1/object/" + url.PathEscape(b.ID), body: map[string]any{"paths": paths}}, &out)
	return out, err
}

// Move renames a file (toBucket "" for the same bucket).
func (b *Bucket) Move(ctx context.Context, from, to, toBucket string) error {
	return b.c.doJSON(ctx, request{path: "/storage/v1/object/move", body: map[string]string{"bucket": b.ID, "from": from, "to": to, "to_bucket": toBucket}}, nil)
}

// Copy copies a file.
func (b *Bucket) Copy(ctx context.Context, from, to, toBucket string) error {
	return b.c.doJSON(ctx, request{path: "/storage/v1/object/copy", body: map[string]string{"bucket": b.ID, "from": from, "to": to, "to_bucket": toBucket}}, nil)
}

// PublicURL is a public bucket's file URL.
func (b *Bucket) PublicURL(path string) string {
	return b.c.URL + "/storage/v1/public/" + b.objectPath(path)
}

// SignedURL is a download URL that works without a session for ttl (an
// hour when 0, 7 days at most).
func (b *Bucket) SignedURL(ctx context.Context, path string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	var out struct {
		SignedURL string `json:"signed_url"`
	}
	err := b.c.doJSON(ctx, request{path: "/storage/v1/object/sign/" + b.objectPath(path), body: map[string]any{"expires_in": int(ttl.Seconds())}}, &out)
	return out.SignedURL, err
}

// SignedUploadURL is a URL a client without a session can upload one file to.
func (b *Bucket) SignedUploadURL(ctx context.Context, path string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := b.c.doJSON(ctx, request{path: "/storage/v1/object/upload/sign/" + b.objectPath(path), body: map[string]any{}}, &out)
	return out.URL, err
}

// ---- Buckets (the secret key) -----------------------------------------------------

// ListBuckets is the project's buckets.
func (s *Storage) ListBuckets(ctx context.Context) ([]BucketInfo, error) {
	var out []BucketInfo
	err := s.c.doJSON(ctx, request{path: "/storage/v1/bucket"}, &out)
	return out, err
}

// CreateBucket makes a bucket.
func (s *Storage) CreateBucket(ctx context.Context, b BucketInfo) (*BucketInfo, error) {
	var out BucketInfo
	if err := s.c.doJSON(ctx, request{path: "/storage/v1/bucket", body: b}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EmptyBucket deletes every file in a bucket.
func (s *Storage) EmptyBucket(ctx context.Context, id string) error {
	return s.c.doJSON(ctx, request{method: http.MethodPost, path: "/storage/v1/bucket/" + url.PathEscape(id) + "/empty"}, nil)
}

// DeleteBucket deletes an empty bucket.
func (s *Storage) DeleteBucket(ctx context.Context, id string) error {
	return s.c.doJSON(ctx, request{method: http.MethodDelete, path: "/storage/v1/bucket/" + url.PathEscape(id)}, nil)
}
