//go:build linux

// One-shot operator release preparation. No HTTP or untrusted input paths.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"atoman/internal/platform/papersassets"
)

type credential struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}
type trust struct {
	EnvironmentDigest string `json:"environment_digest"`
	SignerID          string `json:"signer_id"`
	PublicKey         string `json:"public_key"`
	RetentionDays     int    `json:"retention_days"`
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "Papers release preparation failed")
		os.Exit(1)
	}
}
func write(name string, value any) {
	raw, err := json.Marshal(value)
	must(err)
	f, err := os.OpenFile(filepath.Join("/etc/atoman/papers", name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(err)
	_, err = f.Write(raw)
	must(err)
	must(f.Sync())
	must(f.Close())
}
func main() {
	if os.Geteuid() != 0 || os.Getenv("PAPERS_RELEASE_PREPARE_CONFIRM") != "1" || len(os.Args) != 1 {
		os.Exit(2)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Minute)
	defer stop()
	base := "/var/lib/atoman-papers-release-private"
	dest := filepath.Join(base, "normalized-rootfs.tar")
	stats, err := papersassets.NormalizeTrustedDockerExport(ctx, filepath.Join(base, "export.tar"), dest)
	must(err)
	digest, err := papersassets.HashFile(dest)
	must(err)
	env := papersassets.Environment{Version: 1, Name: "texlive-2026-p0", ImageDigest: "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4", ArchiveDigest: digest, RunscRelease: "20261019.0"}
	must(env.Validate())
	raw, err := json.Marshal(env)
	must(err)
	pub, private, err := ed25519.GenerateKey(nil)
	must(err)
	t := trust{papersassets.HashBytes(raw), "papers-release-20261124", base64.StdEncoding.EncodeToString(pub), 1}
	var p struct {
		Entries []struct {
			Role       string     `json:"role"`
			Credential credential `json:"credential"`
		} `json:"entries"`
	}
	input, err := os.ReadFile("/etc/atoman/papers/storage-provision.json")
	must(err)
	must(json.Unmarshal(input, &p))
	find := func(role string) credential {
		for _, e := range p.Entries {
			if e.Role == role && len(e.Credential.SecretKey) >= 32 {
				return e.Credential
			}
		}
		os.Exit(1)
		return credential{}
	}
	write("trust.json", t)
	write("api.json", map[string]any{"reader": find("api-reader"), "trust": t})
	write("publisher.json", map[string]any{"reader": find("environment-reader"), "trust": t, "private_key": base64.StdEncoding.EncodeToString(private), "environment": env})
	fmt.Printf("PINNED_ENVIRONMENT_NORMALIZED bytes=%d digest=%s\n", stats.SizeBytes, t.EnvironmentDigest)
}
