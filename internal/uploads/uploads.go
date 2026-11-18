package uploads

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var allowedTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
	"image/avif": ".avif",
}

type Service struct {
	Client    *minio.Client
	Bucket    string
	PublicURL string
	MaxBytes  int64
}

type Presigned struct {
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
	Public string            `json:"public_url"`
}

func NewFromEnv() (*Service, error) {
	endpoint := strings.TrimSpace(getenv("S3_ENDPOINT"))
	if endpoint == "" {
		return nil, nil
	}
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	secure := !strings.HasPrefix(getenv("S3_ENDPOINT"), "http://")
	pathStyle := strings.EqualFold(getenv("S3_PATH_STYLE"), "true")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(getenv("S3_ACCESS_KEY_ID"), getenv("S3_SECRET_ACCESS_KEY"), ""),
		Secure: secure,
		Region: getenv("S3_REGION"),
		BucketLookup: func() minio.BucketLookupType {
			if pathStyle {
				return minio.BucketLookupPath
			}
			return minio.BucketLookupAuto
		}(),
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	max := int64(10 * 1024 * 1024)
	if value := getenv("S3_MAX_UPLOAD_MB"); value != "" {
		var megabytes int64
		if _, err := fmt.Sscan(value, &megabytes); err == nil && megabytes > 0 {
			max = megabytes * 1024 * 1024
		}
	}
	return &Service{Client: client, Bucket: getenv("S3_BUCKET"), PublicURL: strings.TrimRight(getenv("S3_PUBLIC_BASE_URL"), "/"), MaxBytes: max}, nil
}

func (s *Service) Presign(ctx context.Context, filename, contentType string) (Presigned, error) {
	ext := allowedTypes[strings.ToLower(contentType)]
	if ext == "" {
		return Presigned{}, fmt.Errorf("unsupported image type")
	}
	if filename != "" {
		if guessed := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); guessed != "" && guessed != contentType {
			return Presigned{}, fmt.Errorf("file extension does not match content type")
		}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Presigned{}, err
	}
	now := time.Now().UTC()
	key := fmt.Sprintf("uploads/%04d/%02d/%s%s", now.Year(), now.Month(), hex.EncodeToString(random), ext)
	policy := minio.NewPostPolicy()
	if err := policy.SetBucket(s.Bucket); err != nil {
		return Presigned{}, err
	}
	if err := policy.SetKey(key); err != nil {
		return Presigned{}, err
	}
	if err := policy.SetExpires(time.Now().Add(5 * time.Minute)); err != nil {
		return Presigned{}, err
	}
	if err := policy.SetContentType(contentType); err != nil {
		return Presigned{}, err
	}
	if err := policy.SetContentLengthRange(1, s.MaxBytes); err != nil {
		return Presigned{}, err
	}
	postURL, fields, err := s.Client.PresignedPostPolicy(ctx, policy)
	if err != nil {
		return Presigned{}, err
	}
	public, err := url.JoinPath(s.PublicURL, key)
	if err != nil {
		return Presigned{}, err
	}
	return Presigned{URL: postURL.String(), Fields: fields, Public: public}, nil
}

var getenv = os.Getenv
