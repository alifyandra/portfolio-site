package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	appconfig "github.com/alifyandra/portfolio-site/backend/internal/config"
)

// ErrObjectNotFound is returned by GetObject when the key does not exist. The
// digest worker treats it as "the task produced no result" (see ADR 0013).
var ErrObjectNotFound = errors.New("storage: object not found")

// Store wraps an S3 client for portfolio asset storage (project images, etc).
type Store struct {
	client *s3.Client
	bucket string
}

// New builds an S3-backed Store. When cfg.S3Endpoint is set (local MinIO), it
// points at that endpoint with path-style addressing; otherwise it uses real S3.
func New(ctx context.Context, cfg *appconfig.Config) (*Store, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = &cfg.S3Endpoint
		}
		o.UsePathStyle = cfg.S3PathStyle
	})

	return &Store{client: client, bucket: cfg.S3Bucket}, nil
}

// PutObject writes body to key with the given content type. Used server-side (the
// digest Fargate task writes its Result here), distinct from the presigned-URL flow
// used for browser uploads.
func (s *Store) PutObject(ctx context.Context, key, contentType string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        bytes.NewReader(body),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}
	return nil
}

// GetObject reads the object at key. Returns ErrObjectNotFound if it does not
// exist, so callers can distinguish "no result yet" from a real S3 error.
func (s *Store) GetObject(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		var nf *s3types.NotFound
		if errors.As(err, &nsk) || errors.As(err, &nf) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("get object %q: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("read object %q: %w", key, err)
	}
	return data, nil
}

// DeleteObject removes the object at key. A missing key is not an error (S3 delete
// is idempotent), so this is safe as best-effort cleanup.
func (s *Store) DeleteObject(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		return fmt.Errorf("delete object %q: %w", key, err)
	}
	return nil
}

// PresignGetURL returns a temporary URL to read an object by key.
func (s *Store) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	ps := s3.NewPresignClient(s.client)
	req, err := ps.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presigning get: %w", err)
	}
	return req.URL, nil
}

// PresignPutURL returns a temporary URL the client can PUT an object to. The
// contentType is bound into the signature, so the caller must send the same
// Content-Type header on the upload or S3 rejects it. Mirrors PresignGetURL.
func (s *Store) PresignPutURL(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	ps := s3.NewPresignClient(s.client)
	req, err := ps.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		ContentType: &contentType,
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presigning put: %w", err)
	}
	return req.URL, nil
}
