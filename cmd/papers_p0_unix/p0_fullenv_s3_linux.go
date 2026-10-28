//go:build linux

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersworker"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

func fullenvAssets(root string) (papersassets.CompileAssetReader, papersworker.ArchiveFetcher, error) {
	if os.Getenv("PAPERS_P0_FULLENV_CONFIRM") != "1" {
		return nil, nil, errors.New("fullenv P0 explicitly disabled")
	}
	// The root Launcher must never receive the offline signer, producer
	// token or MinIO administrator's credentials.
	for _, key := range []string{
		"PAPERS_P0_FULLENV_PUBLISHER_SECRET",
		"PAPERS_P0_IAM_ADMIN_SECRET",
		"PAPERS_P0_IAM_PUBLISHER_SECRET",
		"PAPERS_P0_S3_TEST_SECRET",
	} {
		if os.Getenv(key) != "" {
			return nil, nil, fmt.Errorf("privileged Launcher received forbidden %s", key)
		}
	}
	endpoint := os.Getenv("PAPERS_P0_FULLENV_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" ||
		u.Path != "" || u.RawQuery != "" {
		return nil, nil, errors.New("S3 endpoint must be disposable loopback")
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "9000" || port == "9100" || port == "9101" {
		return nil, nil, errors.New("MinIO default/prod-like port forbidden")
	}
	bucket := os.Getenv("PAPERS_P0_FULLENV_BUCKET")
	readID := os.Getenv("PAPERS_P0_FULLENV_READER_USER")
	readSecret := os.Getenv("PAPERS_P0_FULLENV_READER_SECRET")
	if !strings.HasPrefix(bucket, "papers-private-p0-") ||
		!strings.HasPrefix(readID, "papersp0read") || len(readSecret) < 24 {
		return nil, nil, errors.New("untrusted private S3 reader config")
	}
	pubPath := filepath.Join(root, "trusted-publisher.pub")
	st, err := os.Lstat(pubPath)
	if err != nil {
		return nil, nil, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !st.Mode().IsRegular() ||
		st.Mode().Perm()&0077 != 0 || st.Size() < 40 || st.Size() > 150 {
		return nil, nil, errors.New("untrusted pinned publisher key file")
	}
	encoded, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, nil, errors.New("invalid trusted publisher Ed25519 public key")
	}
	client := s3.New(session.Must(session.NewSession(&aws.Config{
		Endpoint: aws.String(endpoint), Region: aws.String("us-east-1"),
		S3ForcePathStyle: aws.Bool(true), DisableSSL: aws.Bool(true),
		Credentials: credentials.NewStaticCredentials(readID, readSecret, ""),
		MaxRetries:  aws.Int(0),
	})))
	transport, err := papersassets.NewS3ConditionalAPI(client)
	if err != nil {
		return nil, nil, err
	}
	manifests, err := papersassets.NewPrivateS3Registry(transport, bucket, "atoman-public")
	if err != nil {
		return nil, nil, err
	}
	trusted, err := papersassets.NewTrustedArchiveRegistry(manifests, map[string]ed25519.PublicKey{
		"p0-tex-publisher": ed25519.PublicKey(key),
	}, 1)
	if err != nil {
		return nil, nil, err
	}
	cache, err := papersassets.NewTrustedArchiveCache(trusted, filepath.Join(root, "trusted-cache"))
	if err != nil {
		return nil, nil, err
	}
	return &papersassets.TrustedCompileAssetReader{Sources: manifests, Releases: trusted}, cache, nil
}
