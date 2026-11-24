package paperssandbox

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// This package is a P0-only, testable control plane. It is not wired to HTTP,
// the production database, or a privileged OS runtime.
type Status string

const (
	Completed       Status = "completed"
	CompileFailed   Status = "compile_failed"
	MissingArtifact Status = "artifact_missing"
	PIDsExceeded    Status = "resource_exhausted_pids"
	MemoryExceeded  Status = "resource_exhausted_memory"
	ResourceUnknown Status = "resource_exhausted_ambiguous"
	TimedOut        Status = "timed_out"
	Canceled        Status = "canceled"
	IsolationFailed Status = "isolation_failed"
	RuntimeFailed   Status = "runtime_failed"
	CleanupFailed   Status = "cleanup_failed"
)

var (
	ErrBusy        = errors.New("papers sandbox: global heavy-job slot is busy")
	ErrQuarantined = errors.New("papers sandbox: cleanup was not verified; supervisor quarantined")
	ErrInvalidJob  = errors.New("papers sandbox: invalid job")
	jobIDPattern   = regexp.MustCompile("^[a-z][a-z0-9-]{0,62}$")
)

type Limits struct {
	MemoryMaxBytes     int64
	MemorySwapMaxBytes int64 // cgroup v2 memory.swap.max: must be zero
	PIDsMax            int64
	CPUQuotaMicros     int64
	CPUPeriodMicros    int64
}

func (v Limits) Validate() error {
	if v.MemoryMaxBytes < 64<<20 || v.MemorySwapMaxBytes != 0 ||
		v.PIDsMax < 32 || v.CPUQuotaMicros < 1 || v.CPUPeriodMicros < 1 ||
		v.CPUQuotaMicros > v.CPUPeriodMicros*4 {
		return fmt.Errorf("%w: unsafe or incomplete resource limits", ErrInvalidJob)
	}
	return nil
}

type Events struct {
	PIDsMax       int64
	MemoryMax     int64
	MemoryOOM     int64
	MemoryOOMKill int64
	CPUThrottled  int64
}

type Evidence struct {
	ExitCode                  int
	ExitKnown                 bool
	SentryObserved            bool
	SentryCgroup              string
	LimitsVerified            bool
	ArtifactVerified          bool
	CompileDiagnosticVerified bool
	DeadlineReached           bool
	ContextCanceled           bool
	Events                    Events
}

type Job struct {
	ID      string
	Timeout time.Duration
}

func (j Job) Validate() error {
	if !jobIDPattern.MatchString(j.ID) || j.Timeout < 10*time.Millisecond ||
		j.Timeout > 5*time.Minute {
		return fmt.Errorf("%w: id or timeout rejected", ErrInvalidJob)
	}
	return nil
}

type Result struct {
	JobID            string
	Status           Status
	UnderlyingStatus Status
	ExitCode         int
	ExitKnown        bool
	SentryObserved   bool
	LimitsVerified   bool
	ArtifactVerified bool
	CleanupVerified  bool
	PDFDigest        string
	PDFSize          int64
	Events           Events
	StartedAt        time.Time
	FinishedAt       time.Time
	FailureDetail    string
}

// Classify deliberately treats an unexpected runsc exit as an error even if
// the TeX log contains success. Cgroup metrics outrank runsc's exit code:
// gVisor can exit 2 after a real pids.max event, not 137 or EAGAIN.
func Classify(e Evidence, runErr error) Status {
	if !e.SentryObserved || !e.LimitsVerified {
		return IsolationFailed
	}
	if e.Events.PIDsMax > 0 && (e.Events.MemoryOOM > 0 || e.Events.MemoryOOMKill > 0) {
		return ResourceUnknown
	}
	if e.Events.MemoryOOM > 0 || e.Events.MemoryOOMKill > 0 {
		return MemoryExceeded
	}
	if e.Events.PIDsMax > 0 {
		return PIDsExceeded
	}
	if e.ContextCanceled {
		return Canceled
	}
	if e.DeadlineReached {
		return TimedOut
	}
	if !e.ExitKnown {
		return RuntimeFailed
	}
	if e.ExitCode == 0 && runErr == nil {
		if e.ArtifactVerified {
			return Completed
		}
		return MissingArtifact
	}
	if e.ExitCode == 1 && e.CompileDiagnosticVerified {
		return CompileFailed
	}
	return RuntimeFailed
}
