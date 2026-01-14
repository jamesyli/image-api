package gcs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/storage"
)

var ErrBucketRequired = errors.New("bucket is required")

type Uploader struct {
	Client                *storage.Client
	Bucket                string
	MakePublic            bool
	AllowPublicACLFailure bool
}

func NewUploader(client *storage.Client, bucket string, makePublic bool, allowPublicACLFailure bool) *Uploader {
	return &Uploader{
		Client:                client,
		Bucket:                bucket,
		MakePublic:            makePublic,
		AllowPublicACLFailure: allowPublicACLFailure,
	}
}

func (u *Uploader) Upload(ctx context.Context, objectName string, data []byte, contentType string) (string, error) {
	// Write the object and optionally make it public, returning the public URL.
	if u.Client == nil {
		return "", errors.New("storage client is required")
	}
	if u.Bucket == "" {
		return "", ErrBucketRequired
	}
	if objectName == "" {
		return "", errors.New("object name is required")
	}

	obj := u.Client.Bucket(u.Bucket).Object(objectName)
	writer := obj.NewWriter(ctx)
	if contentType != "" {
		writer.ContentType = contentType
	}

	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	if u.MakePublic {
		if err := obj.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err != nil {
			if u.AllowPublicACLFailure && strings.Contains(err.Error(), "uniform bucket-level access") {
				return publicURL(u.Bucket, objectName), nil
			}
			return "", err
		}
	}

	return publicURL(u.Bucket, objectName), nil
}

func publicURL(bucket, objectName string) string {
	return fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucket, objectName)
}
