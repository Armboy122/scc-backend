package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinioClient wraps the minio client with bucket management.
type MinioClient struct {
	client     *minio.Client
	bucket     string
	publicURL  string
	presignTTL time.Duration
}

// NewMinioClient creates and configures a MinIO client.
func NewMinioClient(endpoint, accessKey, secretKey, bucket, publicURL string, useSSL bool) (*MinioClient, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio new: %w", err)
	}
	return &MinioClient{
		client:     client,
		bucket:     bucket,
		publicURL:  publicURL,
		presignTTL: 15 * time.Minute,
	}, nil
}

// CreateBucketIfNotExists ensures the configured bucket exists.
func (m *MinioClient) CreateBucketIfNotExists(ctx context.Context) error {
	exists, err := m.client.BucketExists(ctx, m.bucket)
	if err != nil {
		return fmt.Errorf("bucket exists check: %w", err)
	}
	if exists {
		return nil
	}
	return m.client.MakeBucket(ctx, m.bucket, minio.MakeBucketOptions{})
}

// GeneratePresignedPutURL generates a short-lived PUT URL for direct client uploads.
// kind: "install" or "remove"
// Returns (uploadURL, fileURL, error).
func (m *MinioClient) GeneratePresignedPutURL(ctx context.Context, kind, workOrderID, coverID string) (string, string, error) {
	objectKey := fmt.Sprintf("%s/%s/%s/%d.jpg", kind, workOrderID, coverID, time.Now().UnixMilli())

	putURL, err := m.client.PresignedPutObject(ctx, m.bucket, objectKey, m.presignTTL)
	if err != nil {
		return "", "", fmt.Errorf("presign put: %w", err)
	}

	fileURL := fmt.Sprintf("%s/%s/%s", m.publicURL, m.bucket, objectKey)
	return putURL.String(), fileURL, nil
}
