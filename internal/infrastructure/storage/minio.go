package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/smartcover/backend/internal/domain/evidence"
)

const uploadPresignTTL = 15 * time.Minute

// MinioConfig separates trusted server-to-server access from browser-facing
// URL signing. The internal endpoint remains usable when the public Caddy edge
// is unavailable.
type MinioConfig struct {
	InternalEndpoint string
	PublicEndpoint   string
	AccessKey        string
	SecretKey        string
	Bucket           string
	InternalSecure   bool
	PublicSecure     bool
}

// MinioClient uses one client for internal bucket/stat operations and another
// client whose host is embedded in browser-facing signed URLs.
type MinioClient struct {
	internal *minio.Client
	public   *minio.Client
	bucket   string
}

// NewMinioClientWithConfig creates a split-endpoint private object-store
// client.
func NewMinioClientWithConfig(cfg MinioConfig) (*MinioClient, error) {
	if err := validateMinioConfig(cfg); err != nil {
		return nil, err
	}
	creds := credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	internal, err := minio.New(cfg.InternalEndpoint, &minio.Options{
		Creds:  creds,
		Secure: cfg.InternalSecure,
		Region: "us-east-1",
	})
	if err != nil {
		return nil, fmt.Errorf("create internal MinIO client: %w", err)
	}
	public, err := minio.New(cfg.PublicEndpoint, &minio.Options{
		Creds:  creds,
		Secure: cfg.PublicSecure,
		Region: "us-east-1",
	})
	if err != nil {
		return nil, fmt.Errorf("create public MinIO signing client: %w", err)
	}
	return &MinioClient{internal: internal, public: public, bucket: cfg.Bucket}, nil
}

// NewMinioClient preserves the original constructor for local callers while
// deriving a separate public signer endpoint from publicURL. New production
// wiring should use NewMinioClientWithConfig with explicit endpoints.
func NewMinioClient(endpoint, accessKey, secretKey, bucket, publicURL string, useSSL bool) (*MinioClient, error) {
	publicEndpoint := endpoint
	publicSecure := useSSL
	if strings.TrimSpace(publicURL) != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("MINIO_PUBLIC_URL must be an http(s) origin without a path, query, or fragment")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, errors.New("MINIO_PUBLIC_URL must use http or https")
		}
		publicEndpoint = parsed.Host
		publicSecure = parsed.Scheme == "https"
	}
	return NewMinioClientWithConfig(MinioConfig{
		InternalEndpoint: endpoint,
		PublicEndpoint:   publicEndpoint,
		AccessKey:        accessKey,
		SecretKey:        secretKey,
		Bucket:           bucket,
		InternalSecure:   useSSL,
		PublicSecure:     publicSecure,
	})
}

func validateMinioConfig(cfg MinioConfig) error {
	for name, endpoint := range map[string]string{
		"internal": cfg.InternalEndpoint,
		"public":   cfg.PublicEndpoint,
	} {
		if err := validateEndpoint(endpoint); err != nil {
			return fmt.Errorf("invalid %s MinIO endpoint: %w", name, err)
		}
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return errors.New("MinIO access key and secret key are required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" || strings.ContainsAny(cfg.Bucket, "/\\\r\n\t ") {
		return errors.New("invalid MinIO bucket")
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	if endpoint == "" || strings.TrimSpace(endpoint) != endpoint || strings.Contains(endpoint, "://") ||
		strings.ContainsAny(endpoint, "/?#@\r\n\t ") {
		return errors.New("endpoint must be a host[:port] without a scheme, credentials, path, query, or fragment")
	}
	parsed, err := url.Parse("//" + endpoint)
	if err != nil || parsed.Host != endpoint || parsed.Hostname() == "" {
		return errors.New("endpoint must be a valid host[:port]")
	}
	return nil
}

// CreateBucketIfNotExists ensures the private bucket exists through the
// internal Docker-network endpoint.
func (m *MinioClient) CreateBucketIfNotExists(ctx context.Context) error {
	exists, err := m.internal.BucketExists(ctx, m.bucket)
	if err != nil {
		return fmt.Errorf("bucket exists check: %w", err)
	}
	if !exists {
		if err := m.internal.MakeBucket(ctx, m.bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("create private evidence bucket: %w", err)
		}
	}
	// An empty policy removes any legacy anonymous-download policy. Treat an
	// already policy-free bucket as success, but fail startup on any other error.
	if err := m.internal.SetBucketPolicy(ctx, m.bucket, ""); err != nil {
		if minio.ToErrorResponse(err).Code != "NoSuchBucketPolicy" {
			return fmt.Errorf("enforce private evidence bucket policy: %w", err)
		}
	}
	return nil
}

// Ready verifies the evidence bucket through the private internal endpoint and
// confirms that no bucket policy has re-enabled anonymous access. It never
// contacts the public signer/Caddy hostname.
func (m *MinioClient) Ready(ctx context.Context) error {
	if m == nil || m.internal == nil || strings.TrimSpace(m.bucket) == "" {
		return errors.New("private evidence storage is not configured")
	}
	exists, err := m.internal.BucketExists(ctx, m.bucket)
	if err != nil {
		return fmt.Errorf("check private evidence bucket: %w", err)
	}
	if !exists {
		return errors.New("private evidence bucket does not exist")
	}
	policy, err := m.internal.GetBucketPolicy(ctx, m.bucket)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchBucketPolicy" {
			return nil
		}
		return fmt.Errorf("check private evidence bucket policy: %w", err)
	}
	if strings.TrimSpace(policy) != "" {
		return errors.New("private evidence bucket unexpectedly has an access policy")
	}
	return nil
}

// PresignPut returns a relation-scoped, short-lived PUT URL. Content-Type and
// If-None-Match are signed so the upload is an immutable image write; actual
// size and metadata are verified through the internal endpoint before attach.
func (m *MinioClient) PresignPut(
	ctx context.Context,
	kind evidence.Kind,
	workOrderID, coverID, contentType string,
	size int64,
) (*evidence.Upload, error) {
	normalizedType, err := evidence.ValidateImageMetadata(contentType, size)
	if err != nil {
		return nil, err
	}
	objectKey, err := evidence.NewObjectKey(kind, workOrderID, coverID, normalizedType)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", normalizedType)
	headers.Set("If-None-Match", "*")
	putURL, err := m.public.PresignHeader(
		ctx,
		http.MethodPut,
		m.bucket,
		objectKey,
		uploadPresignTTL,
		nil,
		headers,
	)
	if err != nil {
		return nil, fmt.Errorf("presign evidence PUT: %w", err)
	}
	return &evidence.Upload{UploadURL: putURL.String(), ObjectKey: objectKey}, nil
}

// Stat verifies object metadata using the internal endpoint only.
func (m *MinioClient) Stat(ctx context.Context, objectKey string) (*evidence.ObjectMetadata, error) {
	if objectKey == "" || path.Clean(objectKey) != objectKey || strings.HasPrefix(objectKey, "/") {
		return nil, errors.New("invalid evidence object key")
	}
	info, err := m.internal.StatObject(ctx, m.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		return nil, mapObjectError("stat evidence object", err)
	}
	getOptions := minio.GetObjectOptions{}
	if err := getOptions.SetRange(0, 511); err != nil {
		return nil, fmt.Errorf("configure evidence header range: %w", err)
	}
	object, err := m.internal.GetObject(ctx, m.bucket, objectKey, getOptions)
	if err != nil {
		return nil, mapObjectError("open evidence object", err)
	}
	prefix, readErr := io.ReadAll(io.LimitReader(object, 512))
	closeErr := object.Close()
	if readErr != nil {
		return nil, mapObjectError("read evidence object header", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close evidence object reader: %w", closeErr)
	}
	return &evidence.ObjectMetadata{
		ContentType:         info.ContentType,
		DetectedContentType: http.DetectContentType(prefix),
		Size:                info.Size,
	}, nil
}

func mapObjectError(operation string, err error) error {
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey", "NoSuchObject", "NotFound", "NoSuchBucket":
		return evidence.ErrObjectNotFound
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

// PresignGet returns a short-lived browser URL for an already-authorized read.
func (m *MinioClient) PresignGet(ctx context.Context, objectKey string) (string, error) {
	if objectKey == "" || path.Clean(objectKey) != objectKey || strings.HasPrefix(objectKey, "/") {
		return "", errors.New("invalid evidence object key")
	}
	readURL, err := m.public.PresignedGetObject(ctx, m.bucket, objectKey, evidence.SignedReadTTL, nil)
	if err != nil {
		return "", fmt.Errorf("presign evidence GET: %w", err)
	}
	return readURL.String(), nil
}
