package paperssandbox

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// ParseScopeEvidence consumes only the trusted host-side scope-helper output.
// Never append the sandbox's untrusted LaTeX stdout into this input: an
// untrusted document could forge SENTRY_* lines or fake resource enforcement.
func ParseScopeEvidence(scopeLog string, expected Limits, artifactVerified bool) (Evidence, error) {
	var e Evidence
	if err := expected.Validate(); err != nil {
		return e, err
	}
	if len(scopeLog) > 1<<20 {
		return e, fmt.Errorf("scope diagnostic exceeds 1 MiB")
	}
	e.ArtifactVerified = artifactVerified

	fields := make(map[string]string, 12)
	scanner := bufio.NewScanner(strings.NewReader(scopeLog))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	mismatch := false
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "SENTRY_CGROUP":
			// Exact job ID validation is performed by ScopeBackend.Execute.
			e.SentryCgroup = value
			e.SentryObserved = strings.HasPrefix(value, "/sys/fs/cgroup/system.slice/atomanp0-") &&
				strings.HasSuffix(value, ".scope")
		case "SENTRY_memory.max", "SENTRY_memory.swap.max", "SENTRY_pids.max", "SENTRY_cpu.max":
			fields[key] = value
		case "CGROUP_RUNSC_EXIT":
			exit, err := strconv.Atoi(value)
			if err != nil || exit < 0 || exit > 255 {
				return e, fmt.Errorf("malformed runsc exit status")
			}
			e.ExitCode, e.ExitKnown = exit, true
		case "SENTRY_MEMORY_EVENTS_EARLY", "SENTRY_MEMORY_EVENTS_MID", "SENTRY_MEMORY_EVENTS_AFTER":
			e.Events.MemoryMax = maxInt(e.Events.MemoryMax, findCounter(value, "max"))
			e.Events.MemoryOOM = maxInt(e.Events.MemoryOOM, findCounter(value, "oom"))
			e.Events.MemoryOOMKill = maxInt(e.Events.MemoryOOMKill, findCounter(value, "oom_kill"))
		case "SENTRY_PIDS_EVENTS_EARLY", "SENTRY_PIDS_EVENTS_SAMPLED", "SENTRY_PIDS_EVENTS_AFTER":
			e.Events.PIDsMax = maxInt(e.Events.PIDsMax, findCounter(value, "max"))
		case "SENTRY_CPU_STATS_EARLY", "SENTRY_CPU_STATS_MID", "SENTRY_CPU_STATS_AFTER":
			e.Events.CPUThrottled = maxInt(e.Events.CPUThrottled, findCounter(value, "nr_throttled"))
		case "SENTRY_CGROUP_MISMATCH", "P0_SCOPE_VALIDATION_FAILED":
			mismatch = true
		}
		if strings.Contains(line, "SENTRY_CGROUP_MISMATCH") ||
			strings.Contains(line, "P0_SCOPE_VALIDATION_FAILED") {
			mismatch = true
		}
	}
	if err := scanner.Err(); err != nil {
		return e, err
	}
	e.LimitsVerified = e.SentryObserved && !mismatch &&
		fields["SENTRY_memory.max"] == strconv.FormatInt(expected.MemoryMaxBytes, 10) &&
		fields["SENTRY_memory.swap.max"] == strconv.FormatInt(expected.MemorySwapMaxBytes, 10) &&
		fields["SENTRY_pids.max"] == strconv.FormatInt(expected.PIDsMax, 10) &&
		fields["SENTRY_cpu.max"] == fmt.Sprintf("%d %d", expected.CPUQuotaMicros, expected.CPUPeriodMicros)
	return e, nil
}

func maxInt(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func findCounter(line, name string) int64 {
	for _, part := range strings.Split(line, ",") {
		f := strings.Fields(part)
		if len(f) == 2 && f[0] == name {
			n, err := strconv.ParseInt(f[1], 10, 64)
			if err == nil && n >= 0 {
				return n
			}
		}
	}
	return 0
}
