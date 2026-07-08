package storage

import (
	"context"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	appconfig "github.com/alifyandra/portfolio-site/backend/internal/config"
)

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
