package paperssandbox

import (
	"strings"
	"testing"
)

var testLimits = Limits{
	MemoryMaxBytes:     201326592,
	MemorySwapMaxBytes: 0,
	PIDsMax:            128,
	CPUQuotaMicros:     50000,
	CPUPeriodMicros:    100000,
}

const sampleScope = `
WRAPPER_CGROUP_PATH=/system.slice/atoman-papers-p0-1.scope
WRAPPER_memory.max=268435456
SENTRY_CGROUP=/sys/fs/cgroup/system.slice/atomanp0-paper-run.scope
SENTRY_memory.max=201326592
SENTRY_memory.swap.max=0
SENTRY_pids.max=128
SENTRY_cpu.max=50000 100000
SENTRY_MEMORY_EVENTS_EARLY=low 0,high 0,max 0,oom 0,oom_kill 0,
SENTRY_PIDS_EVENTS_EARLY=max 0,
SENTRY_CPU_STATS_EARLY=usage_usec 1,nr_periods 1,nr_throttled 0,
SENTRY_PIDS_EVENTS_SAMPLED=max 1,
SENTRY_MEMORY_EVENTS_MID=low 0,high 0,max 0,oom 0,oom_kill 0,
SENTRY_CPU_STATS_MID=usage_usec 20,nr_periods 3,nr_throttled 2,
CGROUP_RUNSC_EXIT=2
`

func TestParseScopePIDAndExit2(t *testing.T) {
	evidence, err := ParseScopeEvidence(sampleScope, testLimits, false)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.SentryObserved || !evidence.LimitsVerified || !evidence.ExitKnown || evidence.ExitCode != 2 {
		t.Fatalf("scope evidence is incomplete: %+v", evidence)
	}
	if evidence.Events.PIDsMax != 1 || evidence.Events.CPUThrottled != 2 {
		t.Fatalf("metrics lost: %+v", evidence.Events)
	}
	if got := Classify(evidence, nil); got != PIDsExceeded {
		t.Fatalf("exit 2 after pids.max must be PID resource failure, got %s", got)
	}
}

func TestParseScopeMemoryAndTimeoutEvidence(t *testing.T) {
	raw := strings.Replace(sampleScope, "SENTRY_PIDS_EVENTS_SAMPLED=max 1", "SENTRY_PIDS_EVENTS_SAMPLED=max 0", 1)
	raw = strings.Replace(raw, "max 0,oom 0,oom_kill 0", "max 28,oom 1,oom_kill 0", 1)
	raw = strings.Replace(raw, "CGROUP_RUNSC_EXIT=2", "CGROUP_RUNSC_EXIT=137", 1)
	evidence, err := ParseScopeEvidence(raw, testLimits, false)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Events.MemoryOOM != 1 || evidence.Events.MemoryMax != 28 {
		t.Fatalf("lost OOM data: %+v", evidence)
	}
	if got := Classify(evidence, nil); got != MemoryExceeded {
		t.Fatalf("OOM: got %s", got)
	}
}

func TestFailClosedOnMissingOrIncorrectLimits(t *testing.T) {
	scenarios := []string{
		strings.Replace(sampleScope, "SENTRY_memory.swap.max=0", "SENTRY_memory.swap.max=max", 1),
		strings.Replace(sampleScope, "SENTRY_pids.max=128", "SENTRY_pids.max=256", 1),
		strings.Replace(sampleScope, "SENTRY_cpu.max=50000 100000", "SENTRY_cpu.max=100000 100000", 1),
		strings.Replace(sampleScope, "SENTRY_CGROUP=/sys/fs/cgroup/system.slice/atomanp0-paper-run.scope", "SENTRY_CGROUP=/sys/fs/cgroup/system.slice/untrusted.scope", 1),
		sampleScope + "\nP0_SCOPE_VALIDATION_FAILED=1\n",
	}
	for i, raw := range scenarios {
		evidence, err := ParseScopeEvidence(raw, testLimits, true)
		if err != nil {
			t.Fatalf("scenario %d error: %v", i, err)
		}
		if evidence.LimitsVerified || Classify(evidence, nil) != IsolationFailed {
			t.Fatalf("scenario %d was incorrectly trusted: %+v", i, evidence)
		}
	}
}

func TestRejectOversizeOrMalformedTelemetry(t *testing.T) {
	if _, err := ParseScopeEvidence(strings.Repeat("a", 1<<20+1), testLimits, false); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := ParseScopeEvidence(sampleScope+"\nCGROUP_RUNSC_EXIT=invalid", testLimits, false); err == nil {
		t.Fatal("invalid exit accepted")
	}
}

func TestNoFalseOOMWithoutKernelEvidence(t *testing.T) {
	raw := strings.Replace(sampleScope, "SENTRY_PIDS_EVENTS_SAMPLED=max 1", "SENTRY_PIDS_EVENTS_SAMPLED=max 0", 1)
	raw = strings.Replace(raw, "CGROUP_RUNSC_EXIT=2", "CGROUP_RUNSC_EXIT=137", 1)
	e, err := ParseScopeEvidence(raw, testLimits, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := Classify(e, nil); got != RuntimeFailed {
		t.Fatalf("exit 137 without oom evidence is not proven oom: %s", got)
	}
}
