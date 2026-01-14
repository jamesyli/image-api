package localstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Uploader struct {
	Dir     string
	BaseURL string
}

func NewUploader(dir string, baseURL string) *Uploader {
	return &Uploader{Dir: dir, BaseURL: strings.TrimRight(baseURL, "/")}
}

func (u *Uploader) Upload(ctx context.Context, objectName string, data []byte, contentType string) (string, error) {
	_ = ctx
	_ = contentType

	if u.Dir == "" {
		return "", errors.New("local storage dir is required")
	}
	if objectName == "" {
		return "", errors.New("object name is required")
	}

	clean, err := sanitizeObjectName(objectName)
	if err != nil {
		return "", err
	}

	fullPath := filepath.Join(u.Dir, filepath.FromSlash(clean))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		return "", err
	}

	if u.BaseURL == "" {
		return "", nil
	}
	escaped, err := escapePath(clean)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", u.BaseURL, escaped), nil
}

func sanitizeObjectName(objectName string) (string, error) {
	clean := path.Clean("/" + objectName)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", errors.New("invalid object name")
	}
	if strings.Contains(clean, "..") {
		return "", errors.New("invalid object name")
	}
	return clean, nil
}

func escapePath(p string) (string, error) {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/"), nil
}
