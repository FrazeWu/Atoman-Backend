//go:build linux

package papersruntime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"time"

	"atoman/internal/platform/papersassets"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

func storageClient(c StorageCredential) (*papersassets.S3ConditionalAPI, error) {
	// The only destination is the audited local private MinIO listener.
	// Do not honor HTTP_PROXY, AWS credential chains or the web app's S3 URL.
	transport := &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 4, ResponseHeaderTimeout: 30 * time.Second}
	sess, err := session.NewSession(&aws.Config{
		Endpoint: aws.String(StorageEndpoint), Region: aws.String("us-east-1"),
		DisableSSL: aws.Bool(true), S3ForcePathStyle: aws.Bool(true),
		Credentials: credentials.NewStaticCredentials(c.AccessKey, c.SecretKey, ""),
		MaxRetries:  aws.Int(0), HTTPClient: &http.Client{Transport: transport, Timeout: 8 * time.Minute},
	})
	if err != nil {
		return nil, ErrConfig
	}
	return papersassets.NewS3ConditionalAPI(s3.New(sess))
}

func verifyPrivateBucket(ctx context.Context, client *papersassets.S3ConditionalAPI, days int64) error {
	versioned, err := client.VersioningEnabled(ctx, PrivateBucket)
	if err != nil || !versioned {
		return errors.New("papers runtime: private versioning not verified")
	}
	locked, err := client.LockCompliant(ctx, PrivateBucket, days)
	if err != nil || !locked {
		return errors.New("papers runtime: private retention not verified")
	}
	return nil
}

func trustedRegistry(client papersassets.TrustedArchiveTransport, roots map[string]ed25519.PublicKey, days int64) (*papersassets.PrivateS3Registry, *papersassets.TrustedArchiveRegistry, error) {
	registry, err := papersassets.NewPrivateS3Registry(client, PrivateBucket, SharedBucket)
	if err != nil {
		return nil, nil, err
	}
	trusted, err := papersassets.NewTrustedArchiveRegistry(registry, roots, days)
	if err != nil {
		return nil, nil, err
	}
	return registry, trusted, nil
}

func verifyReleasePin(ctx context.Context, registry *papersassets.TrustedArchiveRegistry, trust TrustConfig) error {
	release, err := registry.LoadRelease(ctx, trust.EnvironmentDigest)
	if err != nil || release.Claim.PublisherKeyID != trust.SignerID ||
		release.Claim.Environment.RunscRelease != RunscRelease {
		return errors.New("papers runtime: approved signed release not available")
	}
	return nil
}
