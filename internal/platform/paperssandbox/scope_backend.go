package paperssandbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScopeBackend is a P0-only adapter for the trusted, fixed-argument
// p0-cgroup-scope-exec.sh helper. It does not create an OCI bundle and must
// not be connected to the production API or given user-supplied commands.
type CommandExecutor func(context.Context, string, ...string) ([]byte, error)

type ScopeBackendOptions struct {
	Workspace        string
	Bundle           string
	RunscState       string
	PDFPath          string
	RunscBinary      string
	SystemdRunBinary string
	ScopeHelper      string
	SystemctlBinary  string
	MountpointBinary string
	FindmntBinary    string
	UmountBinary     string
	Limits           Limits
	WrapperMemory    string
	WrapperCPUQuota  string
	WrapperTasksMax  int
	ExecuteCommand   CommandExecutor
	// AllowP0PIDProbe is available only to the standalone trusted P0 CLI.
	// Never expose it via public API or enable it for ordinary compile jobs.
	AllowP0PIDProbe bool
}

type ScopeBackend struct {
	cfg ScopeBackendOptions
}

func NewScopeBackend(c ScopeBackendOptions) (*ScopeBackend, error) {
	if err := c.Limits.Validate(); err != nil {
		return nil, err
	}
	if c.WrapperTasksMax < 32 || c.WrapperTasksMax > 512 ||
		c.WrapperMemory == "" || c.WrapperCPUQuota == "" {
		return nil, fmt.Errorf("%w: wrapper limits absent", ErrInvalidJob)
	}
	for _, b := range []string{
		c.RunscBinary, c.SystemdRunBinary, c.ScopeHelper,
		c.SystemctlBinary, c.MountpointBinary, c.FindmntBinary, c.UmountBinary,
	} {
		if !filepath.IsAbs(b) {
			return nil, fmt.Errorf("%w: tool path must be absolute", ErrInvalidJob)
		}
	}
	if !filepath.IsAbs(c.Workspace) || !filepath.IsAbs(c.Bundle) ||
		!filepath.IsAbs(c.RunscState) || !filepath.IsAbs(c.PDFPath) {
		return nil, fmt.Errorf("%w: workspace paths must be absolute", ErrInvalidJob)
	}
	root, err := filepath.EvalSymlinks(c.Workspace)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{c.Bundle, c.RunscState, filepath.Dir(c.PDFPath)} {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, real)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("%w: path outside isolated workspace: %s", ErrInvalidJob, p)
		}
	}
	rootfs := filepath.Join(c.Bundle, "rootfs")
	st, err := os.Lstat(rootfs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: rootfs must be an isolated directory", ErrInvalidJob)
	}
	actualRootfs, err := filepath.EvalSymlinks(rootfs)
	if err != nil {
		return nil, err
	}
	relRootfs, err := filepath.Rel(root, actualRootfs)
	if err != nil || relRootfs == "." || relRootfs == ".." || strings.HasPrefix(relRootfs, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: rootfs escapes workspace", ErrInvalidJob)
	}
	if !strings.HasSuffix(c.PDFPath, ".pdf") {
		return nil, ErrInvalidJob
	}
	if c.ExecuteCommand == nil {
		c.ExecuteCommand = runHostCommand
	}
	return &ScopeBackend{cfg: c}, nil
}

func runHostCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// No shell and no user-provided command string; output is capped.
	var output cappedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if output.truncated {
		return nil, fmt.Errorf("host helper produced excessive output")
	}
	return output.data, err
}

const maxHostDiagnostic = 1 << 20

type cappedOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if len(w.data)+n > maxHostDiagnostic {
		remain := maxHostDiagnostic - len(w.data)
		if remain > 0 {
			w.data = append(w.data, p[:remain]...)
		}
		w.truncated = true
	} else {
		w.data = append(w.data, p...)
	}
	return n, nil
}

func (b *ScopeBackend) runsc(ctx context.Context, args ...string) ([]byte, error) {
	fixed := []string{"--systemd-cgroup", "--root=" + b.cfg.RunscState}
	return b.cfg.ExecuteCommand(ctx, b.cfg.RunscBinary, append(fixed, args...)...)
}

func (b *ScopeBackend) Execute(ctx context.Context, job Job) (Evidence, error) {
	if err := job.Validate(); err != nil {
		return Evidence{}, err
	}
	if err := b.verifyBundle(job.ID); err != nil {
		return Evidence{}, err
	}
	// A previous run's PDF must never be passed off as this job's output.
	if _, err := os.Lstat(b.cfg.PDFPath); err == nil {
		return Evidence{}, fmt.Errorf("%w: PDF output already exists", ErrInvalidJob)
	} else if !os.IsNotExist(err) {
		return Evidence{}, err
	}
	unit := "atoman-papers-p0-" + job.ID
	runtimeLog := filepath.Join(b.cfg.Workspace, "runtime-"+job.ID+".log")
	timeoutSecs := int(math.Ceil(job.Timeout.Seconds()))
	args := []string{
		"--quiet", "--scope", "--collect", "--unit=" + unit,
		"-p", "MemoryMax=" + b.cfg.WrapperMemory,
		"-p", "MemorySwapMax=0",
		"-p", "CPUQuota=" + b.cfg.WrapperCPUQuota,
		"-p", "TasksMax=" + strconv.Itoa(b.cfg.WrapperTasksMax),
		"-p", "Delegate=yes",
		"/bin/bash", b.cfg.ScopeHelper,
		"PAPERS_P0_SCOPE_EXEC",
		b.cfg.RunscBinary, b.cfg.Workspace, b.cfg.Bundle,
		job.ID, runtimeLog, strconv.Itoa(timeoutSecs),
	}
	raw, runErr := b.cfg.ExecuteCommand(ctx, b.cfg.SystemdRunBinary, args...)
	if runErr != nil && len(raw) == 0 {
		return Evidence{}, runErr
	}
	verifiedPDF := b.verifyPDF()
	e, parseErr := ParseScopeEvidence(string(raw), b.cfg.Limits, verifiedPDF)
	if parseErr != nil {
		return Evidence{}, fmt.Errorf("cannot parse scope evidence: %w", parseErr)
	}
	if e.SentryCgroup != "/sys/fs/cgroup/system.slice/atomanp0-"+job.ID+".scope" {
		e.SentryObserved = false
		e.LimitsVerified = false
	}
	return e, runErr
}

func (b *ScopeBackend) verifyPDF() bool {
	st, err := os.Lstat(b.cfg.PDFPath)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(b.cfg.PDFPath)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 8 || info.Size() > 50<<20 {
		return false
	}
	header := make([]byte, 5)
	if _, err = f.Read(header); err != nil {
		return false
	}
	return string(header) == "%PDF-"
}

func (b *ScopeBackend) verifyBundle(jobID string) error {
	raw, err := os.ReadFile(filepath.Join(b.cfg.Bundle, "config.json"))
	if err != nil {
		return err
	}
	if len(raw) > 256<<10 {
		return fmt.Errorf("%w: oversized OCI spec", ErrInvalidJob)
	}
	// The P0 helper validates the live Sentry cgroup. This independent
	// preflight strictly validates the sealed OCI file before privilege use.
	return validateOCISpec(raw, jobID, b.cfg.Limits, filepath.Dir(b.cfg.PDFPath), b.cfg.AllowP0PIDProbe)
}

// Cleanup MUST be called regardless of Execute's result. Never use a wildcard
// kill or umount; operate only on the exact job ID and exact owned namespace.
func (b *ScopeBackend) Cleanup(ctx context.Context, job Job) error {
	if err := job.Validate(); err != nil {
		return err
	}
	listed, err := b.runsc(ctx, "list")
	if err != nil {
		return fmt.Errorf("cannot inspect sandbox list: %w", err)
	}
	if sandboxListed(string(listed), job.ID) {
		_, _ = b.runsc(ctx, "kill", job.ID, "KILL")
		if _, err := b.runsc(ctx, "delete", "--force", job.ID); err != nil {
			return fmt.Errorf("sandbox delete failed: %w", err)
		}
	}
	for i := 0; i < 6; i++ {
		list, err := b.runsc(ctx, "list")
		if err != nil {
			return fmt.Errorf("sandbox list recheck failed: %w", err)
		}
		if !sandboxListed(string(list), job.ID) {
			break
		}
		if i == 5 {
			return errors.New("sandbox remains after cleanup")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(75 * time.Millisecond):
		}
	}
	namespace := filepath.Join(b.cfg.RunscState, "null-netns")
	if _, err := os.Lstat(namespace); err == nil {
		_, mountErr := b.cfg.ExecuteCommand(ctx, b.cfg.MountpointBinary, "-q", namespace)
		if mountErr == nil {
			fs, err := b.cfg.ExecuteCommand(ctx, b.cfg.FindmntBinary, "-n", "-o", "FSTYPE", "--target", namespace)
			if err != nil || strings.TrimSpace(string(fs)) != "nsfs" {
				return errors.New("unexpected namespace mount; refusing to unmount")
			}
			if _, err := b.cfg.ExecuteCommand(ctx, b.cfg.UmountBinary, namespace); err != nil {
				return fmt.Errorf("test-owned namespace unmount failed: %w", err)
			}
			if _, err := b.cfg.ExecuteCommand(ctx, b.cfg.MountpointBinary, "-q", namespace); err == nil {
				return errors.New("test-owned namespace remains mounted")
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	// Both the Sentry and wrapper scopes must be inactive before reusing the
	// heavy-job slot; systemd's is-active exit code 3 means inactive/failed.
	for _, unit := range []string{"atomanp0-" + job.ID + ".scope", "atoman-papers-p0-" + job.ID + ".scope"} {
		output, err := b.cfg.ExecuteCommand(ctx, b.cfg.SystemctlBinary, "is-active", unit)
		state := strings.TrimSpace(string(output))
		if state == "active" || state == "activating" || state == "deactivating" {
			return fmt.Errorf("owned cgroup scope still running: %s", unit)
		}
		if state != "inactive" && state != "failed" && state != "unknown" {
			return fmt.Errorf("cannot verify owned scope state %s: %w", unit, err)
		}
	}
	return nil
}

func sandboxListed(list, jobID string) bool {
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == jobID {
			return true
		}
	}
	return false
}
