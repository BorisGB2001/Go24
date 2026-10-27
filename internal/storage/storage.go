// Package storage talks S3 to `rclone serve s3`, so the actual backend can be any self-hosted
// rclone remote (local disk, SFTP file server, Garage/SeaweedFS, Nextcloud WebDAV) without code changes.
package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	client *minio.Client
	bucket string
}

// New connects to the S3 endpoint and makes sure the bucket (a top-level folder on the rclone remote) exists.
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, err
	}
	s := &Store{client: client, bucket: bucket}

	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		if lastErr = s.ensureBucket(ctx); lastErr == nil {
			return s, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("storage not reachable at %s: %w", endpoint, lastErr)
}

// Ping asks the S3 endpoint whether the bucket is there («Состояние системы»).
func (s *Store) Ping(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err == nil && !ok {
		err = fmt.Errorf("bucket %s not found", s.bucket)
	}
	return err
}

func (s *Store) ensureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{})
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
}

// Walk calls fn for every object of the bucket (its key, size and time) until fn fails.
func (s *Store) Walk(ctx context.Context, fn func(key string, size int64, mod time.Time) error) error {
	for o := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if o.Err != nil {
			return o.Err
		}
		if err := fn(o.Key, o.Size, o.LastModified); err != nil {
			return err
		}
	}
	return nil
}
