package paperssandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeEntry struct {
	evidence   Evidence
	runErr     error
	cleanupErr error
}
type fakeBackend struct {
	mu       sync.Mutex
	entries  []fakeEntry
	next     int
	cleanups []string
	block    chan struct{}
	entered  chan struct{}
}

func (f *fakeBackend) Execute(ctx context.Context, job Job) (Evidence, error) {
	f.mu.Lock()
	idx := f.next
	f.next++
	var entry fakeEntry
	if idx < len(f.entries) {
		entry = f.entries[idx]
	}
	block := f.block
	entered := f.entered
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return Evidence{SentryObserved: true, LimitsVerified: true, ExitKnown: false}, ctx.Err()
		}
	}
	return entry.evidence, entry.runErr
}
func (f *fakeBackend) Cleanup(ctx context.Context, job Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanups = append(f.cleanups, job.ID)
	idx := len(f.cleanups) - 1
	if idx < len(f.entries) {
		return f.entries[idx].cleanupErr
	}
	return nil
}
func trusted(exit int, pidEvents, oomEvents int64, artifact bool) Evidence {
	return Evidence{
		SentryObserved: true, LimitsVerified: true, ExitKnown: true, ExitCode: exit,
		Events:           Events{PIDsMax: pidEvents, MemoryOOM: oomEvents},
		ArtifactVerified: artifact,
	}
}
func TestSupervisorRecoversAfterPIDCrash(t *testing.T) {
	f := &fakeBackend{entries: []fakeEntry{
		{evidence: trusted(2, 1, 0, false), runErr: errors.New("runsc exit status 2")},
		{evidence: trusted(0, 0, 0, true)},
	}}
	s := NewSupervisor(f)
	first, err := s.Run(context.Background(), Job{ID: "first-job", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != PIDsExceeded || !first.CleanupVerified {
		t.Fatalf("first task should be classified and cleaned: %+v", first)
	}
	second, err := s.Run(context.Background(), Job{ID: "next-job", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != Completed || !second.CleanupVerified {
		t.Fatalf("next job should be allowed and complete: %+v", second)
	}
	if len(f.cleanups) != 2 || f.cleanups[0] != "first-job" || f.cleanups[1] != "next-job" {
		t.Fatalf("cleanup calls: %+v", f.cleanups)
	}
}

func TestSupervisorQuarantinesOnFailedCleanup(t *testing.T) {
	f := &fakeBackend{entries: []fakeEntry{{evidence: trusted(2, 1, 0, false), cleanupErr: errors.New("sandbox still listed")}}}
	s := NewSupervisor(f)
	r, err := s.Run(context.Background(), Job{ID: "orphan-job", Timeout: time.Second})
	if err != nil || r.Status != CleanupFailed || r.UnderlyingStatus != PIDsExceeded || !s.Quarantined() {
		t.Fatalf("must quarantine after incomplete cleanup: %+v %v", r, err)
	}
	if _, err := s.Run(context.Background(), Job{ID: "unsafe-next", Timeout: time.Second}); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("should not schedule after orphan: %v", err)
	}
	if f.next != 1 {
		t.Fatal("backend was called again after quarantine")
	}
}

func TestSupervisorCannotRunConcurrentHeavyJobs(t *testing.T) {
	f := &fakeBackend{entries: []fakeEntry{{evidence: trusted(0, 0, 0, true)}}, block: make(chan struct{}), entered: make(chan struct{}, 1)}
	s := NewSupervisor(f)
	done := make(chan error, 1)
	go func() { _, err := s.Run(context.Background(), Job{ID: "slow-job", Timeout: time.Second}); done <- err }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("job not started")
	}
	if _, err := s.Run(context.Background(), Job{ID: "second-job", Timeout: time.Second}); !errors.Is(err, ErrBusy) {
		t.Fatalf("simultaneous heavy jobs should fail: %v", err)
	}
	close(f.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorAlwaysCleansUpAfterDeadlineAndCancel(t *testing.T) {
	for _, variant := range []struct {
		name   string
		cancel bool
		want   Status
	}{
		{"deadline", false, TimedOut}, {"cancel", true, Canceled},
	} {
		t.Run(variant.name, func(t *testing.T) {
			f := &fakeBackend{block: make(chan struct{})}
			s := NewSupervisor(f)
			ctx := context.Background()
			job := Job{ID: "cancel-check", Timeout: 20 * time.Millisecond}
			if variant.cancel {
				c, cancel := context.WithCancel(ctx)
				ctx = c
				cancel()
			}
			r, err := s.Run(ctx, job)
			if err != nil || r.Status != variant.want || !r.CleanupVerified {
				t.Fatalf("expected %s with cleanup, got %+v, err %v", variant.want, r, err)
			}
			if len(f.cleanups) != 1 {
				t.Fatal("cleanup skipped")
			}
		})
	}
}

func TestSupervisorFailClosedOnUnverifiedLimitsOrMissingPDF(t *testing.T) {
	f := &fakeBackend{entries: []fakeEntry{
		{evidence: Evidence{SentryObserved: true, ExitKnown: true, ExitCode: 0, ArtifactVerified: true}},
		{evidence: trusted(0, 0, 0, false)},
	}}
	s := NewSupervisor(f)
	r, err := s.Run(context.Background(), Job{ID: "unverified", Timeout: time.Second})
	if err != nil || r.Status != IsolationFailed {
		t.Fatalf("unverified run should fail: %+v %v", r, err)
	}
	r, err = s.Run(context.Background(), Job{ID: "no-pdf", Timeout: time.Second})
	if err != nil || r.Status != MissingArtifact {
		t.Fatalf("missing PDF should fail: %+v %v", r, err)
	}
}

func TestValidateJobsAndLimits(t *testing.T) {
	for _, id := range []string{"", "../escape", "UPPER", "foo;rm", "-bad"} {
		if (Job{ID: id, Timeout: time.Second}).Validate() == nil {
			t.Fatalf("id allowed: %q", id)
		}
	}
	for _, bad := range []Limits{
		{MemoryMaxBytes: 192 << 20, MemorySwapMaxBytes: -1, PIDsMax: 128, CPUQuotaMicros: 50000, CPUPeriodMicros: 100000},
		{MemoryMaxBytes: 192 << 20, MemorySwapMaxBytes: 0, PIDsMax: 1, CPUQuotaMicros: 50000, CPUPeriodMicros: 100000},
	} {
		if bad.Validate() == nil {
			t.Fatalf("unsafe limits accepted: %+v", bad)
		}
	}
}

func TestSupervisorRecoversOrphanBeforeAcceptingNextJob(t *testing.T) {
	f := &fakeBackend{entries: []fakeEntry{{evidence: trusted(0, 0, 0, true)}}}
	s := NewSupervisor(f)
	err := s.RecoverOrphans(context.Background(), []Job{{ID: "abandoned-one", Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.cleanups) != 1 || f.cleanups[0] != "abandoned-one" {
		t.Fatalf("orphan cleanup not called: %+v", f.cleanups)
	}
	r, err := s.Run(context.Background(), Job{ID: "next-compile", Timeout: time.Second})
	if err != nil || r.Status != Completed {
		t.Fatalf("next task after orphan cleanup failed: %+v %v", r, err)
	}
}

func TestSupervisorQuarantinesIfOrphanCannotBeCleaned(t *testing.T) {
	f := &fakeBackend{entries: []fakeEntry{{cleanupErr: errors.New("stale sandbox still active")}}}
	s := NewSupervisor(f)
	if err := s.RecoverOrphans(context.Background(), []Job{{ID: "abandoned-one", Timeout: time.Second}}); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("orphan cleanup failure not quarantined: %v", err)
	}
	if _, err := s.Run(context.Background(), Job{ID: "next-compile", Timeout: time.Second}); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("next job should be blocked: %v", err)
	}
}

func TestInterruptedOrphanRecoveryQuarantines(t *testing.T) {
	f := &fakeBackend{}
	s := NewSupervisor(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.RecoverOrphans(ctx, []Job{{ID: "unknown-orphan", Timeout: time.Second}})
	if !errors.Is(err, ErrQuarantined) || !s.Quarantined() {
		t.Fatalf("canceled recovery must block future tasks: %v", err)
	}
}

func TestRuntimeExitOneNeedsVerifiedCompilerError(t *testing.T) {
	evidence := trusted(1, 0, 0, false)
	if got := Classify(evidence, errors.New("runsc exit 1")); got != RuntimeFailed {
		t.Fatalf("exit 1 alone is not proof of LaTeX failure: %s", got)
	}
	evidence.CompileDiagnosticVerified = true
	if got := Classify(evidence, errors.New("runsc exit 1")); got != CompileFailed {
		t.Fatalf("verified LaTeX diagnostic should classify: %s", got)
	}
}
