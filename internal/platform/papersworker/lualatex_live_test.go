//go:build linux

package papersworker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Explicit opt-in, synthetic-only validation. Never uses production data,
// production database, live source objects or the production launcher socket.
func TestAcademicLuaLaTeXLive(t *testing.T) {
	archive := os.Getenv("PAPERS_ACADEMIC_LUA_TEST_ARCHIVE")
	if archive == "" {
		t.Skip("opt-in pinned local archive and root-owned disposable sandbox required")
	}
	require.Equal(t, 0, os.Geteuid())
	root := t.TempDir()
	work := filepath.Join(root, "runs")
	assets := filepath.Join(root, "assets")
	require.NoError(t, os.Mkdir(work, 0700))
	require.NoError(t, os.Mkdir(assets, 0700))
	store, err := papersassets.OpenStore(assets)
	require.NoError(t, err)
	env, err := store.PublishEnvironment(papersassets.AcademicEnvironment())
	require.NoError(t, err)
	project := uuid.New()
	source := `\documentclass{article}
\usepackage{fontspec}
\setmainfont{Latin Modern Roman}
\begin{document}
\directlua{assert(status.shell_escape == 0)}
\directlua{local ok,r=pcall(os.execute,"true"); assert(not ok or not r,"external command unexpectedly allowed")}
\directlua{local ok,r=pcall(io.popen,"true"); assert(not ok or not r,"external process unexpectedly allowed")}
\directlua{local ok,r=pcall(require,"socket"); assert(not ok or not r,"socket module unexpectedly enabled")}
\directlua{local ok,r=pcall(io.open,"/inputs/fixture-probe.txt","w"); assert(not ok or not r,"immutable source unexpectedly writable")}
LuaLaTeX acceptance fixture. Caf\'e. Unicode: naïve.
\cite{fixture}.
\bibliographystyle{plain}
\bibliography{refs}
\end{document}
`
	snapshot, err := store.PublishSnapshot(papersassets.Snapshot{Version: 1, ProjectID: project, Revision: 1, Compiler: papersassets.CompilerLuaLaTeX, Files: []papersassets.SourceFile{
		{Path: "main.tex", Content: source},
		{Path: "refs.bib", Content: "@article{fixture,author={A. Author},title={Synthetic acceptance fixture},journal={Test},year={2026}}"},
	}})
	require.NoError(t, err)
	claim := papersqueue.Claim{Kind: papersqueue.ClaimRun, Job: model.PaperCompileJob{ID: uuid.New(), ProjectID: project, SourceRevision: 1, MainFile: "main.tex", Compiler: papersassets.CompilerLuaLaTeX, EnvironmentDigest: env, SnapshotDigest: snapshot}, WorkerID: "isolated-lua-acceptance", Token: uuid.New(), Epoch: 1, SandboxID: "pc-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]}
	publisher := &quotaPDFPublisher{}
	var guestLog string
	executor, err := New(Config{WorkRoot: work, Store: store, ArchivePath: archive, PDFPublisher: publisher, BoundedWorkspace: true, EnableLuaLaTeX: true,
		RunscBinary: "/opt/atoman/papers/bin/runsc", ScopeHelper: "/opt/atoman/papers/bin/scope-exec.sh", SystemdRunBinary: "/usr/bin/systemd-run", SystemctlBinary: "/usr/bin/systemctl", MountpointBinary: "/usr/bin/mountpoint", FindmntBinary: "/usr/bin/findmnt", UmountBinary: "/usr/bin/umount", TarBinary: "/usr/bin/tar",
		Limits: paperssandbox.Limits{MemoryMaxBytes: 768 << 20, PIDsMax: 256, CPUQuotaMicros: 100000, CPUPeriodMicros: 100000}, Timeout: 2 * time.Minute, WrapperMemory: "1024M", WrapperCPUQuota: "100%", WrapperTasksMax: 256,
		Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			raw, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			if name == "/usr/bin/systemd-run" {
				// This test only uses generated synthetic data. Capture bounded guest log before teardown.
				logPath := filepath.Join(work, claim.SandboxID, "runtime-"+claim.SandboxID+".log")
				log, readErr := os.ReadFile(logPath)
				if readErr == nil && len(log) <= 1<<20 {
					guestLog = string(log)
					if len(log) > 5000 {
						log = log[len(log)-5000:]
					}
					t.Logf("synthetic guest log:\n%s", log)
				}
			}
			return raw, err
		},
	})
	require.NoError(t, err)
	result, err := executor.Run(context.Background(), claim)
	t.Logf("status=%s exit=%d limits=%v cleanup=%v bytes=%d diagnostic=%+v", result.Status, result.ExitCode, result.LimitsVerified, result.CleanupVerified, result.PDFSize, result.Diagnostics)
	require.True(t, result.CleanupVerified, "disposable sandbox cleanup")
	require.NoError(t, err)
	require.Equal(t, paperssandbox.Completed, result.Status)
	require.True(t, result.LimitsVerified)
	require.True(t, result.SentryObserved)
	require.True(t, publisher.called)
	require.Contains(t, guestLog, "AT0MAN_BIBTEX_PASS=1")
	require.Contains(t, guestLog, "AT0MAN_LATEX_COMPLETE=3")
	require.NotEmpty(t, result.SourceMap, "LuaLaTeX must preserve verified SyncTeX")
	require.Greater(t, result.PDFSize, int64(1000))
	require.NoDirExists(t, filepath.Join(work, claim.SandboxID))
}
