//go:build linux

package papersruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
	"atoman/internal/platform/papersworker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type diagnosticFixturePublisher struct{}

func (diagnosticFixturePublisher) PublishPDF(_ context.Context, id uuid.UUID, epoch int64, f *os.File) (papersassets.PDFRef, error) {
	st, err := f.Stat()
	if err != nil || st.Size() < 8 || st.Size() > papersassets.MaxPDFBytes {
		return papersassets.PDFRef{}, papersassets.ErrInvalid
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return papersassets.PDFRef{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, papersassets.MaxPDFBytes+1))
	if err != nil || n != st.Size() {
		return papersassets.PDFRef{}, papersassets.ErrIntegrity
	}
	return papersassets.PDFRef{JobID: id, Epoch: epoch, Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// Uses only synthetic sources and the exact already-approved academic
// archive. No production DB, project data, credentials or runtime policies
// are modified; the same Executor and enforced limits are used.
func TestAcademicCompilerDiagnosticsOffline(t *testing.T) {
	if os.Getenv("PAPERS_DIAGNOSTICS_OFFLINE_TEST") != "1" || os.Geteuid() != 0 {
		t.Skip("operator-only isolated acceptance")
	}
	archive := "/var/lib/atoman-papers/trusted-cache/83f5c23b237e83a05e73ab160b4ab6e3dd0a8597ccfb941c681e7fb77207ebe7.tar"
	digest, err := papersassets.HashFile(archive)
	require.NoError(t, err)
	require.Equal(t, papersassets.AcademicEnvironment().ArchiveDigest, digest)
	cases := []struct {
		name, main, chapter, bib, file, message string
		line                                    int
		ok                                      bool
	}{
		{"nested-error", `\documentclass{article}
\begin{document}
\input{chapters/results}
\end{document}`, "A paragraph.\n\\UnknownFixtureCommand\n", "", "chapters/results.tex", "Undefined control sequence", 2, false},
		{"missing-package", `\documentclass{article}
\usepackage{atoman-nonexistent-fixture-package}
\begin{document}
Test
\end{document}`, "", "", "", "atoman-nonexistent-fixture-package", 0, false},
		{"bibliography-error", `\documentclass{article}
\begin{document}
Reference \cite{fixture}.
\bibliographystyle{plain}
\bibliography{refs}
\end{document}`, "", "@article{fixture,\n title={Broken} author={Example},\n year={2026}\n}\n", "refs.bib", "expecting", 2, false},
		{"success", `\documentclass{article}
\begin{document}
\section{Evidence}\label{sec:one}
Reference \ref{sec:one} and \cite{fixture}.
\bibliographystyle{plain}
\bibliography{refs}
\end{document}`, "", "@article{fixture,author={Example, Alice},title={Fixture},journal={Journal},year={2026}}", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			snap := papersassets.Snapshot{Version: 1, ProjectID: uuid.New(), Revision: 1, Files: []papersassets.SourceFile{{Path: "main.tex", Content: tc.main}}}
			if tc.chapter != "" {
				snap.Files = append(snap.Files, papersassets.SourceFile{Path: "chapters/results.tex", Content: tc.chapter})
			}
			if tc.bib != "" {
				snap.Files = append(snap.Files, papersassets.SourceFile{Path: "refs.bib", Content: tc.bib})
			}
			root, err := os.MkdirTemp("/var/tmp", "papers-diagnostic-fixture.")
			require.NoError(t, err)
			require.NoError(t, os.Chown(root, 0, 65534))
			require.NoError(t, os.Chmod(root, 0710))
			runs := filepath.Join(root, "runs")
			require.NoError(t, os.Mkdir(runs, 0700))
			require.NoError(t, os.Chown(runs, 0, 65534))
			require.NoError(t, os.Chmod(runs, 0710))
			assets := filepath.Join(root, "assets")
			require.NoError(t, os.Mkdir(assets, 0700))
			store, err := papersassets.OpenStore(assets)
			require.NoError(t, err)
			env, err := store.PublishEnvironment(papersassets.AcademicEnvironment())
			require.NoError(t, err)
			source, err := store.PublishSnapshot(snap)
			require.NoError(t, err)
			sandbox := "pc-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
			claim := papersqueue.Claim{Kind: papersqueue.ClaimRun, Job: model.PaperCompileJob{ID: uuid.New(), ProjectID: snap.ProjectID, SourceRevision: 1, SnapshotDigest: source, EnvironmentDigest: env, MainFile: "main.tex"}, WorkerID: "diagnostic-fixture", Token: uuid.New(), Epoch: 1, SandboxID: sandbox}
			e, err := papersworker.New(papersworker.Config{
				WorkRoot: runs, Store: store, ArchivePath: archive, PDFPublisher: diagnosticFixturePublisher{}, BoundedWorkspace: true,
				RunscBinary: RunscPath, ScopeHelper: ScopeHelper, SystemdRunBinary: "/usr/bin/systemd-run", SystemctlBinary: "/usr/bin/systemctl", MountpointBinary: "/usr/bin/mountpoint", FindmntBinary: "/usr/bin/findmnt", UmountBinary: "/usr/bin/umount", TarBinary: "/usr/bin/tar",
				Limits:  paperssandbox.Limits{MemoryMaxBytes: 768 << 20, PIDsMax: 256, CPUQuotaMicros: 100000, CPUPeriodMicros: 100000},
				Timeout: 2 * time.Minute, WrapperMemory: "1024M", WrapperCPUQuota: "100%", WrapperTasksMax: 256,
			})
			require.NoError(t, err)
			result, err := e.Run(ctx, claim)
			t.Logf("status=%s sentry=%t limits=%t cleanup=%t exit=%d", result.Status, result.SentryObserved, result.LimitsVerified, result.CleanupVerified, result.ExitCode)
			if result.CleanupVerified {
				require.NoError(t, os.RemoveAll(root))
			}
			require.NoError(t, err)
			require.True(t, result.CleanupVerified && result.LimitsVerified && result.SentryObserved && result.ExitKnown)
			if tc.ok {
				require.Equal(t, paperssandbox.Completed, result.Status)
				require.Positive(t, result.PDFSize)
				require.NoError(t, papersassets.ValidateSourceMap(result.SourceMap))
				require.Empty(t, result.Diagnostics)
				return
			}
			require.Equal(t, paperssandbox.CompileFailed, result.Status)
			require.NotEmpty(t, papersassets.EncodeCompileDiagnostics(result.Diagnostics))
			found := false
			for _, d := range result.Diagnostics {
				if strings.Contains(strings.ToLower(d.Message), strings.ToLower(tc.message)) && (tc.file == "" || d.File == tc.file) && (tc.line == 0 || d.Line == tc.line) {
					found = true
				}
			}
			require.True(t, found, "expected synthetic diagnostic not found: %v", result.Diagnostics)
		})
	}
}
