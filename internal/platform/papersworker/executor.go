// Package papersworker connects the P0 PostgreSQL queue's fenced claims to a
// verified offline TeX environment and the sandbox Supervisor. This is an
// internal, privileged-test adapter: it is NOT connected to web handlers.
package papersworker

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
)

var ErrInvalid = errors.New("papers worker: invalid or untrusted input")

// ArchiveFetcher is implemented by the pinned signed-release cache, not by
// untrusted user inputs. The only identifier passed is the authoritative
// PostgreSQL job's EnvironmentDigest after lease verification.
type ArchiveFetcher interface {
	FetchArchive(context.Context, string) (string, error)
}

type Config struct {
	WorkRoot         string // private persistent P0 workspace, not HTTP-supplied
	Store            papersassets.CompileAssetReader
	ArchivePath      string                    // trusted operator-pinned Docker export .tar
	ArchiveFetcher   ArchiveFetcher            // optional P0 signed-release cache
	PDFPublisher     papersassets.PDFPublisher // separately scoped private result gateway
	EnableLuaLaTeX   bool                      // Operator-reviewed opt-in for the exact pinned archive.
	BoundedWorkspace bool                      // mandatory when publishing persistent PDF results
	RunscBinary      string
	ScopeHelper      string
	SystemdRunBinary string
	SystemctlBinary  string
	MountpointBinary string
	FindmntBinary    string
	UmountBinary     string
	TarBinary        string
	Limits           paperssandbox.Limits
	Timeout          time.Duration
	WrapperMemory    string
	WrapperCPUQuota  string
	WrapperTasksMax  int
	Command          paperssandbox.CommandExecutor
}

// Executor may run only where the global PostgreSQL compile slot has been
// claimed. The queue controls cross-process exclusivity and lease renewal.
type Executor struct{ cfg Config }

func New(cfg Config) (*Executor, error) {
	if cfg.Store == nil || cfg.Timeout < time.Second || cfg.Timeout > 4*time.Minute ||
		cfg.Limits.Validate() != nil || cfg.WrapperMemory == "" ||
		cfg.WrapperCPUQuota == "" || cfg.WrapperTasksMax < 32 || cfg.WrapperTasksMax > 512 {
		return nil, ErrInvalid
	}
	for _, p := range []string{cfg.WorkRoot, cfg.ArchivePath, cfg.RunscBinary,
		cfg.ScopeHelper, cfg.SystemdRunBinary, cfg.SystemctlBinary,
		cfg.MountpointBinary, cfg.FindmntBinary, cfg.UmountBinary, cfg.TarBinary} {
		if !filepath.IsAbs(p) {
			return nil, ErrInvalid
		}
	}
	stat, err := os.Lstat(cfg.WorkRoot)
	if err != nil {
		return nil, err
	}
	// P0 gVisor runs with UID/GID 65534. The directory is root-owned
	// and may grant only group execute to 65534 (0710), never read or
	// traverse permissions to arbitrary host users.
	if !stat.IsDir() || stat.Mode().Perm()&0067 != 0 || stat.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: work root must be owner-private with no public access", ErrInvalid)
	}
	if stat.Mode().Perm()&0010 != 0 {
		owner, ok := stat.Sys().(*syscall.Stat_t)
		if !ok || owner.Gid != 65534 {
			return nil, fmt.Errorf("%w: group traversal only for sandbox GID 65534", ErrInvalid)
		}
	}
	if cfg.PDFPublisher != nil {
		cfg.BoundedWorkspace = true
	}
	if cfg.BoundedWorkspace {
		if os.Geteuid() != 0 {
			return nil, ErrInvalid
		}
		if err := validateOwnedRoot(cfg.WorkRoot); err != nil {
			return nil, err
		}
	}
	return &Executor{cfg: cfg}, nil
}

func validSandboxID(sandboxID string) bool {
	if len(sandboxID) != 27 || !strings.HasPrefix(sandboxID, "pc-") {
		return false
	}
	for _, ch := range sandboxID[3:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func (e *Executor) backend(sandboxID string) (*paperssandbox.ScopeBackend, error) {
	if !validSandboxID(sandboxID) {
		return nil, ErrInvalid
	}
	root := filepath.Join(e.cfg.WorkRoot, sandboxID)
	return paperssandbox.NewScopeBackend(paperssandbox.ScopeBackendOptions{
		Workspace: root, Bundle: filepath.Join(root, "bundle"),
		RunscState:  filepath.Join(root, "state"),
		PDFPath:     filepath.Join(root, "output", "main.pdf"),
		RunscBinary: e.cfg.RunscBinary, SystemdRunBinary: e.cfg.SystemdRunBinary,
		ScopeHelper: e.cfg.ScopeHelper, SystemctlBinary: e.cfg.SystemctlBinary,
		MountpointBinary: e.cfg.MountpointBinary, FindmntBinary: e.cfg.FindmntBinary,
		UmountBinary: e.cfg.UmountBinary, Limits: e.cfg.Limits,
		WrapperMemory: e.cfg.WrapperMemory, WrapperCPUQuota: e.cfg.WrapperCPUQuota,
		WrapperTasksMax: e.cfg.WrapperTasksMax, ExecuteCommand: e.cfg.Command,
	})
}
func mkdirReal(path string, mode os.FileMode) error {
	if err := os.Mkdir(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	// The P0 shell sets umask 077. Explicitly restore only the approved
	// mode needed by guest rootfs traversal, never world access to secrets.
	return os.Chmod(path, mode)
}

// GVisor may need host pathname traversal for the fixed guest identity.
// Provide only execute (not list/read/write) to GID 65534. The parent
// private workspace and per-job random IDs still protect user project data.
func mkdirGuestPath(path string) error {
	if os.Geteuid() != 0 {
		return ErrInvalid
	}
	if err := mkdirReal(path, 0700); err != nil {
		return err
	}
	if err := os.Chown(path, 0, 65534); err != nil {
		return err
	}
	return os.Chmod(path, 0710)
}

func (e *Executor) stage(ctx context.Context, c papersqueue.Claim) (stageErr error) {
	phase := "validate_claim"
	began := time.Now()
	defer func() {
		if stageErr != nil {
			// Only allowlisted phase and category are logged. No user source,
			// archive content, storage credentials or untrusted filenames.
			category := "operation_failed"
			switch {
			case errors.Is(ctx.Err(), context.Canceled):
				category = "context_canceled"
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				category = "deadline"
			case errors.Is(stageErr, os.ErrPermission):
				category = "permission"
			case errors.Is(stageErr, unix.ENOSPC):
				category = "disk_full"
			case errors.Is(stageErr, papersassets.ErrIntegrity):
				category = "integrity"
			case errors.Is(stageErr, os.ErrExist):
				category = "already_exists"
			}
			log.Printf("papers staging failed job=%s phase=%s category=%s duration_ms=%d", c.Job.ID, phase, category, time.Since(began).Milliseconds())
		}
	}()

	if c.Kind != papersqueue.ClaimRun || !c.Valid() || !validSandboxID(c.SandboxID) {
		return ErrInvalid
	}
	phase = "snapshot"
	snapshot, err := e.cfg.Store.LoadCompileSnapshot(ctx, c.Job.SnapshotDigest)
	if err != nil {
		return fmt.Errorf("snapshot rejected: %w", err)
	}
	compiler, validCompiler := papersassets.NormalizeCompiler(snapshot.Compiler)
	jobCompiler, validJobCompiler := papersassets.NormalizeCompiler(c.Job.Compiler)
	if !validCompiler || !validJobCompiler || compiler != jobCompiler {
		return fmt.Errorf("%w: compiler differs from immutable job", ErrInvalid)
	}
	if snapshot.ProjectID != c.Job.ProjectID || snapshot.Revision != c.Job.SourceRevision ||
		c.Job.MainFile != "main.tex" {
		return fmt.Errorf("%w: snapshot belongs to another job", ErrInvalid)
	}
	// The signed S3 archive cache is optional in P0; it is provided only
	// through privileged Launcher configuration. Never fall back to the
	// operator-pinned local archive if download or signature verification
	// failed, or the job's environment identity could silently change.
	phase = "archive_fetch"
	archivePath := e.cfg.ArchivePath
	if e.cfg.ArchiveFetcher != nil {
		archivePath, err = e.cfg.ArchiveFetcher.FetchArchive(ctx, c.Job.EnvironmentDigest)
		if err != nil {
			return fmt.Errorf("trusted archive fetch rejected: %w", err)
		}
		if !filepath.IsAbs(archivePath) {
			return ErrInvalid
		}
	}
	phase = "archive_verify"
	environment, err := e.cfg.Store.VerifyCompileArchive(ctx, c.Job.EnvironmentDigest, archivePath)
	if err != nil {
		return fmt.Errorf("environment rejected: %w", err)
	}
	if (compiler == papersassets.CompilerLuaLaTeX && !e.cfg.EnableLuaLaTeX) || !environment.SupportsCompiler(compiler) {
		return fmt.Errorf("%w: compiler not supported by verified environment", ErrInvalid)
	}
	mode := paperssandbox.P0Latex
	if environment == papersassets.AcademicEnvironment() {
		mode = paperssandbox.P0AcademicLatex
		if compiler == papersassets.CompilerLuaLaTeX {
			mode = paperssandbox.P0AcademicLuaLatex
		}
	}

	phase = "workspace"
	root := filepath.Join(e.cfg.WorkRoot, c.SandboxID)
	if err := mkdirGuestPath(root); err != nil {
		return err
	}
	if e.cfg.BoundedWorkspace {
		// This invocation exclusively created the root and has not started runsc.
		defer func() {
			if stageErr != nil {
				stageErr = errors.Join(stageErr, removeBoundedWorkspace(root))
			}
		}()
	}
	bundle := filepath.Join(root, "bundle")
	if err := mkdirGuestPath(bundle); err != nil {
		return err
	}
	rootfs := filepath.Join(bundle, "rootfs")
	if err := mkdirReal(rootfs, 0755); err != nil {
		return err
	}
	for _, name := range []string{"state", "output"} {
		if err := mkdirReal(filepath.Join(root, name), 0700); err != nil {
			return err
		}
	}
	// The tar is sourced ONLY from a pre-approved pinned image; the archive was
	// verified byte-for-byte immediately above. Do not generalize this P0
	// extraction path to uploads or arbitrary project ZIP files.
	phase = "archive_extract"
	cmd := exec.CommandContext(ctx, e.cfg.TarBinary, "--no-same-owner", "-xf", archivePath, "-C", rootfs)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 1024 {
			output = output[:1024]
		}
		return fmt.Errorf("trusted offline rootfs extraction failed: %w: %s", err, string(output))
	}
	phase = "source_stage"
	inputDir := filepath.Join(rootfs, "inputs")
	if err := mkdirReal(inputDir, 0755); err != nil {
		return err
	}
	for _, file := range snapshot.Files {
		target := filepath.Join(inputDir, filepath.FromSlash(file.Path))
		parent := filepath.Dir(target)
		if parent != inputDir {
			if err := os.MkdirAll(parent, 0755); err != nil {
				return err
			}
			// The P0 worker intentionally supports only trusted, validated
			// project-relative paths. Ensure each ancestor is traversable
			// despite umask 077, but never grant host access to its parent.
			for dir := parent; dir != inputDir; dir = filepath.Dir(dir) {
				if dir == filepath.Dir(dir) {
					return ErrInvalid
				}
				if err := os.Chmod(dir, 0755); err != nil {
					return err
				}
			}
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		raw, decodeErr := file.Bytes()
		if decodeErr != nil {
			f.Close()
			return decodeErr
		}
		if _, err = f.Write(raw); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err := os.Chmod(target, 0644); err != nil {
			return err
		}
	}
	phase = "output_quota"
	if err := os.Chown(filepath.Join(root, "output"), 65534, 65534); err != nil {
		return err
	}
	if e.cfg.BoundedWorkspace {
		if err := mountBoundedOutput(root); err != nil {
			return err
		}
	}
	phase = "oci_config"
	return paperssandbox.WriteP0BundleConfig(bundle, filepath.Join(root, "output"), c.SandboxID, e.cfg.Limits, mode)
}

func (e *Executor) Run(ctx context.Context, c papersqueue.Claim) (paperssandbox.Result, error) {
	if e == nil || c.Kind != papersqueue.ClaimRun || !c.Valid() || !validSandboxID(c.SandboxID) {
		return paperssandbox.Result{}, ErrInvalid
	}
	root := filepath.Join(e.cfg.WorkRoot, c.SandboxID)
	if err := e.stage(ctx, c); err != nil {
		result := paperssandbox.Result{JobID: c.SandboxID, Status: paperssandbox.IsolationFailed, UnderlyingStatus: paperssandbox.IsolationFailed}
		if e.cfg.BoundedWorkspace {
			if _, statErr := os.Lstat(root); os.IsNotExist(statErr) {
				clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
				result.CleanupVerified = e.verifyAbsentWorkspace(clean, c.SandboxID) == nil
				stop()
			}
		}
		return result, fmt.Errorf("fail-closed sandbox staging: %w", err)
	}
	backend, err := e.backend(c.SandboxID)
	if err != nil {
		return paperssandbox.Result{JobID: c.SandboxID}, err
	}
	if e.cfg.BoundedWorkspace {
		if err := validateQuota(root); err != nil {
			return paperssandbox.Result{JobID: c.SandboxID, Status: paperssandbox.IsolationFailed}, err
		}
	}
	supervisor := paperssandbox.NewSupervisor(backend)
	result, runErr := supervisor.Run(ctx, paperssandbox.Job{ID: c.SandboxID, Timeout: e.cfg.Timeout})
	if runErr == nil && result.Status == paperssandbox.Completed && result.CleanupVerified && e.cfg.PDFPublisher != nil {
		// Cleanup of live processes precedes any reads of guest-owned output.
		f, err := openResultPDF(filepath.Join(root, "output"))
		if err == nil {
			var ref papersassets.PDFRef
			ref, err = e.cfg.PDFPublisher.PublishPDF(ctx, c.Job.ID, c.Epoch, f)
			closeErr := f.Close()
			err = errors.Join(err, closeErr)
			if err == nil {
				if ref.Validate() != nil || ref.JobID != c.Job.ID || ref.Epoch != c.Epoch {
					err = ErrInvalid
				} else {
					result.PDFDigest, result.PDFSize = ref.Digest, ref.Size
					// SyncTeX is optional metadata. Read only the exact output file after
					// verified teardown, without following links or exposing guest paths.
					if snapshot, loadErr := e.cfg.Store.LoadCompileSnapshot(ctx, c.Job.SnapshotDigest); loadErr == nil {
						result.SourceMap = readSourceMap(filepath.Join(root, "output"), snapshot)
					}

				}
			}
		}
		if err != nil {
			result.Status = paperssandbox.MissingArtifact
			result.FailureDetail = "verified PDF persistence failed"
			runErr = err
		}
	}
	// Compiler-owned output is inspected only after verified process
	// teardown, using fixed names and bounded no-follow file descriptors.
	// No wrapper/host log is ever sent to an ordinary user.
	if result.CleanupVerified && result.LimitsVerified && result.SentryObserved &&
		result.ExitKnown && result.ExitCode == 1 &&
		(result.Status == paperssandbox.RuntimeFailed || result.Status == paperssandbox.CompileFailed) {
		if snapshot, err := e.cfg.Store.LoadCompileSnapshot(ctx, c.Job.SnapshotDigest); err == nil {
			result.Diagnostics = readCompileDiagnostics(filepath.Join(root, "output"), snapshot)
			if len(result.Diagnostics) > 0 {
				result.Status = paperssandbox.CompileFailed
				result.FailureDetail = string(paperssandbox.CompileFailed)
			}
		}
	}
	if e.cfg.BoundedWorkspace && result.CleanupVerified {
		if err := removeBoundedWorkspace(root); err != nil {
			result.CleanupVerified = false
			result.Status = paperssandbox.CleanupFailed
			result.FailureDetail = "owned output/workspace cleanup not verified"
			result.PDFDigest, result.PDFSize = "", 0
			result.SourceMap = nil
			result.Diagnostics = nil
			runErr = errors.Join(runErr, err)
		}
	}
	return result, runErr
}

func readCompileDiagnostics(dir string, snapshot papersassets.Snapshot) []papersassets.CompileDiagnostic {
	d, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer unix.Close(d)
	read := func(name string) string {
		fd, err := unix.Openat(d, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return ""
		}
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() < 1 || st.Size() > 128<<20 {
			return ""
		}
		const limit = int64(256 << 10)
		if st.Size() > limit {
			if _, err = f.Seek(st.Size()-limit, io.SeekStart); err != nil {
				return ""
			}
		}
		raw, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil || int64(len(raw)) > limit {
			return ""
		}
		return string(raw)
	}
	return papersassets.ParseCompileDiagnostics(read("main.log"), read("main.blg"), snapshot)
}

func readSourceMap(dir string, snapshot papersassets.Snapshot) []byte {
	d, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer unix.Close(d)
	fd, err := unix.Openat(d, "main.synctex.gz", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), "main.synctex.gz")
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() < 1 || st.Size() > 8<<20 {
		return nil
	}
	raw, err := papersassets.ParseSyncTeX(f, snapshot)
	if err != nil {
		return nil
	}
	return raw
}

func openResultPDF(dir string) (*os.File, error) {
	d, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(d)
	fd, err := unix.Openat(d, "main.pdf", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "main.pdf")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 8 || info.Size() > papersassets.MaxPDFBytes {
		f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}

// Reap is called only on an expired, fenced recovery lease. It does not rely
// on source/environment archives being available: cleanup must work even
// after the original input was removed or corrupted.
func (e *Executor) Reap(ctx context.Context, c papersqueue.Claim) error {
	if e == nil || c.Kind != papersqueue.ClaimRecover || !c.Valid() || !validSandboxID(c.SandboxID) {
		return ErrInvalid
	}
	root := filepath.Join(e.cfg.WorkRoot, c.SandboxID)
	if e.cfg.BoundedWorkspace {
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			return e.verifyAbsentWorkspace(ctx, c.SandboxID)
		}
	}
	backend, err := e.backend(c.SandboxID)
	if err != nil {
		return err
	}
	if err := backend.Cleanup(ctx, paperssandbox.Job{ID: c.SandboxID, Timeout: e.cfg.Timeout}); err != nil {
		return err
	}
	if e.cfg.BoundedWorkspace {
		return removeBoundedWorkspace(root)
	}
	return nil
}
