// P0-only: provision two non-admin users on a disposable loopback MinIO.
// NEVER connect this tool to production, even with its test flag.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	madmin "github.com/minio/madmin-go/v3"
)

type policy struct {
	Version   string           `json:"Version"`
	Statement []map[string]any `json:"Statement"`
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "P0_IAM_ADMIN_FAILED:", err)
	os.Exit(1)
}
func env(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fatal(fmt.Errorf("missing %s", key))
	}
	return v
}
func main() {
	if os.Getenv("PAPERS_P0_IAM_CONFIRM") != "1" || len(os.Args) != 1 {
		fatal(errors.New("explicit P0-only IAM permission required"))
	}
	endpoint := env("PAPERS_P0_IAM_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" ||
		u.Path != "" || u.RawQuery != "" || u.Port() == "" {
		fatal(errors.New("refusing non-loopback IAM endpoint"))
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "9000" || port == "9100" || port == "9101" {
		fatal(errors.New("production-like IAM port forbidden"))
	}
	bucket := env("PAPERS_P0_IAM_BUCKET")
	if !strings.HasPrefix(bucket, "papers-private-p0-") || len(bucket) > 60 {
		fatal(errors.New("refusing non-disposable bucket"))
	}
	adminUser := env("PAPERS_P0_IAM_ADMIN_USER")
	if !strings.HasPrefix(adminUser, "papersp0root") {
		fatal(errors.New("refusing non-disposable admin user"))
	}
	adminSecret := env("PAPERS_P0_IAM_ADMIN_SECRET")
	pub := env("PAPERS_P0_IAM_PUBLISHER_USER")
	pubSecret := env("PAPERS_P0_IAM_PUBLISHER_SECRET")
	reader := env("PAPERS_P0_IAM_READER_USER")
	readerSecret := env("PAPERS_P0_IAM_READER_SECRET")
	if !strings.HasPrefix(pub, "papersp0pub") || !strings.HasPrefix(reader, "papersp0read") ||
		len(pubSecret) < 24 || len(readerSecret) < 24 {
		fatal(errors.New("invalid P0-only identities"))
	}
	adm, err := madmin.New(u.Host, adminUser, adminSecret, false)
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	resource := "arn:aws:s3:::" + bucket + "/papers/immutable/v1/*"
	bucketARN := "arn:aws:s3:::" + bucket
	writerPolicy := policy{Version: "2012-10-17", Statement: []map[string]any{
		{"Sid": "AllowPublisherContent", "Effect": "Allow",
			"Action":   []string{"s3:PutObject", "s3:GetObject"},
			"Resource": []string{resource}},
		{"Sid": "AllowPublisherBucketMetadata", "Effect": "Allow",
			"Action":   []string{"s3:GetBucketVersioning", "s3:GetBucketObjectLockConfiguration"},
			"Resource": []string{bucketARN}},
		{"Sid": "DenyPublisherUnconditionalPut", "Effect": "Deny",
			"Action": []string{"s3:PutObject"}, "Resource": []string{resource},
			"Condition": map[string]any{"Null": map[string]string{"s3:if-none-match": "true"}}},
	}}
	readerPolicy := policy{Version: "2012-10-17", Statement: []map[string]any{
		{"Sid": "ReadOnlyDigestAssets", "Effect": "Allow",
			"Action": []string{"s3:GetObject"}, "Resource": []string{resource}},
	}}
	// Explicit test-only fallback is never evidence of server-side
	// write-once enforcement. It lets us test separate non-admin accounts
	// even when the specific MinIO release rejects s3:if-none-match.
	if os.Getenv("PAPERS_P0_IAM_UNSAFE_TEST_MODE") == "1" {
		writerPolicy.Statement = writerPolicy.Statement[:len(writerPolicy.Statement)-1]
		fmt.Println("P0_IAM_UNSAFE_TEST_ONLY_CONDITIONAL_SERVER_ENFORCEMENT_UNAVAILABLE")
	}
	writerJSON, err := json.Marshal(writerPolicy)
	if err != nil {
		fatal(err)
	}
	readerJSON, err := json.Marshal(readerPolicy)
	if err != nil {
		fatal(err)
	}
	writerName := "papersp0-publish"
	readerName := "papersp0-read"
	if err := adm.AddCannedPolicy(ctx, writerName, writerJSON); err != nil {
		fatal(fmt.Errorf("server rejected mandatory conditional-write IAM policy: %w", err))
	}
	if err := adm.AddCannedPolicy(ctx, readerName, readerJSON); err != nil {
		fatal(err)
	}
	if err := adm.AddUser(ctx, pub, pubSecret); err != nil {
		fatal(err)
	}
	if err := adm.AddUser(ctx, reader, readerSecret); err != nil {
		fatal(err)
	}
	if err := adm.SetPolicy(ctx, writerName, pub, false); err != nil {
		fatal(err)
	}
	if err := adm.SetPolicy(ctx, readerName, reader, false); err != nil {
		fatal(err)
	}
	fmt.Println("P0_IAM_NON_ADMIN_PUBLISHER_READER_POLICIES_CREATED")
}
