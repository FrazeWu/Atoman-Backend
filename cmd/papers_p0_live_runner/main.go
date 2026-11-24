// Command papers_p0_live_runner performs exactly one controlled, local P0
// PID exhaustion test followed by one real LaTeX compile in separate sandboxes.
// This command must never be called by public API or service workers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"atoman/internal/platform/paperssandbox"
)

func main() {
	if os.Getenv("PAPERS_P0_LIVE_CONFIRM") != "1" || os.Geteuid() != 0 {
		fatal(errors.New("requires local root invocation with PAPERS_P0_LIVE_CONFIRM=1"))
	}
	var root, runsc, helper, suffix string
	flag.StringVar(&root, "root", "", "owned disposable P0 work root")
	flag.StringVar(&runsc, "runsc", "", "pinned verified runsc")
	flag.StringVar(&helper, "helper", "", "trusted P0 systemd helper")
	flag.StringVar(&suffix, "suffix", "", "job ID unique lowercase hex suffix")
	flag.Parse()
	if !filepath.IsAbs(root) || !filepath.IsAbs(runsc) || !filepath.IsAbs(helper) ||
		len(suffix) != 8 || strings.Trim(suffix, "0123456789abcdef") != "" {
		fatal(errors.New("invalid P0 local input paths or suffix"))
	}
	for _, p := range []string{root, runsc, helper} {
		if _, err := os.Stat(p); err != nil {
			fatal(err)
		}
	}
	lookup := func(bin string) string {
		p, err := exec.LookPath(bin)
		if err != nil {
			fatal(err)
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			fatal(err)
		}
		return abs
	}
	ids := []string{"p0-pids-" + suffix, "p0-tex-" + suffix}
	limits := []paperssandbox.Limits{
		{MemoryMaxBytes: 768 << 20, MemorySwapMaxBytes: 0, PIDsMax: 128, CPUQuotaMicros: 100000, CPUPeriodMicros: 100000},
		{MemoryMaxBytes: 768 << 20, MemorySwapMaxBytes: 0, PIDsMax: 256, CPUQuotaMicros: 100000, CPUPeriodMicros: 100000},
	}
	modes := []paperssandbox.P0Mode{paperssandbox.P0PIDPressure, paperssandbox.P0Latex}
	backends := map[string]*paperssandbox.ScopeBackend{}
	for i, id := range ids {
		jobRoot := filepath.Join(root, id)
		bundle := filepath.Join(jobRoot, "bundle")
		output := filepath.Join(jobRoot, "output")
		if err := paperssandbox.WriteP0BundleConfig(bundle, output, id, limits[i], modes[i]); err != nil {
			fatal(fmt.Errorf("bundle for %s: %w", id, err))
		}
		backend, err := paperssandbox.NewScopeBackend(paperssandbox.ScopeBackendOptions{
			Workspace: jobRoot, Bundle: bundle, RunscState: filepath.Join(jobRoot, "state"),
			PDFPath: filepath.Join(output, "main.pdf"), RunscBinary: runsc,
			SystemdRunBinary: lookup("systemd-run"), ScopeHelper: helper,
			SystemctlBinary: lookup("systemctl"), MountpointBinary: lookup("mountpoint"),
			FindmntBinary: lookup("findmnt"), UmountBinary: lookup("umount"),
			Limits: limits[i], WrapperMemory: "1024M", WrapperCPUQuota: "100%",
			WrapperTasksMax: 256, AllowP0PIDProbe: modes[i] == paperssandbox.P0PIDPressure,
		})
		if err != nil {
			fatal(err)
		}
		backends[id] = backend
	}
	supervisor := paperssandbox.NewSupervisor(routes{backends: backends})
	for i, id := range ids {
		timeout := 45 * time.Second
		if i == 1 {
			timeout = 90 * time.Second
		}
		result, err := supervisor.Run(context.Background(), paperssandbox.Job{ID: id, Timeout: timeout})
		if err != nil {
			fatal(err)
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		if !result.CleanupVerified {
			fatal(fmt.Errorf("%s cleanup did not verify, stopping", id))
		}
		if i == 0 {
			if result.Status != paperssandbox.PIDsExceeded {
				fatal(fmt.Errorf("PID test did not classify as PID exhaustion: %s", result.Status))
			}
		} else if result.Status != paperssandbox.Completed {
			fatal(fmt.Errorf("following LaTeX compile failed: %s", result.Status))
		}
	}
	fmt.Println("P0_LIVE_PID_FAILURE_THEN_REAL_LATEX_SUCCESS")
}

type routes struct {
	backends map[string]*paperssandbox.ScopeBackend
}

func (r routes) Execute(ctx context.Context, j paperssandbox.Job) (paperssandbox.Evidence, error) {
	b := r.backends[j.ID]
	if b == nil {
		return paperssandbox.Evidence{}, paperssandbox.ErrInvalidJob
	}
	return b.Execute(ctx, j)
}
func (r routes) Cleanup(ctx context.Context, j paperssandbox.Job) error {
	b := r.backends[j.ID]
	if b == nil {
		return paperssandbox.ErrInvalidJob
	}
	return b.Cleanup(ctx, j)
}
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "P0_LIVE_FAILURE:", err)
	os.Exit(1)
}
