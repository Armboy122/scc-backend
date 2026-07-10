package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/smartcover/backend/internal/domain/evidence"
)

func TestMinioPrivateEvidenceRoundTripIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("SCC_TEST_MINIO_ENDPOINT"))
	accessKey := os.Getenv("SCC_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("SCC_TEST_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("set SCC_TEST_MINIO_ENDPOINT/ACCESS_KEY/SECRET_KEY to run MinIO integration")
	}
	bucket := fmt.Sprintf("scc-test-%d", time.Now().UnixNano())
	client, err := NewMinioClientWithConfig(MinioConfig{
		InternalEndpoint: endpoint, PublicEndpoint: endpoint,
		AccessKey: accessKey, SecretKey: secretKey, Bucket: bucket,
	})
	if err != nil {
		t.Fatalf("create MinIO client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.CreateBucketIfNotExists(ctx); err != nil {
		t.Fatalf("create private bucket: %v", err)
	}
	var objectKey string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if objectKey != "" {
			_ = client.internal.RemoveObject(cleanupCtx, bucket, objectKey, minio.RemoveObjectOptions{})
		}
		_ = client.internal.RemoveBucket(cleanupCtx, bucket)
	})
	if err := client.Ready(ctx); err != nil {
		t.Fatalf("private bucket readiness: %v", err)
	}

	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0xff, 0xd9}
	upload, err := client.PresignPut(
		ctx, evidence.KindInstall, "wo-integration", "cover-integration", "image/jpeg", int64(len(jpeg)),
	)
	if err != nil {
		t.Fatalf("presign PUT: %v", err)
	}
	objectKey = upload.ObjectKey
	put := func() *http.Response {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPut, upload.UploadURL, bytes.NewReader(jpeg))
		if requestErr != nil {
			t.Fatalf("create PUT request: %v", requestErr)
		}
		req.Header.Set("Content-Type", "image/jpeg")
		req.Header.Set("If-None-Match", "*")
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatalf("execute PUT: %v", requestErr)
		}
		return response
	}
	firstPut := put()
	_, _ = io.Copy(io.Discard, firstPut.Body)
	_ = firstPut.Body.Close()
	if firstPut.StatusCode < 200 || firstPut.StatusCode >= 300 {
		t.Fatalf("first immutable PUT status = %d", firstPut.StatusCode)
	}
	secondPut := put()
	_, _ = io.Copy(io.Discard, secondPut.Body)
	_ = secondPut.Body.Close()
	if secondPut.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("second immutable PUT status = %d, want 412", secondPut.StatusCode)
	}

	metadata, err := client.Stat(ctx, objectKey)
	if err != nil {
		t.Fatalf("verify evidence metadata: %v", err)
	}
	if metadata.ContentType != "image/jpeg" || metadata.DetectedContentType != "image/jpeg" || metadata.Size != int64(len(jpeg)) {
		t.Fatalf("metadata = %#v", metadata)
	}

	plainURL := "http://" + endpoint + "/" + bucket + "/" + objectKey
	plainRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, plainURL, nil)
	if err != nil {
		t.Fatalf("create anonymous GET: %v", err)
	}
	plainResponse, err := http.DefaultClient.Do(plainRequest)
	if err != nil {
		t.Fatalf("anonymous GET: %v", err)
	}
	_, _ = io.Copy(io.Discard, plainResponse.Body)
	_ = plainResponse.Body.Close()
	if plainResponse.StatusCode < 400 {
		t.Fatalf("anonymous GET unexpectedly succeeded with %d", plainResponse.StatusCode)
	}

	readURL, err := client.PresignGet(ctx, objectKey)
	if err != nil {
		t.Fatalf("presign GET: %v", err)
	}
	readRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, readURL, nil)
	if err != nil {
		t.Fatalf("create signed GET: %v", err)
	}
	readResponse, err := http.DefaultClient.Do(readRequest)
	if err != nil {
		t.Fatalf("signed GET: %v", err)
	}
	body, readErr := io.ReadAll(readResponse.Body)
	_ = readResponse.Body.Close()
	if readErr != nil {
		t.Fatalf("read signed GET: %v", readErr)
	}
	if readResponse.StatusCode != http.StatusOK || !bytes.Equal(body, jpeg) {
		t.Fatalf("signed GET status/body mismatch: status=%d body=%x", readResponse.StatusCode, body)
	}
}
