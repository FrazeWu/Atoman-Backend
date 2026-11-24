//go:build linux

package papersruntime

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/paperspubgate"
)

type PublisherConfig struct {
	Reader      StorageCredential        `json:"reader"`
	Trust       TrustConfig              `json:"trust"`
	PrivateKey  string                   `json:"private_key"`
	Environment papersassets.Environment `json:"environment"`
}

// Operator-only one-shot signed release publisher. Never started by a web
// request or boot target. A signing key is never loaded by API/Launcher.
func runPublisher(ctx context.Context) error {
	if _, _, err := serviceIdentity("atoman-papers-publisher", "atoman-papers-publish"); err != nil {
		return err
	}
	var cfg PublisherConfig
	if err := loadCredential("papers-publisher.json", &cfg); err != nil {
		return err
	}
	if err := cfg.Reader.validate("environment-reader"); err != nil {
		return err
	}
	if cfg.Environment.Validate() != nil {
		return ErrConfig
	}
	raw, err := json.Marshal(cfg.Environment)
	if err != nil || papersassets.HashBytes(raw) != cfg.Trust.EnvironmentDigest {
		return ErrConfig
	}
	roots, err := cfg.Trust.roots()
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(cfg.PrivateKey)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return ErrConfig
	}
	private := ed25519.PrivateKey(key)
	if !private.Public().(ed25519.PublicKey).Equal(roots[cfg.Trust.SignerID]) {
		return ErrConfig
	}
	reader, err := storageClient(cfg.Reader)
	if err != nil {
		return err
	}
	transport, err := paperspubgate.NewTransport(EnvironmentSocket, PrivateBucket, reader)
	if err != nil {
		return err
	}
	_, trusted, err := trustedRegistry(transport, roots, cfg.Trust.RetentionDays)
	if err != nil {
		return err
	}
	publish, stop := context.WithTimeout(ctx, 12*time.Minute)
	defer stop()
	if err = trusted.ProbePublisher(publish); err != nil {
		return err
	}
	digest, err := trusted.PublishTrustedArchive(publish, cfg.Environment,
		"/var/lib/atoman-papers-release/normalized-rootfs.tar", cfg.Trust.SignerID, private)
	if err != nil {
		return err
	}
	if digest != cfg.Trust.EnvironmentDigest {
		return ErrConfig
	}
	fmt.Println("PAPERS_SIGNED_RELEASE_PUBLISHED", digest)
	return nil
}
