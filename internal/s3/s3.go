// Package s3 uploads backups to any S3-compatible storage (AWS S3,
// Cloudflare R2, Backblaze B2, MinIO, Wasabi, DigitalOcean Spaces ...).
package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Endpoint  string // e.g. https://s3.amazonaws.com or https://<account>.r2.cloudflarestorage.com
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

func (c Config) Valid() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
}

// PutFile uploads a local file to bucket/key using path-style addressing and
// SigV4 with an unsigned payload, which every major S3 implementation accepts.
func PutFile(ctx context.Context, c Config, key, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	req, err := newRequest(ctx, c, http.MethodPut, key, f)
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	return do(req, c)
}

// EnsureBucket creates the bucket if it does not exist yet.
func EnsureBucket(ctx context.Context, c Config) error {
	req, err := newRequest(ctx, c, http.MethodPut, "", nil)
	if err != nil {
		return err
	}
	err = do(req, c)
	if err != nil && (strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") || strings.Contains(err.Error(), "BucketAlreadyExists")) {
		return nil
	}
	return err
}

func Delete(ctx context.Context, c Config, key string) error {
	req, err := newRequest(ctx, c, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	return do(req, c)
}

func newRequest(ctx context.Context, c Config, method, key string, body io.Reader) (*http.Request, error) {
	base, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil {
		return nil, err
	}
	segments := []string{c.Bucket}
	if key = strings.TrimLeft(key, "/"); key != "" {
		segments = append(segments, strings.Split(key, "/")...)
	}
	var escaped []string
	for _, s := range segments {
		escaped = append(escaped, uriEncode(s))
	}
	base.Path = "/" + strings.Join(segments, "/")
	base.RawPath = "/" + strings.Join(escaped, "/") // sent verbatim, matching the signature
	return http.NewRequestWithContext(ctx, method, base.String(), body)
}

func do(req *http.Request, c Config) error {
	sign(req, c, time.Now().UTC())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("s3 %s: HTTP %d: %s", req.Method, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// uriEncode implements the AWS flavour of percent-encoding.
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' ||
			ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sign(req *http.Request, c Config, t time.Time) {
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	amzDate := t.Format("20060102T150405Z")
	date := t.Format("20060102")
	const payload = "UNSIGNED-PAYLOAD"
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payload)

	host := req.URL.Host
	canonicalHeaders := "host:" + host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	path := req.URL.EscapedPath()
	canonical := strings.Join([]string{req.Method, path, "", canonicalHeaders, signedHeaders, payload}, "\n")

	scope := date + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	k := hmacSHA256([]byte("AWS4"+c.SecretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))

	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKey, scope, signedHeaders, sig))
}
