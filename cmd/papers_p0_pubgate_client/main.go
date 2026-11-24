//go:build linux

// Disposable nonroot producer acceptance: no direct MinIO write key.
// Use actual PrivateS3Registry + signed TrustedArchiveRegistry interfaces.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/paperspubgate"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
)

func run() error {
	if os.Getenv("PAPERS_P0_GATEWAY_CONFIRM") != "1" || os.Geteuid() != 65534 || len(os.Args) != 2 {
		return errors.New("P0-only unprivileged publisher identity required")
	}
	root, err := filepath.Abs(os.Args[1])
	if err != nil {
		return err
	}
	if !strings.HasPrefix(root, "/var/tmp/atoman-papers-p0-gate.") {
		return errors.New("invalid P0 root")
	}
	for _, name := range []string{"PAPERS_P0_GATEWAY_WRITER_SECRET", "PAPERS_P0_IAM_ADMIN_SECRET", "PAPERS_P0_IAM_READER_SECRET", "AWS_SECRET_ACCESS_KEY"} {
		if os.Getenv(name) != "" {
			return fmt.Errorf("publisher holds forbidden credential %s", name)
		}
	}
	endpoint := os.Getenv("PAPERS_P0_IAM_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" {
		return errors.New("non-loopback endpoint")
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "9000" || port == "9100" || port == "9101" {
		return errors.New("invalid P0 port")
	}
	bucket := os.Getenv("PAPERS_P0_IAM_BUCKET")
	user := os.Getenv("PAPERS_P0_IAM_PUBLISHER_USER")
	secret := os.Getenv("PAPERS_P0_IAM_PUBLISHER_SECRET")
	if !strings.HasPrefix(bucket, "papers-private-p0-") || !strings.HasPrefix(user, "papersp0pub") || len(secret) < 24 {
		return errors.New("invalid P0 publisher")
	}
	client := s3.New(session.Must(session.NewSession(&aws.Config{
		Endpoint: aws.String(endpoint), Region: aws.String("us-east-1"),
		DisableSSL: aws.Bool(true), S3ForcePathStyle: aws.Bool(true),
		Credentials: credentials.NewStaticCredentials(user, secret, ""), MaxRetries: aws.Int(0),
	})))
	reader, err := papersassets.NewS3ConditionalAPI(client)
	if err != nil {
		return err
	}
	pub, err := paperspubgate.NewTransport(filepath.Join(root, "sockets", "publish.sock"), bucket, reader)
	if err != nil {
		return err
	}
	registry, err := papersassets.NewPrivateS3Registry(pub, bucket, "atoman-public")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := registry.Probe(ctx); err != nil {
		return fmt.Errorf("publish probe: %w", err)
	}
	snapshot := papersassets.Snapshot{
		Version: 1, ProjectID: uuid.New(), Revision: 1,
		Files: []papersassets.SourceFile{{Path: "main.tex", Content: "\\documentclass{article}\\begin{document}hello\\end{document}"}},
	}
	snapID, err := registry.PublishSnapshot(ctx, snapshot)
	if err != nil {
		return err
	}
	loaded, err := registry.LoadSnapshot(ctx, snapID)
	if err != nil || loaded.ProjectID != snapshot.ProjectID {
		return errors.New("gateway snapshot roundtrip failed")
	}
	archive := filepath.Join(root, "client", "synthetic-tex.tar")
	f, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(f)
	contents := bytes.Repeat([]byte("Synthetic trusted P0 fixture; not an actual TeX Live rootfs.\n"), 96)
	for _, dir := range []string{"usr", "usr/share"} {
		if err = tw.WriteHeader(&tar.Header{Name: dir, Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
			return err
		}
	}
	if err = tw.WriteHeader(&tar.Header{Name: "usr/share/p0-fixture.txt", Mode: 0644, Typeflag: tar.TypeReg, Size: int64(len(contents))}); err != nil {
		return err
	}
	if _, err = tw.Write(contents); err != nil {
		return err
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(archive, 0400); err != nil {
		return err
	}
	archiveID, err := papersassets.HashFile(archive)
	if err != nil {
		return err
	}
	env := papersassets.Environment{Version: 1, Name: "texlive-2026-p0",
		ImageDigest:  "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4",
		RunscRelease: "20261019.0", ArchiveDigest: archiveID}
	envID, err := registry.PublishEnvironment(ctx, env)
	if err != nil {
		return err
	}
	pubKey, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	trusted, err := papersassets.NewTrustedArchiveRegistry(registry, map[string]ed25519.PublicKey{
		"p0-gateway-publisher": pubKey,
	}, 1)
	if err != nil {
		return err
	}
	if err = trusted.ProbePublisher(ctx); err != nil {
		return err
	}
	released, err := trusted.PublishTrustedArchive(ctx, env, archive, "p0-gateway-publisher", private)
	if err != nil {
		return err
	}
	if released != envID {
		return papersassets.ErrIntegrity
	}
	verified, err := trusted.LoadRelease(ctx, envID)
	if err != nil || verified.Claim.Environment.ArchiveDigest != archiveID {
		return errors.New("signed release verification failed")
	}
	cacheRoot := filepath.Join(root, "client", "cache")
	if err = os.Mkdir(cacheRoot, 0700); err != nil {
		return err
	}
	cache, err := papersassets.NewTrustedArchiveCache(trusted, cacheRoot)
	if err != nil {
		return err
	}
	cached, err := cache.FetchArchive(ctx, envID)
	if err != nil {
		return err
	}
	cachedID, err := papersassets.HashFile(cached)
	if err != nil || cachedID != archiveID {
		return papersassets.ErrIntegrity
	}
	fmt.Println("P0_GATEWAY_READONLY_PUBLISHER_SIGNED_SNAPSHOT_ENV_TAR_RELEASE_CACHE_PASS")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "P0_GATEWAY_CLIENT_ERROR:", err)
		os.Exit(1)
	}
}
