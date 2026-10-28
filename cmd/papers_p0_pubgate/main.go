//go:build linux

// P0 disposable write-only Unix gateway, NEVER an API server route.
// The gateway process has scoped S3 PutObject; the separate publisher has
// no PutObject permission and can only ask this service for conditional PUT.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/paperspubgate"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

func run() error {
	publisherUID, publisherGID := uint32(65534), 65534
	udsName := "sockets"
	if os.Getenv("PAPERS_P0_GATEWAY_SEPARATE_PUBLISHER") == "1" {
		publisherUID, publisherGID = 65531, 65531
		udsName = "pub-uds"
	}
	if os.Getenv("PAPERS_P0_GATEWAY_CONFIRM") != "1" || os.Geteuid() != 65532 || os.Getegid() != publisherGID || len(os.Args) != 2 {
		return errors.New("P0 isolated gateway identity/confirmation required")
	}
	root, err := filepath.Abs(os.Args[1])
	if err != nil {
		return err
	}
	if !(strings.HasPrefix(root, "/var/tmp/atoman-papers-p0-gate.") ||
		strings.HasPrefix(root, "/var/tmp/atoman-papers-p0-durable.")) || len(root) > 105 {
		return errors.New("refusing non-disposable workroot")
	}
	endpoint := os.Getenv("PAPERS_P0_GATEWAY_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "" || u.RawQuery != "" {
		return errors.New("only ephemeral loopback MinIO allowed")
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" || port == "9000" || port == "9100" || port == "9101" {
		return errors.New("production-like port rejected")
	}
	bucket := os.Getenv("PAPERS_P0_GATEWAY_BUCKET")
	user := os.Getenv("PAPERS_P0_GATEWAY_WRITER_USER")
	secret := os.Getenv("PAPERS_P0_GATEWAY_WRITER_SECRET")
	if !strings.HasPrefix(bucket, "papers-private-p0-") || !strings.HasPrefix(user, "papersp0gate") || len(secret) < 24 {
		return errors.New("only disposable gateway writer identity allowed")
	}
	for _, name := range []string{"PAPERS_P0_IAM_ADMIN_SECRET", "PAPERS_P0_IAM_PUBLISHER_SECRET", "PAPERS_P0_IAM_READER_SECRET", "AWS_SECRET_ACCESS_KEY"} {
		if os.Getenv(name) != "" {
			return fmt.Errorf("gateway has forbidden credential variable %s", name)
		}
	}
	client := s3.New(session.Must(session.NewSession(&aws.Config{
		Endpoint: aws.String(endpoint), Region: aws.String("us-east-1"), DisableSSL: aws.Bool(true),
		S3ForcePathStyle: aws.Bool(true), Credentials: credentials.NewStaticCredentials(user, secret, ""),
		MaxRetries: aws.Int(0),
	})))
	transport, err := papersassets.NewS3ConditionalAPI(client)
	if err != nil {
		return err
	}
	// Never accept publisher uploads until this gateway independently proves
	// private reads, versioning, true atomic 412 and COMPLIANCE retention.
	probe, err := papersassets.NewPrivateS3Registry(transport, bucket, "atoman-public")
	if err != nil {
		return err
	}
	ctxProbe, stopProbe := context.WithTimeout(context.Background(), 25*time.Second)
	defer stopProbe()
	if err := probe.Probe(ctxProbe); err != nil {
		return fmt.Errorf("gateway bucket capability probe: %w", err)
	}
	locked, err := transport.LockCompliant(ctxProbe, bucket, 1)
	if err != nil || !locked {
		return fmt.Errorf("gateway requires COMPLIANCE retention: %v", err)
	}
	fmt.Println("P0_GATEWAY_INDEPENDENT_PRIVATE_VERSIONING_CONDITIONAL_COMPLIANCE_PROBE_PASS")
	gateway, err := paperspubgate.New(transport, bucket, filepath.Join(root, "staging"), publisherUID)
	if err != nil {
		return err
	}
	srv, err := paperspubgate.NewUnixServer(gateway, filepath.Join(root, udsName, "publish.sock"), publisherGID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		done := make(chan struct{})
		go func() { _ = srv.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
		}
	}()
	fmt.Println("P0_GATEWAY_UNIX_PEERCRED_CONDITIONAL_WRITER_READY")
	return srv.Serve()
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "P0_GATEWAY_FAILED:", err)
		os.Exit(1)
	}
}
