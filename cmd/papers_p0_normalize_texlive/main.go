//go:build linux

// A deliberately restricted P0-only tool for operator-approved, pinned,
// real Docker-exported TeX rootfs. Never run against uploaded user archives.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"atoman/internal/platform/papersassets"
)

func main() {
	if os.Geteuid() != 0 || os.Getenv("PAPERS_P0_NORMALIZE_CONFIRM") != "1" || len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "P0 normalize requires root, confirmation, and one disposable workroot")
		os.Exit(2)
	}
	root, err := filepath.Abs(os.Args[1])
	validFullenv := strings.HasPrefix(root, "/var/tmp/atoman-papers-p0-fullenv.") &&
		root != "/var/tmp/atoman-papers-p0-fullenv."
	validSelfContained := filepath.Base(root) == "image-prep" &&
		strings.HasPrefix(filepath.Dir(root), "/var/tmp/atoman-papers-p0-durable.")
	if err != nil || len(root) > 120 || (!validFullenv && !validSelfContained) {
		fmt.Fprintln(os.Stderr, "invalid P0 workroot")
		os.Exit(2)
	}
	result, err := papersassets.NormalizeTrustedDockerExport(context.Background(),
		filepath.Join(root, "texlive-rootfs.tar"), filepath.Join(root, "normalized-rootfs.tar"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "P0_NORMALIZE_FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("P0_NORMALIZED_TEXLIVE entries=%d rewritten_absolute_symlinks=%d stripped_suid_sgid=%d excluded_systemd_unit=%d tar_bytes=%d\n",
		result.Entries, result.AbsoluteLinksRewritten, result.PrivilegeBitsStripped, result.SystemdUnitsExcluded, result.SizeBytes)
}
