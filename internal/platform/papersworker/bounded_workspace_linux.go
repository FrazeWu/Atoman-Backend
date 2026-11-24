//go:build linux

package papersworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const OutputQuotaBytes uint64 = 128 << 20
const OutputQuotaInodes uint64 = 4096

type ownedMount struct{ path, fs, source, mountOptions, superOptions string }

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

func mountsUnder(root string) ([]ownedMount, error) {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	var mounts []ownedMount
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		p := unescapeMount(f[4])
		if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
			continue
		}
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+3 >= len(f) {
			return nil, ErrInvalid
		}
		mounts = append(mounts, ownedMount{p, f[sep+1], unescapeMount(f[sep+2]), f[5], f[sep+3]})
	}
	return mounts, nil
}

func quotaSource(root string) string { return "atoman-papers-output-" + filepath.Base(root) }
func hasOption(s, want string) bool {
	for _, v := range strings.Split(s, ",") {
		if v == want {
			return true
		}
	}
	return false
}
func validateOwnedRoot(root string) error {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if real != root {
		return ErrInvalid
	}
	s, err := os.Lstat(root)
	if err != nil {
		return err
	}
	owner, ok := s.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !s.IsDir() || s.Mode().Perm()&0067 != 0 {
		return ErrInvalid
	}
	return nil
}

func validateQuota(root string) error {
	mounts, err := mountsUnder(root)
	if err != nil {
		return err
	}
	out := filepath.Join(root, "output")
	if len(mounts) != 1 {
		return errors.New("expected exactly one owned output mount")
	}
	m := mounts[0]
	if m.path != out || m.fs != "tmpfs" || m.source != quotaSource(root) {
		return errors.New("unexpected output mount identity")
	}
	for _, flag := range []string{"nosuid", "nodev", "noexec", "rw"} {
		if !hasOption(m.mountOptions, flag) {
			return fmt.Errorf("missing output mount flag %s", flag)
		}
	}
	if !hasOption(m.superOptions, "noswap") {
		return errors.New("output mount may use host swap")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(out, &stat); err != nil {
		return err
	}
	if stat.Type != unix.TMPFS_MAGIC || stat.Bsize <= 0 || stat.Blocks == 0 || stat.Blocks > OutputQuotaBytes/uint64(stat.Bsize) || stat.Files == 0 || stat.Files > OutputQuotaInodes {
		return errors.New("output block or inode quota not verified")
	}
	info, err := os.Lstat(out)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || owner.Uid != 65534 || owner.Gid != 65534 {
		return ErrInvalid
	}
	return nil
}

// Only the root-owned launcher creates this mount. The sandbox can never
// reach its backing directory or change the mount. Aggregate bytes and
// inodes are enforced by the kernel; noswap prevents host swap consumption.
func mountBoundedOutput(root string) error {
	if os.Geteuid() != 0 {
		return ErrInvalid
	}
	if err := validateOwnedRoot(root); err != nil {
		return err
	}
	mounts, err := mountsUnder(root)
	if err != nil || len(mounts) != 0 {
		return errors.New("refusing workspace with existing mounts")
	}
	out := filepath.Join(root, "output")
	real, err := filepath.EvalSymlinks(out)
	if err != nil || real != out {
		return ErrInvalid
	}
	s, err := os.Lstat(out)
	if err != nil || !s.IsDir() {
		return ErrInvalid
	}
	if err := unix.Mount(quotaSource(root), out, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		"size=134217728,nr_inodes=4096,noswap,mode=0700,uid=65534,gid=65534"); err != nil {
		return err
	}
	if err := validateQuota(root); err != nil {
		return errors.Join(err, unix.Unmount(out, 0))
	}
	return nil
}

// Called ONLY after the exact runtime and both systemd scopes are verified
// stopped (or by the staging invocation before runtime creation). No lazy
// unmounts, no unrelated mounts, no symlink traversal.
func removeBoundedWorkspace(root string) error {
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		mounts, e := mountsUnder(root)
		if e != nil {
			return e
		}
		if len(mounts) > 0 {
			return ErrInvalid
		}
		return nil
	} else if err != nil {
		return err
	}
	if err := validateOwnedRoot(root); err != nil {
		return err
	}
	mounts, err := mountsUnder(root)
	if err != nil {
		return err
	}
	if len(mounts) > 0 {
		if len(mounts) != 1 || mounts[0].path != filepath.Join(root, "output") || mounts[0].fs != "tmpfs" || mounts[0].source != quotaSource(root) {
			return errors.New("unknown mount in compile workspace; preserve for inspection")
		}
		if err := unix.Unmount(mounts[0].path, 0); err != nil {
			return err
		}
	}
	mounts, err = mountsUnder(root)
	if err != nil {
		return err
	}
	if len(mounts) != 0 {
		return ErrInvalid
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		return errors.New("compile workspace remains")
	}
	return nil
}

func (e *Executor) verifyAbsentWorkspace(ctx context.Context, id string) error {
	// A completed launcher may have removed the workspace just before a
	// worker/DB crash. Absence alone is insufficient: verify both scopes.
	for _, name := range []string{"atomanp0-" + id + ".scope", "atoman-papers-p0-" + id + ".scope"} {
		var data []byte
		if e.cfg.Command != nil {
			data, _ = e.cfg.Command(ctx, e.cfg.SystemctlBinary, "is-active", name)
		} else {
			data, _ = exec.CommandContext(ctx, e.cfg.SystemctlBinary, "is-active", name).Output()
		}
		state := strings.TrimSpace(string(data))
		if state != "inactive" && state != "failed" && state != "unknown" {
			return fmt.Errorf("cannot verify inactive scope %s", name)
		}
	}
	return removeBoundedWorkspace(filepath.Join(e.cfg.WorkRoot, id))
}
