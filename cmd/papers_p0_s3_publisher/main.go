//go:build linux

// P0-only, operator-authorized offline full-image S3 publisher.
// The signer secret is generated in memory and never passed to Launcher.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"atoman/internal/platform/papersassets"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

func mustEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		panic("P0 missing required " + name)
	}
	return value
}
func oneManifest(root, kind string) (string, error) {
	files, err := os.ReadDir(filepath.Join(root, "assets", kind))
	if err != nil {
		return "", err
	}
	if len(files) != 1 || files[0].IsDir() {
		return "", errors.New("P0 must have exactly one seeded " + kind + " manifest")
	}
	name := files[0].Name()
	if !strings.HasSuffix(name, ".json") {
		return "", errors.New("unrecognized manifest path")
	}
	return "sha256:" + strings.TrimSuffix(name, ".json"), nil
}
func run() error {
	if os.Geteuid() != 0 || os.Getenv("PAPERS_P0_FULLENV_CONFIRM") != "1" ||
		len(os.Args) != 2 {
		return errors.New("P0-only root explicit confirmation required")
	}
	root := os.Args[1]
	if !filepath.IsAbs(root) || !strings.HasPrefix(root, "/var/tmp/atoman-papers-p0-durable.") ||
		len(root) > 90 {
		return errors.New("non-disposable project root rejected")
	}
	endpoint := mustEnv("PAPERS_P0_FULLENV_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" ||
		u.Path != "" || u.RawQuery != "" {
		return errors.New("P0-only loopback S3 endpoint required")
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "9000" || port == "9100" || port == "9101" {
		return errors.New("default/production MinIO S3 port forbidden")
	}
	bucket := mustEnv("PAPERS_P0_FULLENV_BUCKET")
	if !strings.HasPrefix(bucket, "papers-private-p0-") {
		return errors.New("non-disposable bucket rejected")
	}
	username := mustEnv("PAPERS_P0_FULLENV_PUBLISHER_ID")
	password := mustEnv("PAPERS_P0_FULLENV_PUBLISHER_SECRET")
	if !strings.HasPrefix(username, "papersp0pub") || len(password) < 24 ||
		os.Getenv("PAPERS_P0_FULLENV_READER_SECRET") != "" {
		return errors.New("P0 publisher must not receive reader credentials")
	}
	client := s3.New(session.Must(session.NewSession(&aws.Config{
		Endpoint: aws.String(endpoint), Region: aws.String("us-east-1"),
		S3ForcePathStyle: aws.Bool(true), DisableSSL: aws.Bool(true),
		Credentials: credentials.NewStaticCredentials(username, password, ""),
		MaxRetries:  aws.Int(0),
	})))
	transport, err := papersassets.NewS3ConditionalAPI(client)
	if err != nil {
		return err
	}
	registry, err := papersassets.NewPrivateS3Registry(transport, bucket, "atoman-public")
	if err != nil {
		return err
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	defer func() {
		for i := range private {
			private[i] = 0
		}
	}()
	trusted, err := papersassets.NewTrustedArchiveRegistry(registry, map[string]ed25519.PublicKey{
		"p0-tex-publisher": pub,
	}, 1)
	if err != nil {
		return err
	}
	ctx, stop := context.WithTimeout(context.Background(), 9*time.Minute)
	defer stop()
	if err := trusted.ProbePublisher(ctx); err != nil {
		return err
	}
	local, err := papersassets.OpenStore(filepath.Join(root, "assets"))
	if err != nil {
		return err
	}
	snapID, err := oneManifest(root, "snapshots")
	if err != nil {
		return err
	}
	snap, err := local.LoadSnapshot(snapID)
	if err != nil {
		return err
	}
	publishedSnap, err := registry.PublishSnapshot(ctx, snap)
	if err != nil {
		return err
	}
	if publishedSnap != snapID {
		return papersassets.ErrIntegrity
	}
	envID, err := oneManifest(root, "environments")
	if err != nil {
		return err
	}
	env, err := local.LoadEnvironment(envID)
	if err != nil {
		return err
	}
	publishedEnv, err := registry.PublishEnvironment(ctx, env)
	if err != nil {
		return err
	}
	if publishedEnv != envID {
		return papersassets.ErrIntegrity
	}
	releaseID, err := trusted.PublishTrustedArchive(ctx, env, filepath.Join(root, "rootfs.tar"), "p0-tex-publisher", private)
	if err != nil {
		return err
	}
	if releaseID != envID {
		return papersassets.ErrIntegrity
	}
	keyFile := filepath.Join(root, "trusted-publisher.pub")
	f, err := os.OpenFile(keyFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(pub)
	_, err = f.WriteString(encoded + "\n")
	if err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("P0_REAL_TEX_S3_SIGNED_RELEASE_PUBLISHED environment=%s snapshot=%s\n", envID, snapID)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "P0_SIGNED_PUBLISH_FAILED:", err)
		os.Exit(1)
	}
}
