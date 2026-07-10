package storage

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/smartcover/backend/internal/domain/evidence"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestMinioClientUsesPublicSignerAndInternalStatEndpoint(t *testing.T) {
	client, err := NewMinioClientWithConfig(MinioConfig{
		InternalEndpoint: "minio-internal:9000",
		PublicEndpoint:   "storage.example.test",
		AccessKey:        "access",
		SecretKey:        "secret",
		Bucket:           "scc",
	})
	if err != nil {
		t.Fatalf("NewMinioClientWithConfig: %v", err)
	}
	internalRequests := 0
	client.internal, err = minio.New("minio-internal:9000", &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Region: "us-east-1",
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			internalRequests++
			if req.URL.Host != "minio-internal:9000" ||
				!strings.Contains(req.URL.Path, "/scc/evidence/v1/install/wo-1/cover-1/") {
				t.Errorf("unexpected internal request: %s %s", req.Method, req.URL.String())
			}
			body := ""
			if req.Method == http.MethodGet {
				if req.Header.Get("Range") != "bytes=0-511" {
					t.Errorf("GET Range = %q, want bytes=0-511", req.Header.Get("Range"))
				}
				body = "\xff\xd8\xff\xe0" + strings.Repeat("\x00", 119)
			} else if req.Method != http.MethodHead {
				t.Errorf("unexpected internal method: %s", req.Method)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header: http.Header{
					"Content-Type":   []string{"image/jpeg"},
					"Content-Length": []string{"123"},
					"ETag":           []string{`"abc"`},
					"Last-Modified":  []string{"Fri, 10 Jul 2026 00:00:00 GMT"},
				},
				Body:    io.NopCloser(strings.NewReader(body)),
				Request: req,
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create internal fake client: %v", err)
	}

	upload, err := client.PresignPut(context.Background(), evidence.KindInstall, "wo-1", "cover-1", "image/jpeg", 123)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	parsedUpload, err := url.Parse(upload.UploadURL)
	if err != nil {
		t.Fatalf("parse upload URL: %v", err)
	}
	if parsedUpload.Host != "storage.example.test" {
		t.Fatalf("upload host = %q, want public signer", parsedUpload.Host)
	}
	if err := evidence.ValidateObjectKey(upload.ObjectKey, evidence.KindInstall, "wo-1", "cover-1"); err != nil {
		t.Fatalf("generated object key: %v", err)
	}
	if signedHeaders := parsedUpload.Query().Get("X-Amz-SignedHeaders"); !strings.Contains(signedHeaders, "content-type") || !strings.Contains(signedHeaders, "if-none-match") {
		t.Fatalf("expected immutable image headers to be signed, got %q", signedHeaders)
	}

	metadata, err := client.Stat(context.Background(), upload.ObjectKey)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if metadata.ContentType != "image/jpeg" || metadata.DetectedContentType != "image/jpeg" || metadata.Size != 123 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if internalRequests != 2 {
		t.Fatalf("internal requests = %d, want HEAD plus ranged GET", internalRequests)
	}

	readURL, err := client.PresignGet(context.Background(), upload.ObjectKey)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	parsedRead, err := url.Parse(readURL)
	if err != nil {
		t.Fatalf("parse read URL: %v", err)
	}
	if parsedRead.Host != "storage.example.test" {
		t.Fatalf("read host = %q, want public signer", parsedRead.Host)
	}
}

func TestNewMinioClientWithConfigRejectsUnsafeEndpoints(t *testing.T) {
	for _, endpoint := range []string{"", "https://storage.example", "user@storage.example", "storage.example/path", " storage.example"} {
		_, err := NewMinioClientWithConfig(MinioConfig{
			InternalEndpoint: endpoint,
			PublicEndpoint:   "storage.example",
			AccessKey:        "access",
			SecretKey:        "secret",
			Bucket:           "scc",
		})
		if err == nil {
			t.Fatalf("expected endpoint %q to be rejected", endpoint)
		}
	}
}

func TestCreateBucketIfNotExistsRemovesLegacyAnonymousPolicy(t *testing.T) {
	policyDeleteSeen := false
	internal, err := minio.New("minio-internal:9000", &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Region: "us-east-1",
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			status := http.StatusOK
			if req.Method == http.MethodDelete && req.URL.Query().Has("policy") {
				policyDeleteSeen = true
				status = http.StatusNoContent
			} else if req.Method != http.MethodHead {
				t.Errorf("unexpected request while ensuring private bucket: %s %s", req.Method, req.URL.String())
			}
			return &http.Response{
				StatusCode: status,
				Status:     http.StatusText(status),
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create internal fake client: %v", err)
	}
	client := &MinioClient{internal: internal, bucket: "scc"}

	if err := client.CreateBucketIfNotExists(context.Background()); err != nil {
		t.Fatalf("CreateBucketIfNotExists: %v", err)
	}
	if !policyDeleteSeen {
		t.Fatal("expected legacy bucket policy to be removed")
	}
}

func TestMinioReadyUsesInternalEndpointAndRequiresPolicyFreeBucket(t *testing.T) {
	policyPresent := false
	requestCount := 0
	internal, err := minio.New("minio-internal:9000", &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Region: "us-east-1",
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestCount++
			if req.URL.Host != "minio-internal:9000" {
				t.Errorf("readiness used non-internal host %q", req.URL.Host)
			}
			status := http.StatusOK
			body := ""
			header := make(http.Header)
			if req.Method == http.MethodGet && req.URL.Query().Has("policy") {
				if policyPresent {
					body = `{"Version":"2012-10-17","Statement":[]}`
				} else {
					status = http.StatusNotFound
					body = `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchBucketPolicy</Code><Message>none</Message><BucketName>scc</BucketName><RequestId>test</RequestId><HostId>test</HostId></Error>`
					header.Set("Content-Type", "application/xml")
				}
			} else if req.Method != http.MethodHead {
				t.Errorf("unexpected readiness request: %s %s", req.Method, req.URL.String())
			}
			return &http.Response{
				StatusCode: status,
				Status:     http.StatusText(status),
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("create internal fake client: %v", err)
	}
	client := &MinioClient{internal: internal, bucket: "scc"}
	if err := client.Ready(context.Background()); err != nil {
		t.Fatalf("policy-free bucket should be ready: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("readiness requests = %d, want bucket and policy checks", requestCount)
	}

	policyPresent = true
	if err := client.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "access policy") {
		t.Fatalf("Ready() = %v, want policy rejection", err)
	}
}
