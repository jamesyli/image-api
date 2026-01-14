package uploader

import "context"

type Uploader interface {
	Upload(ctx context.Context, objectName string, data []byte, contentType string) (string, error)
}
