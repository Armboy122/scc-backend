package evidence

import (
	"strings"
	"testing"
)

func TestObjectKeyIsOpaqueAndRelationScoped(t *testing.T) {
	key, err := NewObjectKey(KindInstall, "wo-1", "cover-1", "image/jpeg")
	if err != nil {
		t.Fatalf("NewObjectKey: %v", err)
	}
	if !strings.HasPrefix(key, "evidence/v1/install/wo-1/cover-1/") {
		t.Fatalf("unexpected key prefix: %q", key)
	}
	if err := ValidateObjectKey(key, KindInstall, "wo-1", "cover-1"); err != nil {
		t.Fatalf("ValidateObjectKey: %v", err)
	}

	for _, test := range []struct {
		name    string
		kind    Kind
		woID    string
		coverID string
		key     string
	}{
		{name: "wrong work order", kind: KindInstall, woID: "wo-2", coverID: "cover-1", key: key},
		{name: "wrong cover", kind: KindInstall, woID: "wo-1", coverID: "cover-2", key: key},
		{name: "wrong kind", kind: KindRemove, woID: "wo-1", coverID: "cover-1", key: key},
		{name: "arbitrary URL", kind: KindInstall, woID: "wo-1", coverID: "cover-1", key: "https://storage.example/scc/photo.jpg"},
		{name: "traversal", kind: KindInstall, woID: "wo-1", coverID: "cover-1", key: "evidence/v1/install/wo-1/cover-1/../photo.jpg"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateObjectKey(test.key, test.kind, test.woID, test.coverID); err == nil {
				t.Fatalf("expected key %q to be rejected", test.key)
			}
		})
	}
}

func TestValidateImageMetadata(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
		size        int64
		wantType    string
		wantErr     bool
	}{
		{name: "jpeg", contentType: "image/jpeg", size: 1, wantType: "image/jpeg"},
		{name: "canonicalizes parameters", contentType: "IMAGE/PNG; charset=binary", size: 100, wantType: "image/png"},
		{name: "webp at limit", contentType: "image/webp", size: MaxImageBytes, wantType: "image/webp"},
		{name: "zero byte", contentType: "image/jpeg", size: 0, wantErr: true},
		{name: "too large", contentType: "image/jpeg", size: MaxImageBytes + 1, wantErr: true},
		{name: "non image", contentType: "text/html", size: 10, wantErr: true},
		{name: "svg is active content", contentType: "image/svg+xml", size: 10, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidateImageMetadata(test.contentType, test.size)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil || got != test.wantType {
				t.Fatalf("got (%q, %v), want (%q, nil)", got, err, test.wantType)
			}
		})
	}
}

func TestValidateStoredImageMetadataRejectsSpoofedContentType(t *testing.T) {
	_, err := ValidateStoredImageMetadata(&ObjectMetadata{
		ContentType:         "image/jpeg",
		DetectedContentType: "text/html; charset=utf-8",
		Size:                64,
	})
	if err == nil {
		t.Fatal("expected HTML bytes with image/jpeg metadata to be rejected")
	}

	got, err := ValidateStoredImageMetadata(&ObjectMetadata{
		ContentType:         "image/jpeg",
		DetectedContentType: "image/jpeg",
		Size:                64,
	})
	if err != nil || got != "image/jpeg" {
		t.Fatalf("got (%q, %v), want (image/jpeg, nil)", got, err)
	}
}

func TestValidateIdentifier(t *testing.T) {
	for _, value := range []string{"", "../wo", "wo/1", " wo-1", "wo.1", strings.Repeat("a", 129)} {
		if err := ValidateIdentifier(value); err == nil {
			t.Fatalf("expected identifier %q to be rejected", value)
		}
	}
	for _, value := range []string{"wo-1", "550e8400-e29b-41d4-a716-446655440000", "cover_1"} {
		if err := ValidateIdentifier(value); err != nil {
			t.Fatalf("expected identifier %q to be valid: %v", value, err)
		}
	}
}
