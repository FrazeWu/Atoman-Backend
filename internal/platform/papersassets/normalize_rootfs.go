package papersassets

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// RootfsNormalization describes an operator-approved deterministic rewrite
// of Docker-exported rootfs symlink targets. It is deliberately NOT exposed
// to arbitrary project uploads or the compile API.
type RootfsNormalization struct {
	Entries                int
	AbsoluteLinksRewritten int
	PrivilegeBitsStripped  int
	SystemdUnitsExcluded   int
	SizeBytes              int64
}

// NormalizeTrustedDockerExport makes an already-pinned, root-owned Docker
// image export safe for extraction under a host-private staging directory:
// absolute symlinks to image-root locations are rewritten as equivalent
// relative links. A strict second pass rejects every unsafe TAR structure.
// No user-supplied archive may be passed here.
func NormalizeTrustedDockerExport(ctx context.Context, sourcePath, destPath string) (stats RootfsNormalization, retErr error) {
	if ctx == nil || !filepath.IsAbs(sourcePath) || !filepath.IsAbs(destPath) ||
		sourcePath == destPath {
		return stats, ErrInvalid
	}
	sourceStat, err := os.Lstat(sourcePath)
	if err != nil {
		return stats, err
	}
	if !sourceStat.Mode().IsRegular() || sourceStat.Size() < 1024 ||
		sourceStat.Size() > MaxTrustedArchiveBytes || sourceStat.Mode().Perm()&0022 != 0 {
		return stats, ErrInvalid
	}
	owner, ok := sourceStat.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || os.Geteuid() != 0 {
		return stats, ErrInvalid
	}
	root := filepath.Dir(destPath)
	parent, err := os.Lstat(root)
	if err != nil {
		return stats, err
	}
	pOwner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || pOwner.Uid != 0 || !parent.IsDir() ||
		parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0077 != 0 {
		return stats, ErrInvalid
	}
	in, err := os.Open(sourcePath)
	if err != nil {
		return stats, err
	}
	defer in.Close()
	out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return stats, err
	}
	completed := false
	defer func() {
		if !completed {
			_ = out.Close()
			_ = os.Remove(destPath)
		}
	}()
	reader := tar.NewReader(in)
	writer := tar.NewWriter(out)
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("%w: Docker export TAR header: %v", ErrInvalid, err)
		}
		stats.Entries++
		// This single Debian systemd slice uses an escaped backslash
		// filename that the generic trusted TAR scanner rejects. It has
		// no purpose in an offline, no-systemd TeX sandbox. The exception
		// is exact and only applies to the operator-pinned Docker export.
		// Every other unsupported path remains a hard error.
		if strings.ContainsRune(header.Name, '\\') {
			if header.Name != `usr/lib/systemd/system/system-systemd\x2dmute\x2dconsole.slice` ||
				header.Typeflag != tar.TypeReg {
				return stats, ErrInvalid
			}
			stats.SystemdUnitsExcluded++
			continue
		}
		if stats.Entries > 500000 {
			return stats, ErrInvalid
		}
		if header.Typeflag == tar.TypeSymlink && path.IsAbs(header.Linkname) {
			name := strings.TrimPrefix(header.Name, "./")
			name = strings.TrimSuffix(name, "/")
			if name == "" || path.IsAbs(name) || strings.Contains(name, "..") {
				return stats, ErrInvalid
			}
			target := strings.TrimPrefix(path.Clean(header.Linkname), "/")
			relative, err := filepath.Rel(path.Dir(name), target)
			if err != nil {
				return stats, ErrInvalid
			}
			header.Linkname = filepath.ToSlash(relative)
			// The relativized target may exceed the old USTAR link field;
			// PAX preserves it exactly without truncation.
			header.Format = tar.FormatPAX
			if len(header.PAXRecords) > 0 {
				pax := make(map[string]string, len(header.PAXRecords))
				for k, v := range header.PAXRecords {
					if k != "linkpath" {
						pax[k] = v
					}
				}
				header.PAXRecords = pax
			}
			stats.AbsoluteLinksRewritten++
		}
		// Rootless TeX compilation never needs SUID/SGID or privileged
		// host helpers from the Docker export. Drop these permission bits
		// rather than relaxing the trusted TAR scanner.
		if (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA ||
			header.Typeflag == tar.TypeDir) && header.Mode&06000 != 0 {
			header.Mode &^= 06000
			stats.PrivilegeBitsStripped++
		}
		if err := writer.WriteHeader(header); err != nil {
			return stats, err
		}
		if header.Size > 0 {
			if _, err := io.CopyN(writer, reader, header.Size); err != nil {
				return stats, err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return stats, err
	}
	if err := out.Sync(); err != nil {
		return stats, err
	}
	if err := out.Close(); err != nil {
		return stats, err
	}
	check, err := os.Open(destPath)
	if err != nil {
		return stats, err
	}
	if err := ValidateTrustedTar(ctx, check); err != nil {
		check.Close()
		return stats, err
	}
	check.Close()
	info, err := os.Stat(destPath)
	if err != nil {
		return stats, err
	}
	stats.SizeBytes = info.Size()
	if stats.SizeBytes > MaxTrustedArchiveBytes {
		return stats, ErrInvalid
	}
	if err := os.Chmod(destPath, 0400); err != nil {
		return stats, err
	}
	completed = true
	return stats, nil
}
