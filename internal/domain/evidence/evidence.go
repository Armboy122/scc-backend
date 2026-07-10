// Package evidence defines the storage-neutral contract for work-order photo
// evidence. Object keys are opaque references; callers never choose them and
// objects are only exposed through short-lived signed URLs.
package evidence

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Kind identifies which field transition an evidence image supports.
type Kind string

const (
	// KindInstall is evidence captured before an installation is submitted.
	KindInstall Kind = "install"
	// KindRemove is evidence captured after a cover is physically removed.
	KindRemove Kind = "remove"

	// MaxImageBytes is the largest accepted evidence image (10 MiB).
	MaxImageBytes int64 = 10 * 1024 * 1024

	objectRoot = "evidence/v1"
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

	// ErrObjectNotFound is returned when a referenced object does not exist.
	ErrObjectNotFound = errors.New("evidence object not found")
)

// Upload contains the browser-facing signed PUT URL and its server-generated
// opaque object key.
type Upload struct {
	UploadURL string `json:"uploadUrl"`
	ObjectKey string `json:"objectKey"`
}

// ObjectMetadata is the trusted metadata returned by the internal object-store
// endpoint after an upload.
type ObjectMetadata struct {
	ContentType         string
	DetectedContentType string
	Size                int64
}

// Store is the minimum private-object storage surface used by work orders.
type Store interface {
	PresignPut(ctx context.Context, kind Kind, workOrderID, coverID, contentType string, size int64) (*Upload, error)
	Stat(ctx context.Context, objectKey string) (*ObjectMetadata, error)
	PresignGet(ctx context.Context, objectKey string) (string, error)
}

// IsValid reports whether k is a supported evidence kind.
func (k Kind) IsValid() bool {
	return k == KindInstall || k == KindRemove
}

// ValidateIdentifier rejects path separators, traversal, whitespace, and
// control characters before an identifier can become part of an object key.
func ValidateIdentifier(value string) error {
	if !identifierPattern.MatchString(value) {
		return errors.New("identifier must use 1-128 letters, digits, underscore, or dash")
	}
	return nil
}

// NormalizeImageContentType returns a canonical supported image media type.
func NormalizeImageContentType(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return "", errors.New("invalid image content type")
	}
	mediaType = strings.ToLower(mediaType)
	switch mediaType {
	case "image/jpeg", "image/png", "image/webp":
		return mediaType, nil
	default:
		return "", errors.New("content type must be image/jpeg, image/png, or image/webp")
	}
}

// ValidateImageMetadata validates both declared and storage-reported image
// metadata. Zero-byte objects and images above 10 MiB are rejected.
func ValidateImageMetadata(contentType string, size int64) (string, error) {
	normalized, err := NormalizeImageContentType(contentType)
	if err != nil {
		return "", err
	}
	if size <= 0 {
		return "", errors.New("image size must be greater than zero")
	}
	if size > MaxImageBytes {
		return "", fmt.Errorf("image size must not exceed %d bytes", MaxImageBytes)
	}
	return normalized, nil
}

// ValidateStoredImageMetadata verifies both S3 metadata and magic-byte content
// sniffing. Content-Type metadata alone is uploader-controlled and therefore
// cannot prove that an object contains an image.
func ValidateStoredImageMetadata(metadata *ObjectMetadata) (string, error) {
	if metadata == nil {
		return "", errors.New("evidence object metadata is missing")
	}
	reported, err := ValidateImageMetadata(metadata.ContentType, metadata.Size)
	if err != nil {
		return "", err
	}
	detected, err := NormalizeImageContentType(metadata.DetectedContentType)
	if err != nil {
		return "", errors.New("object bytes are not a supported image")
	}
	if detected != reported {
		return "", fmt.Errorf("object content type %s does not match metadata %s", detected, reported)
	}
	return reported, nil
}

// NewObjectKey creates a relation-scoped key using an unpredictable UUID. The
// caller controls only validated relation identifiers and the canonical MIME
// type; it cannot supply any part of the random object name.
func NewObjectKey(kind Kind, workOrderID, coverID, contentType string) (string, error) {
	return objectKey(kind, workOrderID, coverID, contentType, uuid.NewString())
}

func objectKey(kind Kind, workOrderID, coverID, contentType, token string) (string, error) {
	if !kind.IsValid() {
		return "", errors.New("unsupported evidence kind")
	}
	if err := ValidateIdentifier(workOrderID); err != nil {
		return "", fmt.Errorf("invalid work order ID: %w", err)
	}
	if err := ValidateIdentifier(coverID); err != nil {
		return "", fmt.Errorf("invalid cover ID: %w", err)
	}
	if _, err := uuid.Parse(token); err != nil {
		return "", errors.New("invalid evidence token")
	}
	normalized, err := NormalizeImageContentType(contentType)
	if err != nil {
		return "", err
	}
	extension := map[string]string{
		"image/jpeg": "jpg",
		"image/png":  "png",
		"image/webp": "webp",
	}[normalized]
	return strings.Join([]string{objectRoot, string(kind), workOrderID, coverID, token + "." + extension}, "/"), nil
}

// ValidateObjectKey verifies that an opaque key belongs to the exact evidence
// kind, work order, and cover relation. URLs and caller-crafted prefixes fail.
func ValidateObjectKey(objectKey string, kind Kind, workOrderID, coverID string) error {
	if !kind.IsValid() {
		return errors.New("unsupported evidence kind")
	}
	if err := ValidateIdentifier(workOrderID); err != nil {
		return fmt.Errorf("invalid work order ID: %w", err)
	}
	if err := ValidateIdentifier(coverID); err != nil {
		return fmt.Errorf("invalid cover ID: %w", err)
	}
	if objectKey == "" || path.Clean(objectKey) != objectKey || strings.HasPrefix(objectKey, "/") {
		return errors.New("invalid evidence object key")
	}
	parts := strings.Split(objectKey, "/")
	if len(parts) != 6 || parts[0] != "evidence" || parts[1] != "v1" ||
		parts[2] != string(kind) || parts[3] != workOrderID || parts[4] != coverID {
		return errors.New("evidence object key does not match the requested relation")
	}
	nameParts := strings.Split(parts[5], ".")
	if len(nameParts) != 2 {
		return errors.New("invalid evidence object name")
	}
	if _, err := uuid.Parse(nameParts[0]); err != nil {
		return errors.New("invalid evidence object token")
	}
	switch nameParts[1] {
	case "jpg", "png", "webp":
		return nil
	default:
		return errors.New("invalid evidence object extension")
	}
}

// SignedReadTTL is the lifetime used for authenticated evidence reads.
const SignedReadTTL = 5 * time.Minute
