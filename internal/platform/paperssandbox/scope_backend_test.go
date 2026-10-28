package paperssandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu          sync.Mutex
	running     map[string]bool
	output      map[string]string
	exitCode    map[string]int
	called      map[string]int
	scopeActive bool
	wrongCgroup bool
}

func (h *fakeHost) command(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.called == nil {
		h.called = map[string]int{}
	}
	h.called[name]++
	switch name {
	case "/mock/systemd-run":
		if len(args) < 5 {
			return nil, errors.New("missing scoped command")
		}
		id := args[len(args)-3]
		h.running[id] = true
		code := h.exitCode[id]
		if code == 0 {
			if err := os.WriteFile(h.output[id], []byte("%PDF-1.7\\nP0 dummy output"), 0600); err != nil {
				return nil, err
			}
		}
		pidMax := 0
		if code == 2 {
			pidMax = 1
		}
		cgroupID := id
		if h.wrongCgroup {
			cgroupID = "unrelated-task"
		}
		raw := fmt.Sprintf(`SENTRY_CGROUP=/sys/fs/cgroup/system.slice/atomanp0-%s.scope
SENTRY_memory.max=201326592
SENTRY_memory.swap.max=0
SENTRY_pids.max=128
SENTRY_cpu.max=50000 100000
SENTRY_MEMORY_EVENTS_EARLY=low 0,high 0,max 0,oom 0,oom_kill 0,
SENTRY_PIDS_EVENTS_SAMPLED=max %d,
CGROUP_RUNSC_EXIT=%d
`, cgroupID, pidMax, code)
		if code != 0 {
			return []byte(raw), fmt.Errorf("mock runsc exit %d", code)
		}
		return []byte(raw), nil
	case "/mock/runsc":
		sub := ""
		for _, arg := range args {
			if arg == "list" || arg == "delete" || arg == "kill" {
				sub = arg
				break
			}
		}
		switch sub {
		case "list":
			var sb strings.Builder
			sb.WriteString("ID PID STATUS\n")
			for id, exists := range h.running {
				if exists {
					sb.WriteString(id + " 42 running\n")
				}
			}
			return []byte(sb.String()), nil
		case "kill":
			return nil, nil
		case "delete":
			id := args[len(args)-1]
			h.running[id] = false
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected runsc args: %v", args)
	case "/mock/systemctl":
		if h.scopeActive {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit status 3")
	default:
		return nil, fmt.Errorf("unrecognized command: %s", name)
	}
}

func makeTestScopeBackend(t *testing.T, host *fakeHost, id string) *ScopeBackend {
	t.Helper()
	root := filepath.Join(t.TempDir(), "p0-"+id)
	for _, dir := range []string{"bundle/rootfs", "state", "output"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(root, "output", "main.pdf")
	bundle := filepath.Join(root, "bundle")
	data := validOCITestConfig(id, filepath.Join(root, "output"), testLimits)
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	host.output[id] = out
	host.mu.Unlock()
	backend, err := NewScopeBackend(ScopeBackendOptions{
		Workspace: root, Bundle: bundle, RunscState: filepath.Join(root, "state"),
		PDFPath: out, RunscBinary: "/mock/runsc", SystemdRunBinary: "/mock/systemd-run",
		ScopeHelper: "/mock/scope-helper", SystemctlBinary: "/mock/systemctl",
		MountpointBinary: "/mock/mountpoint", FindmntBinary: "/mock/findmnt",
		UmountBinary: "/mock/umount", Limits: testLimits, WrapperMemory: "256M",
		WrapperCPUQuota: "50%", WrapperTasksMax: 192,
		ExecuteCommand: host.command,
	})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

type routedBackend struct{ byID map[string]*ScopeBackend }

func (r routedBackend) Execute(ctx context.Context, job Job) (Evidence, error) {
	return r.byID[job.ID].Execute(ctx, job)
}
func (r routedBackend) Cleanup(ctx context.Context, job Job) error {
	return r.byID[job.ID].Cleanup(ctx, job)
}

func TestScopeBackendPIDFailureDoesNotPoisonNextJob(t *testing.T) {
	host := &fakeHost{running: map[string]bool{}, output: map[string]string{}, exitCode: map[string]int{"pid-failure": 2, "next-compile": 0}}
	a := makeTestScopeBackend(t, host, "pid-failure")
	b := makeTestScopeBackend(t, host, "next-compile")
	s := NewSupervisor(routedBackend{byID: map[string]*ScopeBackend{"pid-failure": a, "next-compile": b}})
	first, err := s.Run(context.Background(), Job{ID: "pid-failure", Timeout: time.Second})
	if err != nil || first.Status != PIDsExceeded || !first.CleanupVerified {
		t.Fatalf("pid-exhausted job not classified and cleaned: %+v err=%v", first, err)
	}
	second, err := s.Run(context.Background(), Job{ID: "next-compile", Timeout: time.Second})
	if err != nil || second.Status != Completed || !second.CleanupVerified {
		t.Fatalf("next job not usable: %+v err=%v", second, err)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.running["pid-failure"] || host.running["next-compile"] {
		t.Fatalf("runsc sandbox still running: %+v", host.running)
	}
	if host.called["/mock/systemd-run"] != 2 {
		t.Fatal("expected both tasks to execute")
	}
}

func TestScopeBackendBlocksIfSystemdScopeRemainsActive(t *testing.T) {
	host := &fakeHost{running: map[string]bool{}, output: map[string]string{},
		exitCode: map[string]int{"first": 2}, scopeActive: true}
	b := makeTestScopeBackend(t, host, "first")
	s := NewSupervisor(b)
	r, err := s.Run(context.Background(), Job{ID: "first", Timeout: time.Second})
	if err != nil || r.Status != CleanupFailed || !s.Quarantined() {
		t.Fatalf("active systemd scope must quarantine: %+v %v", r, err)
	}
}

func TestScopeBackendRejectsStalePDFWithoutCommandExecution(t *testing.T) {
	host := &fakeHost{running: map[string]bool{}, output: map[string]string{}, exitCode: map[string]int{"stale": 0}}
	backend := makeTestScopeBackend(t, host, "stale")
	if err := os.WriteFile(host.output["stale"], []byte("%PDF-stale"), 0600); err != nil {
		t.Fatal(err)
	}
	e, err := backend.Execute(context.Background(), Job{ID: "stale", Timeout: time.Second})
	if err == nil || e.LimitsVerified {
		t.Fatalf("stale PDF was accepted: %v %+v", err, e)
	}
	if host.called["/mock/systemd-run"] != 0 {
		t.Fatal("unsafe launch actually executed")
	}
}

func TestScopeBackendBlocksWorkspaceSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bundle", "rootfs"), 0700); err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{}
	_, err := NewScopeBackend(ScopeBackendOptions{
		Workspace: root, Bundle: filepath.Join(root, "bundle"), RunscState: filepath.Join(root, "outside"),
		PDFPath:     filepath.Join(root, "outside", "fake.pdf"),
		RunscBinary: "/mock/runsc", SystemdRunBinary: "/mock/systemd-run",
		ScopeHelper: "/mock/scope-helper", SystemctlBinary: "/mock/systemctl",
		MountpointBinary: "/mock/mountpoint", FindmntBinary: "/mock/findmnt",
		UmountBinary: "/mock/umount", Limits: testLimits, WrapperMemory: "256M",
		WrapperCPUQuota: "50%", WrapperTasksMax: 192, ExecuteCommand: host.command,
	})
	if err == nil {
		t.Fatal("symlink outside workspace accepted")
	}
}

func TestScopeBackendRejectsDifferentSentryCgroup(t *testing.T) {
	host := &fakeHost{
		running: map[string]bool{}, output: map[string]string{},
		exitCode: map[string]int{"wrong-cgroup": 0}, wrongCgroup: true,
	}
	backend := makeTestScopeBackend(t, host, "wrong-cgroup")
	s := NewSupervisor(backend)
	r, err := s.Run(context.Background(), Job{ID: "wrong-cgroup", Timeout: time.Second})
	if err != nil || r.Status != IsolationFailed || !r.CleanupVerified {
		t.Fatalf("wrong job's cgroup was trusted: %+v %v", r, err)
	}
}
