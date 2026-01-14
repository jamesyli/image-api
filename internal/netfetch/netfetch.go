package netfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

var (
	ErrTooLarge         = errors.New("content exceeds maximum size")
	ErrDownloadFailed   = errors.New("download failed")
	ErrInvalidURL       = errors.New("url must use http or https")
	ErrTooManyRedirects = errors.New("too many redirects")
)

type Options struct {
	MaxBytes     int64
	MaxRedirects int
}

func Download(ctx context.Context, client *http.Client, rawURL string, opts Options) ([]byte, string, error) {
	// Download with scheme checks, redirect limits, and size guards (Content-Length check + hard read cap).
	if client == nil {
		client = http.DefaultClient
	}
	if !isAllowedScheme(rawURL) {
		return nil, "", ErrInvalidURL
	}

	clientCopy := *client
	redirectLimit := opts.MaxRedirects
	if redirectLimit <= 0 {
		redirectLimit = 3
	}
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= redirectLimit {
			return ErrTooManyRedirects
		}
		if !isAllowedScheme(req.URL.String()) {
			return ErrInvalidURL
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "image-api/1.0 (+https://example.invalid)")
	}

	resp, err := clientCopy.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("%w: status %d", ErrDownloadFailed, resp.StatusCode)
	}

	if opts.MaxBytes > 0 && resp.ContentLength > opts.MaxBytes {
		return nil, "", ErrTooLarge
	}

	var limit io.Reader = resp.Body
	if opts.MaxBytes > 0 {
		limit = io.LimitReader(resp.Body, opts.MaxBytes+1)
	}

	data, err := io.ReadAll(limit)
	if err != nil {
		return nil, "", err
	}
	if opts.MaxBytes > 0 && int64(len(data)) > opts.MaxBytes {
		return nil, "", ErrTooLarge
	}

	return data, resp.Header.Get("Content-Type"), nil
}

func isAllowedScheme(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch parsed.Scheme {
	case "http", "https":
		return true
	default:
		return false
	}
}
