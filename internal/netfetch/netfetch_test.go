package netfetch

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadTooLarge(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	_, _, err := Download(context.Background(), server.Client(), server.URL, Options{MaxBytes: 512})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if err != ErrTooLarge {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestDownloadRejectsScheme(t *testing.T) {
	_, _, err := Download(context.Background(), http.DefaultClient, "file:///tmp/nope", Options{})
	if err != ErrInvalidURL {
		t.Fatalf("expected ErrInvalidURL, got %v", err)
	}
}
