package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Store struct {
	client         *s3.Client
	uploader       *transfermanager.Client
	bucket, prefix string
}

func NewR2(account, bucket, access, secret, prefix string) (*Store, error) {
	if !regexp.MustCompile(`^[a-fA-F0-9]{32}$`).MatchString(account) {
		return nil, errors.New("R2_ACCOUNT_ID must be a 32-digit Cloudflare account ID")
	}
	if bucket == "" || access == "" || secret == "" {
		return nil, errors.New("R2_BUCKET, R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY are required")
	}
	prefix, err := KeyPrefix(prefix)
	if err != nil {
		return nil, err
	}
	endpoint := "https://" + strings.ToLower(account) + ".r2.cloudflarestorage.com"
	client := s3.New(s3.Options{Region: "auto", BaseEndpoint: &endpoint, UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider(access, secret, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		RetryMaxAttempts:           3,
	})
	return newStore(client, bucket, prefix), nil
}

func newStore(client *s3.Client, bucket, prefix string) *Store {
	return &Store{client: client, bucket: bucket, prefix: prefix, uploader: transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = 16 << 20
		o.MultipartUploadThreshold = 16 << 20
		o.Concurrency = 2
		o.FailTimeout = 10 * time.Second
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})}
}

// Upload verifies the snapshot before calling this function. Object metadata
// stores a digest for subsequent restore verification (not a signature).
func (s *Store) Upload(ctx context.Context, path string) (string, error) {
	digest, size, err := hashFile(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s%s-%x.db", s.prefix, time.Now().UTC().Format("20060102T150405.000000000Z"), nonce)
	_, err = s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: f, ContentType: aws.String("application/vnd.sqlite3"), Metadata: map[string]string{"sha256": digest}})
	if err != nil {
		return "", fmt.Errorf("upload backup: %w", err)
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return "", fmt.Errorf("check uploaded backup: %w", err)
	}
	if head.ContentLength == nil || *head.ContentLength != size || head.Metadata["sha256"] != digest {
		return "", errors.New("uploaded backup size or metadata mismatch")
	}
	return key, nil
}

// VerifyLatest downloads into a private temporary directory and never writes
// to the production database. Listing requires R2 object-read permission.
func (s *Store) VerifyLatest(ctx context.Context, dir string) error {
	key, err := s.latest(ctx)
	if err != nil {
		return err
	}
	return s.download(ctx, key, dir, "")
}

// RestoreLatest installs a verified backup at a NEW path, never replacing any file.
func (s *Store) RestoreLatest(ctx context.Context, destination string) error {
	if destination == "" {
		return errors.New("restore destination is required")
	}
	key, err := s.latest(ctx)
	if err != nil {
		return err
	}
	return s.RestoreKey(ctx, key, destination)
}

// RestoreKey permits recovery from an older backup if the latest is damaged.
func (s *Store) RestoreKey(ctx context.Context, key, destination string) error {
	if destination == "" {
		return errors.New("restore destination is required")
	}
	if !strings.HasPrefix(key, s.prefix) || !strings.HasSuffix(key, ".db") {
		return errors.New("backup key is outside the configured prefix")
	}
	if err := ensureRestorePathEmpty(destination); err != nil {
		return err
	}
	return s.download(ctx, key, filepath.Dir(destination), destination)
}

// List streams keys without accumulating a potentially unbounded bucket listing.
func (s *Store) List(ctx context.Context, visit func(string) error) error {
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(s.prefix)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list backups: %w", err)
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			if strings.HasPrefix(key, s.prefix) && strings.HasSuffix(key, ".db") {
				if err := visit(key); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Store) latest(ctx context.Context) (string, error) {
	var latest string
	err := s.List(ctx, func(key string) error {
		if key > latest {
			latest = key
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if latest == "" {
		return "", errors.New("no backup found in the configured prefix")
	}
	return latest, nil
}

func ensureRestorePathEmpty(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Lstat(candidate); err == nil {
			return fmt.Errorf("restore path or SQLite sidecar already exists: %s", candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) download(ctx context.Context, key, dir, destination string) error {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("download backup: %w", err)
	}
	defer func() { _ = out.Body.Close() }()
	digest := out.Metadata["sha256"]
	if len(digest) != 64 || out.ContentLength == nil || *out.ContentLength <= 0 {
		return errors.New("backup is missing required size or checksum metadata")
	}
	if err := requireSpace(dir, *out.ContentLength); err != nil {
		return err
	}
	work, err := os.MkdirTemp(dir, ".metric-restore-check-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	f, err := os.OpenFile(work+"/restore.db", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(out.Body, *out.ContentLength+1))
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("download backup: %w", err)
	}
	if n != *out.ContentLength || fmt.Sprintf("%x", h.Sum(nil)) != digest {
		return errors.New("downloaded backup size or checksum mismatch")
	}
	if err := VerifySnapshot(ctx, work+"/restore.db"); err != nil {
		return err
	}
	if destination != "" {
		if err := ensureRestorePathEmpty(destination); err != nil {
			return err
		}
		if err := os.Link(work+"/restore.db", destination); err != nil {
			return fmt.Errorf("install verified restore without overwriting: %w", err)
		}
	}
	return nil
}
