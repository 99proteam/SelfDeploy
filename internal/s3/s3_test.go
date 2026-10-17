package s3

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestURIEncode(t *testing.T) {
	if got := uriEncode("a b+c/é~"); got != "a%20b%2Bc%2F%C3%A9~" {
		t.Errorf("got %s", got)
	}
}

// TestLiveUpload runs against a real S3-compatible server when
// S3_TEST_ENDPOINT, S3_TEST_ACCESS_KEY and S3_TEST_SECRET_KEY are set.
func TestLiveUpload(t *testing.T) {
	c := Config{Endpoint: os.Getenv("S3_TEST_ENDPOINT"), Region: "us-east-1", Bucket: "selfdeploy-test",
		AccessKey: os.Getenv("S3_TEST_ACCESS_KEY"), SecretKey: os.Getenv("S3_TEST_SECRET_KEY")}
	if !c.Valid() {
		t.Skip("S3_TEST_* not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := EnsureBucket(ctx, c); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := EnsureBucket(ctx, c); err != nil {
		t.Fatalf("bucket creation must be idempotent: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup file.sql.gz")
	os.WriteFile(path, []byte("hello backup"), 0o600)
	key := "selfdeploy/db/backup file.sql.gz"
	if err := PutFile(ctx, c, key, path); err != nil {
		t.Fatalf("upload: %v", err)
	}
	req, _ := newRequest(ctx, c, http.MethodGet, key, nil)
	sign(req, c, time.Now().UTC())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello backup" {
		t.Fatalf("download: %d %s", resp.StatusCode, body)
	}
	if err := Delete(ctx, c, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	bad := c
	bad.SecretKey = "wrong"
	if err := PutFile(ctx, bad, key, path); err == nil {
		t.Fatal("upload with a wrong secret must fail (signature is verified)")
	}
}
