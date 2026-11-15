package ops

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	dbstore "traffic-center/internal/sqlite"
)

type object struct {
	body []byte
	hash string
}
type fakeUpload struct {
	key, hash string
	parts     map[string][]byte
}
type fakeS3 struct {
	mu                sync.Mutex
	objects           map[string]object
	uploads           map[string]*fakeUpload
	failPut, failHead bool
	signed            bool
}

func (s *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		s.signed = true
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	query := r.URL.Query()
	if _, ok := query["uploads"]; ok && r.Method == "POST" {
		s.uploads["upload"] = &fakeUpload{key: key, hash: r.Header.Get("X-Amz-Meta-Sha256"), parts: map[string][]byte{}}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>upload</UploadId></InitiateMultipartUploadResult>`)
		return
	}
	if id := query.Get("uploadId"); id != "" {
		u := s.uploads[id]
		if u == nil {
			w.WriteHeader(404)
			return
		}
		switch r.Method {
		case "PUT":
			b, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			u.parts[query.Get("partNumber")] = b
			w.Header().Set("ETag", `"part"`)
			return
		case "POST":
			var b []byte
			for i := 1; i <= len(u.parts); i++ {
				b = append(b, u.parts[fmt.Sprint(i)]...)
			}
			s.objects[u.key] = object{b, u.hash}
			delete(s.uploads, id)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
			return
		case "DELETE":
			delete(s.uploads, id)
			w.WriteHeader(204)
			return
		}
	}
	if r.Method == "GET" && r.URL.Query().Get("list-type") == "2" {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`)
		for k := range s.objects {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				_, _ = fmt.Fprintf(w, "<Contents><Key>%s</Key></Contents>", xmlEscape(k))
			}
		}
		_, _ = fmt.Fprint(w, `</ListBucketResult>`)
		return
	}
	switch r.Method {
	case "PUT":
		if s.failPut {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", 500)
			return
		}
		s.objects[key] = object{b, r.Header.Get("X-Amz-Meta-Sha256")}
		w.Header().Set("ETag", `"etag"`)
		w.WriteHeader(200)
	case "HEAD":
		if s.failHead {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		o, ok := s.objects[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(o.body)))
		w.Header().Set("X-Amz-Meta-Sha256", o.hash)
	case "GET":
		o, ok := s.objects[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(o.body)))
		w.Header().Set("X-Amz-Meta-Sha256", o.hash)
		_, _ = w.Write(o.body)
	default:
		w.WriteHeader(405)
	}
}
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func fakeStore(t *testing.T) (*Store, *fakeS3) {
	t.Helper()
	fake := &fakeS3{objects: map[string]object{}, uploads: map[string]*fakeUpload{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client := s3.New(s3.Options{Region: "auto", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test-key", "test-secret", ""), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired, RetryMaxAttempts: 1})
	return newStore(client, "bucket", "metric/"), fake
}
func liveDB(t *testing.T, dir string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(dir, "traffic db.sqlite")
	db, err := dbstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('p','test','hash')`); err != nil {
		t.Fatal(err)
	}
	return path, db
}
func TestSnapshotIncludesCommittedWALAndRestores(t *testing.T) {
	dir := t.TempDir()
	path, db := liveDB(t, dir)
	dst := filepath.Join(dir, "backup.db")
	if err := Snapshot(context.Background(), path, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('later','later','hash2')`); err != nil {
		t.Fatal(err)
	}
	restored, err := readOnlyDB(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var count int
	if err := restored.QueryRow(`SELECT count(*) FROM projects`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored count %d err %v", count, err)
	}
	if err := Snapshot(context.Background(), path, path); err == nil {
		t.Fatal("allowed source overwrite")
	}
	alias := filepath.Join(dir, "alias.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(context.Background(), path, alias); err == nil {
		t.Fatal("allowed source alias overwrite")
	}
	missing := filepath.Join(dir, "missing.db")
	if err := Snapshot(context.Background(), missing, filepath.Join(dir, "missing-copy.db")); err == nil {
		t.Fatal("missing DB accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("created missing source: %v", err)
	}
}
func TestBackupUploadFailureRestoreAndIntegrity(t *testing.T) {
	dir := t.TempDir()
	dbpath, _ := liveDB(t, dir)
	store, fake := fakeStore(t)
	service := NewService(dbpath, dir, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	if err := service.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	firstKey, err := store.latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := service.current()
	if state.LastSuccess == 0 || !state.LastAttemptSuccess {
		t.Fatalf("bad status %+v", state)
	}
	info, err := os.Stat(service.StatePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions: %v %v", info, err)
	}
	if got := NewService(dbpath, dir, store, nil).current(); got.LastSuccess != state.LastSuccess {
		t.Fatalf("state not persisted: %+v", got)
	}
	if err := store.VerifyLatest(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := service.RestoreCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if status := service.current(); status.LastRestoreSuccess == 0 || !status.LastRestoreAttemptSuccess {
		t.Fatalf("restore status: %+v", status)
	}
	restored := filepath.Join(dir, "restored.db")
	if err := store.RestoreLatest(ctx, restored); err != nil {
		t.Fatal(err)
	}
	if err := VerifySnapshot(ctx, restored); err != nil {
		t.Fatal(err)
	}
	appDB, err := dbstore.Open(restored)
	if err != nil {
		t.Fatalf("application could not open restored DB: %v", err)
	}
	var count int
	if err := appDB.QueryRow(`SELECT count(*) FROM projects`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("application data after restore count=%d err=%v", count, err)
	}
	_ = appDB.Close()
	if err := store.RestoreLatest(ctx, restored); err == nil {
		t.Fatal("restore overwrote an existing file")
	}
	sidecar := filepath.Join(dir, "unsafe.db")
	if err := os.WriteFile(sidecar+"-wal", []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreLatest(ctx, sidecar); err == nil {
		t.Fatal("restore ignored existing WAL")
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("unsafe restore installed: %v", err)
	}
	fake.mu.Lock()
	fake.failPut = true
	fake.mu.Unlock()
	if err := service.Backup(ctx); err == nil {
		t.Fatal("expected failed upload")
	}
	failed := service.current()
	if failed.LastAttemptSuccess || failed.LastSuccess != state.LastSuccess {
		t.Fatalf("false success after failure: %+v", failed)
	}
	fake.mu.Lock()
	fake.failPut = false
	fake.failHead = true
	fake.mu.Unlock()
	if err := service.Backup(ctx); err == nil {
		t.Fatal("expected failed head verification")
	}
	if service.current().LastAttemptSuccess {
		t.Fatal("head failure recorded as success")
	}
	fake.mu.Lock()
	fake.failHead = false
	signed := fake.signed
	fake.mu.Unlock()
	latestKey, err := store.latest(ctx)
	if err != nil || latestKey == firstKey {
		t.Fatalf("latest key %q, first %q: %v", latestKey, firstKey, err)
	}
	fake.mu.Lock()
	v := fake.objects[latestKey]
	v.body[0] ^= 1
	fake.objects[latestKey] = v
	fake.mu.Unlock()
	older := filepath.Join(dir, "older.db")
	if err := store.RestoreKey(ctx, firstKey, older); err != nil {
		t.Fatalf("older valid backup unusable: %v", err)
	}
	if err := store.RestoreKey(ctx, "other/bad.db", filepath.Join(dir, "bad.db")); err == nil {
		t.Fatal("accepted key outside prefix")
	}
	if !signed {
		t.Fatal("requests were not signed with AWS SDK")
	}
	if err := store.VerifyLatest(ctx, dir); err == nil {
		t.Fatal("corrupt download passed checksum")
	}
	lastRestore := service.current().LastRestoreSuccess
	if err := service.RestoreCheck(ctx); err == nil {
		t.Fatal("corrupt restore check succeeded")
	}
	if got := service.current(); got.LastRestoreAttemptSuccess || got.LastRestoreSuccess != lastRestore {
		t.Fatalf("corrupt restore reported success: %+v", got)
	}
	dirs, err := filepath.Glob(filepath.Join(dir, ".metric-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Fatalf("temporary files not cleaned up: %v", dirs)
	}
}
func TestScheduledRunPerformsInitialBackupAndRestoreCheck(t *testing.T) {
	dir := t.TempDir()
	path, _ := liveDB(t, dir)
	store, _ := fakeStore(t)
	svc := NewService(path, dir, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx, time.Hour, time.Minute) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if st := svc.current(); st.LastSuccess > 0 && st.LastRestoreSuccess > 0 {
				return
			}
		case <-deadline:
			t.Fatalf("scheduled backup and restore check did not finish: %+v", svc.current())
		}
	}
}

func TestMultipartBackupAndRestore(t *testing.T) {
	dir := t.TempDir()
	path, db := liveDB(t, dir)
	if _, err := db.Exec(`CREATE TABLE test_large(b BLOB); INSERT INTO test_large VALUES(zeroblob(18000000))`); err != nil {
		t.Fatal(err)
	}
	store, _ := fakeStore(t)
	svc := NewService(path, dir, store, nil)
	if err := svc.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyLatest(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}

func TestR2ConfigAndPrefix(t *testing.T) {
	if _, err := NewR2("invalid", "bucket", "key", "secret", "metric/"); err == nil {
		t.Fatal("accepted bad account")
	}
	if _, err := NewR2(strings.Repeat("a", 32), "bucket", "", "secret", "metric/"); err == nil {
		t.Fatal("accepted missing key")
	}
	for _, v := range []string{"", "../etc", "/absolute", "abc\\def"} {
		if _, err := KeyPrefix(v); err == nil {
			t.Fatalf("accepted prefix %q", v)
		}
	}
	if got, err := KeyPrefix("metric"); err != nil || got != "metric/" {
		t.Fatalf("prefix %q %v", got, err)
	}
}
func TestMetricsAndDiskGuard(t *testing.T) {
	dir := t.TempDir()
	path, _ := liveDB(t, dir)
	service := NewService(path, dir, nil, nil)
	if err := service.Backup(context.Background()); err == nil {
		t.Fatal("missing R2 credentials should fail backup")
	}
	if status := service.current(); status.LastAttempt == 0 || status.LastAttemptSuccess {
		t.Fatalf("missing configuration did not surface failure: %+v", status)
	}
	w := httptest.NewRecorder()
	service.Metrics(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()
	for _, want := range []string{"metric_disk_stat_success 1", "metric_database_stat_success 1", "metric_wal_stat_success 1", "metric_backup_last_success_timestamp_seconds 0"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	if err := requireSpace(dir, 1<<62); err == nil {
		t.Fatal("accepted impossible disk requirement")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Snapshot(ctx, path, filepath.Join(dir, "cancelled.db")); err == nil {
		t.Fatal("accepted cancelled context")
	}
}
