package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/webp"
	"github.com/jackc/pgx/v5"
	xdraw "golang.org/x/image/draw"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/files"
	"github.com/israel-duff/pgdock/internal/storage"
)

// Image transforms (V4 §5.5): resize, crop and convert on request, in pure
// Go (WebP and AVIF through their WebAssembly builds, so pgdock-edge stays
// one static binary). Results are kept in the object store next to the
// source version, so a repeated request is served without rendering.

const (
	maxSourceBytes  = 25 << 20
	maxSourcePixels = 25_000_000
	maxSide         = 2500
)

func checkTransform(t *files.Transform) *apiErr {
	switch {
	case t.Width < 0 || t.Width > maxSide || t.Height < 0 || t.Height > maxSide:
		return refuse(http.StatusBadRequest, "invalid_transform", fmt.Sprintf("width and height are 1–%d pixels", maxSide))
	case t.Resize != "" && t.Resize != "cover" && t.Resize != "contain" && t.Resize != "fill":
		return refuse(http.StatusBadRequest, "invalid_transform", "resize is cover, contain or fill")
	case t.Format != "" && t.Format != "origin" && t.Format != "webp" && t.Format != "avif" && t.Format != "jpeg" && t.Format != "png":
		return refuse(http.StatusBadRequest, "invalid_transform", "format is origin, webp, avif, jpeg or png")
	case t.Quality != 0 && (t.Quality < 20 || t.Quality > 100):
		return refuse(http.StatusBadRequest, "invalid_transform", "quality is 20–100")
	}
	return nil
}

// transformKey is the transform's part of its cache key.
func transformKey(t files.Transform) string {
	q := t.Quality
	if q == 0 {
		q = 80
	}
	r := t.Resize
	if r == "" {
		r = "cover"
	}
	return fmt.Sprintf("w%d-h%d-%s-%s-q%d", t.Width, t.Height, r, t.Format, q)
}

func transformFromQuery(c *call) (files.Transform, *apiErr) {
	q := c.r.URL.Query()
	atoi := func(k string) int {
		n, _ := strconv.Atoi(q.Get(k))
		return n
	}
	t := files.Transform{Width: atoi("width"), Height: atoi("height"), Resize: q.Get("resize"), Format: q.Get("format"), Quality: atoi("quality")}
	if t.Resize == "" {
		t.Resize = q.Get("fit")
	}
	if (q.Get("width") != "" && t.Width <= 0) || (q.Get("height") != "" && t.Height <= 0) {
		return t, refuse(http.StatusBadRequest, "invalid_transform", fmt.Sprintf("width and height are 1–%d pixels", maxSide))
	}
	return t, checkTransform(&t)
}

type renderMode int

const (
	renderAuthenticated renderMode = iota
	renderPublic
	renderSigned
)

var renderable = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/avif": true}

// render serves a transformed image: as the caller's policies allow, from a
// public bucket, or for a signed URL (whose transform is in its token).
func (e *Edge) render(c *call, req *Request, bucket, path string, mode renderMode) {
	sc, cl, ok := e.store(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.r.Context(), 2*time.Minute)
	defer cancel()
	var t files.Transform
	var o *Object
	cache, public := 0, false
	switch mode {
	case renderSigned:
		tok, ok := files.Verify(sc.SigningSecret, c.r.URL.Query().Get("token"), files.TokenGet, c.p.cfg.Ref, bucket, path, time.Now())
		if !ok || tok.Transform == nil {
			c.fail(http.StatusBadRequest, "invalid_signature", "the URL's token is invalid or has expired")
			return
		}
		t = *tok.Transform
		var err error
		if o, err = e.edgeObject(ctx, c.p, bucket, path); err != nil {
			e.storageError(c, err)
			return
		}
		cache = int(time.Until(time.Unix(tok.Exp, 0)).Seconds())
	case renderPublic:
		var a *apiErr
		if t, a = transformFromQuery(c); a != nil {
			a.send(c)
			return
		}
		b, err := e.bucket(ctx, c.p, bucket)
		if errors.Is(err, errNoBucket) || (err == nil && !b.Public) {
			c.fail(http.StatusNotFound, "object_not_found", "no such object")
			return
		}
		if err != nil {
			e.storageError(c, err)
			return
		}
		if o, err = e.edgeObject(ctx, c.p, bucket, path); err != nil {
			e.storageError(c, err)
			return
		}
		cache, public = b.CacheSeconds, true
	default:
		var a *apiErr
		if t, a = transformFromQuery(c); a != nil {
			a.send(c)
			return
		}
		if err := e.asRole(ctx, c, *req, func(tx pgx.Tx) error {
			var err error
			o, err = getObject(ctx, tx, bucket, path)
			return err
		}); err != nil {
			e.storageError(c, err)
			return
		}
		if o != nil {
			b, err := e.bucket(ctx, c.p, bucket)
			if err != nil {
				e.storageError(c, err)
				return
			}
			cache = b.CacheSeconds
		}
	}
	if o == nil {
		c.fail(http.StatusNotFound, "object_not_found", "no such object")
		return
	}
	if !renderable[o.MIMEType] {
		c.fail(http.StatusBadRequest, "not_an_image", "only PNG, JPEG, GIF, WebP and AVIF images can be transformed")
		return
	}
	if o.Size > maxSourceBytes {
		c.fail(http.StatusBadRequest, "image_too_large", "images larger than 25 MB can't be transformed")
		return
	}
	format := t.Format
	if format == "" || format == "origin" {
		format = strings.TrimPrefix(o.MIMEType, "image/")
		if format == "gif" {
			format = "png" // a still of the first frame
		}
	}
	sum := sha256.Sum256([]byte(transformKey(t)))
	key := files.TransformsPrefix(sc.Prefix, o.version) + "/" + hex.EncodeToString(sum[:16]) + "." + format
	ctype := "image/" + format
	if got, err := cl.Get(ctx, key, ""); err == nil {
		defer got.Body.Close()
		e.serveRender(c, sc, got.Body, got.Length, ctype, key, o, cache, public, "HIT")
		return
	} else if !errors.Is(err, storage.ErrNotFound) {
		e.storageError(c, storeErr(err))
		return
	}
	if sc.TransformsBlocked {
		c.w.Header().Set("Retry-After", "3600")
		c.fail(http.StatusTooManyRequests, "transform_limit", "the organisation's image transforms for this month are used up")
		return
	}
	if c.p.cfg.SpendCapped {
		c.w.Header().Set("Retry-After", "3600")
		c.fail(http.StatusTooManyRequests, "spend_cap_reached", "the organisation has reached its spend cap; new image transforms are paused (cached ones still serve)")
		return
	}
	select {
	case e.renderSlots <- struct{}{}:
		defer func() { <-e.renderSlots }()
	case <-ctx.Done():
		c.fail(http.StatusServiceUnavailable, "busy", "too many images are being transformed; retry shortly")
		return
	}
	out, err := e.renderImage(ctx, cl, objectKey(sc, o.version), t, format)
	if err != nil {
		e.storageError(c, err)
		return
	}
	if _, err := cl.Put(ctx, key, bytes.NewReader(out), int64(len(out)), ctype); err != nil {
		e.cfg.Log.Warn("cache a transformed image", "project", c.p.cfg.Ref, "err", err)
	}
	c.transforms++
	e.serveRender(c, sc, bytes.NewReader(out), int64(len(out)), ctype, key, o, cache, public, "MISS")
}

func (e *Edge) serveRender(c *call, sc *edgeapi.StorageConfig, body io.Reader, n int64, ctype, key string, o *Object, cache int, public bool, hit string) {
	if sc.EgressBlocked {
		c.w.Header().Set("Retry-After", "3600")
		c.fail(http.StatusTooManyRequests, "storage_egress_limit", "the organisation's file download allowance for this month is used up")
		return
	}
	h := c.w.Header()
	sum := sha256.Sum256([]byte(key))
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h.Set("ETag", etag)
	h.Set("Last-Modified", o.UpdatedAt.UTC().Format(http.TimeFormat))
	h.Set("X-Cache", hit)
	h.Set("X-Content-Type-Options", "nosniff")
	if public {
		h.Set("Cache-Control", "public, max-age="+strconv.Itoa(max(cache, 0)))
	} else {
		h.Set("Cache-Control", "private, max-age="+strconv.Itoa(max(cache, 0)))
	}
	if inm := c.r.Header.Get("If-None-Match"); inm == etag {
		c.w.WriteHeader(http.StatusNotModified)
		return
	}
	c.storage = true
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	c.w.WriteHeader(http.StatusOK)
	if c.r.Method != http.MethodHead {
		_, _ = io.Copy(c.w, body)
	}
}

// renderImage decodes the source, resizes it and encodes it as format.
func (e *Edge) renderImage(ctx context.Context, cl *storage.Client, src string, t files.Transform, format string) ([]byte, error) {
	obj, err := cl.Get(ctx, src, "")
	if err != nil {
		return nil, storeErr(err)
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, maxSourceBytes+1))
	_ = obj.Body.Close()
	if err != nil {
		return nil, storeErr(err)
	}
	cfg, kind, err := decodeConfig(data)
	if err != nil {
		return nil, refuse(http.StatusBadRequest, "invalid_image", "the image can't be read: "+err.Error())
	}
	if cfg.Width*cfg.Height > maxSourcePixels {
		return nil, refuse(http.StatusBadRequest, "image_too_large", "images over 25 megapixels can't be transformed")
	}
	img, err := decodeImage(data, kind)
	if err != nil {
		return nil, refuse(http.StatusBadRequest, "invalid_image", "the image can't be read: "+err.Error())
	}
	img = resize(img, t)
	q := t.Quality
	if q == 0 {
		q = 80
	}
	var buf bytes.Buffer
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: q})
	case "png":
		err = png.Encode(&buf, img)
	case "webp":
		err = webp.Encode(&buf, img, webp.Options{Quality: q, Method: 2})
	case "avif":
		err = avif.Encode(&buf, img, avif.Options{Quality: q, QualityAlpha: q, Speed: 8})
	default:
		return nil, refuse(http.StatusBadRequest, "invalid_transform", "format is origin, webp, avif, jpeg or png")
	}
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", format, err)
	}
	return buf.Bytes(), nil
}

func decodeConfig(data []byte) (image.Config, string, error) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG")):
		c, err := png.DecodeConfig(bytes.NewReader(data))
		return c, "png", err
	case bytes.HasPrefix(data, []byte("\xff\xd8")):
		c, err := jpeg.DecodeConfig(bytes.NewReader(data))
		return c, "jpeg", err
	case bytes.HasPrefix(data, []byte("GIF8")):
		c, err := gif.DecodeConfig(bytes.NewReader(data))
		return c, "gif", err
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		c, err := webp.DecodeConfig(bytes.NewReader(data))
		return c, "webp", err
	case len(data) > 12 && string(data[4:8]) == "ftyp":
		c, err := avif.DecodeConfig(bytes.NewReader(data))
		return c, "avif", err
	}
	return image.Config{}, "", errors.New("not a PNG, JPEG, GIF, WebP or AVIF image")
}

func decodeImage(data []byte, kind string) (image.Image, error) {
	r := bytes.NewReader(data)
	switch kind {
	case "png":
		return png.Decode(r)
	case "jpeg":
		return jpeg.Decode(r)
	case "gif":
		return gif.Decode(r)
	case "webp":
		return webp.Decode(r)
	case "avif":
		return avif.Decode(r)
	}
	return nil, errors.New("unknown image format")
}

// resize scales img per t, never up: cover fills the box and crops the
// middle, contain fits inside it, fill stretches to it.
func resize(img image.Image, t files.Transform) image.Image {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if t.Width == 0 && t.Height == 0 || sw == 0 || sh == 0 {
		return img
	}
	w, h := t.Width, t.Height
	if w == 0 {
		w = int(float64(sw) * float64(h) / float64(sh))
	}
	if h == 0 {
		h = int(float64(sh) * float64(w) / float64(sw))
	}
	w, h = max(1, w), max(1, h)
	mode := t.Resize
	if t.Width == 0 || t.Height == 0 {
		mode = "fill" // one side given: the other keeps the aspect
	}
	var dw, dh int
	crop := b
	switch mode {
	case "fill":
		dw, dh = min(w, sw), min(h, sh)
	case "contain":
		s := min(float64(w)/float64(sw), float64(h)/float64(sh), 1)
		dw, dh = max(1, int(float64(sw)*s)), max(1, int(float64(sh)*s))
	default: // cover
		s := max(float64(w)/float64(sw), float64(h)/float64(sh))
		if s > 1 {
			s = 1
		}
		// The source rectangle that scales to the box.
		cw, ch := min(sw, int(float64(w)/s)), min(sh, int(float64(h)/s))
		x0, y0 := b.Min.X+(sw-cw)/2, b.Min.Y+(sh-ch)/2
		crop = image.Rect(x0, y0, x0+cw, y0+ch)
		dw, dh = max(1, int(float64(cw)*s)), max(1, int(float64(ch)*s))
	}
	if dw == sw && dh == sh && crop == b {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, crop, xdraw.Src, nil)
	return dst
}

// flatten puts an image with transparency on white, for JPEG.
func flatten(img image.Image) image.Image {
	if _, ok := img.(*image.YCbCr); ok {
		return img
	}
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Over)
	return dst
}
