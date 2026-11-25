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
func TestAcademicXeLaTeXLive(t *testing.T) {
	archive := os.Getenv("PAPERS_ACADEMIC_CJK_TEST_ARCHIVE")
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
	env, err := store.PublishEnvironment(papersassets.AcademicCJKEnvironment())
	require.NoError(t, err)
	project := uuid.New()
	source := `\documentclass[UTF8,fontset=none]{ctexart}
\usepackage{shellesc}
\setCJKmainfont{Noto Serif CJK SC}
\setCJKsansfont{Noto Sans CJK SC}
\begin{document}
\title{中文论文编译验收}
\author{测试作者}
\maketitle
\section{研究背景}
中文正文与英文 English 混排，数学公式 $E=mc^2$。
\label{sec:background}
见第\ref{sec:background}节，参考文献\cite{fixture}。
\ifnum\ShellEscapeStatus=0\relax\else\errmessage{shell escape enabled}\fi
\bibliographystyle{plain}
\bibliography{refs}
\end{document}
`
	snapshot, err := store.PublishSnapshot(papersassets.Snapshot{Version: 1, ProjectID: project, Revision: 1, Compiler: papersassets.CompilerXeLaTeX, Files: []papersassets.SourceFile{
		{Path: "main.tex", Content: source},
		{Path: "refs.bib", Content: "@article{fixture,author={A. Author},title={Synthetic acceptance fixture},journal={Test},year={2026}}"},
	}})
	require.NoError(t, err)
	claim := papersqueue.Claim{Kind: papersqueue.ClaimRun, Job: model.PaperCompileJob{ID: uuid.New(), ProjectID: project, SourceRevision: 1, MainFile: "main.tex", Compiler: papersassets.CompilerXeLaTeX, EnvironmentDigest: env, SnapshotDigest: snapshot}, WorkerID: "isolated-xe-acceptance", Token: uuid.New(), Epoch: 1, SandboxID: "pc-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]}
	publisher := &quotaPDFPublisher{}
	var guestLog string
	executor, err := New(Config{WorkRoot: work, Store: store, ArchivePath: archive, PDFPublisher: publisher, BoundedWorkspace: true, EnableXeLaTeX: true,
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
	require.Regexp(t, `AT0MAN_LATEX_COMPLETE=[34]`, guestLog)
	require.NotContains(t, guestLog, "Missing character:")
	require.NotEmpty(t, result.SourceMap, "XeLaTeX must preserve verified SyncTeX")
	require.Greater(t, result.PDFSize, int64(1000))
	require.NoDirExists(t, filepath.Join(work, claim.SandboxID))
}
