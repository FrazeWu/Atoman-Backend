package paperssandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// validateOCISpec is a deliberately narrow P0 profile, NOT a general-purpose
// OCI policy language. Future TeX engines require explicit policy updates.
func validateOCISpec(raw []byte, jobID string, limits Limits, outputDir string, allowP0PIDProbe ...bool) error {
	type caps struct {
		Bounding    []string
		Effective   []string
		Permitted   []string
		Inheritable []string
		Ambient     []string
	}
	var spec struct {
		OCIVersion string
		Hostname   string
		Hooks      json.RawMessage
		Root       struct {
			Path     string
			Readonly bool
		}
		Process struct {
			Args            []string
			Cwd             string
			NoNewPrivileges bool
			User            struct {
				UID            int
				GID            int
				AdditionalGids []int
			}
			Capabilities *caps
			Env          []string
		}
		Linux struct {
			CgroupsPath string
			Namespaces  []struct {
				Type string
				Path string
			}
			Devices   []json.RawMessage
			Resources struct {
				Memory struct {
					Limit int64
					Swap  int64
				}
				Pids struct{ Limit int64 }
				CPU  struct {
					Quota  int64
					Period int64
				}
			}
		}
		Mounts []struct {
			Destination string
			Source      string
			Type        string
			Options     []string
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return fmt.Errorf("%w: invalid or unapproved OCI JSON: %v", ErrInvalidJob, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: trailing OCI data or decode failure", ErrInvalidJob)
	}
	if len(spec.Hooks) > 0 && string(spec.Hooks) != "null" {
		return fmt.Errorf("%w: OCI host hooks forbidden", ErrInvalidJob)
	}
	if !spec.Root.Readonly || spec.Root.Path != "rootfs" || !spec.Process.NoNewPrivileges ||
		spec.Process.User.UID != 65534 || spec.Process.User.GID != 65534 ||
		len(spec.Process.User.AdditionalGids) > 0 ||
		spec.Process.Cwd != "/inputs" || spec.Process.Capabilities == nil {
		return fmt.Errorf("%w: OCI privilege profile not approved", ErrInvalidJob)
	}
	c := spec.Process.Capabilities
	if len(c.Bounding)+len(c.Effective)+len(c.Permitted)+len(c.Inheritable)+len(c.Ambient) != 0 {
		return fmt.Errorf("%w: OCI capabilities not empty", ErrInvalidJob)
	}
	if len(spec.Linux.Devices) > 0 {
		return fmt.Errorf("%w: OCI custom devices forbidden", ErrInvalidJob)
	}
	seenNamespaces := make(map[string]bool)
	for _, ns := range spec.Linux.Namespaces {
		if ns.Path != "" || seenNamespaces[ns.Type] {
			return fmt.Errorf("%w: namespace joining or duplication forbidden", ErrInvalidJob)
		}
		seenNamespaces[ns.Type] = true
	}
	if !seenNamespaces["network"] || !seenNamespaces["mount"] || !seenNamespaces["pid"] {
		return fmt.Errorf("%w: mandatory namespaces missing", ErrInvalidJob)
	}
	if spec.Linux.CgroupsPath != "system.slice:atomanp0:"+jobID {
		return fmt.Errorf("%w: OCI cgroup identity mismatch", ErrInvalidJob)
	}
	if spec.Linux.Resources.Memory.Limit != limits.MemoryMaxBytes ||
		spec.Linux.Resources.Memory.Swap != limits.MemoryMaxBytes ||
		spec.Linux.Resources.Pids.Limit != limits.PIDsMax ||
		spec.Linux.Resources.CPU.Quota != limits.CPUQuotaMicros ||
		spec.Linux.Resources.CPU.Period != limits.CPUPeriodMicros {
		return fmt.Errorf("%w: OCI resource limits differ from approved spec", ErrInvalidJob)
	}
	args := spec.Process.Args
	if len(args) == 2 && args[0] == "/inputs/p0-sandbox-probe" && args[1] == "pids" &&
		len(allowP0PIDProbe) > 0 && allowP0PIDProbe[0] {
		// Explicitly controlled diagnostic only, not available to Papers API.
	} else if isCompileDriverArgs(args) {
		// Only this literal, sealed driver path is permitted. ScopeBackend
		// verifies its exact trusted bytes before starting the runtime.
	} else if len(args) != 6 || args[0] != "pdflatex" ||
		args[1] != "-no-shell-escape" || args[2] != "-interaction=nonstopmode" ||
		args[3] != "-halt-on-error" || args[4] != "-output-directory=/output" ||
		args[5] != "main.tex" {
		return fmt.Errorf("%w: executable, args, or shell policy not approved", ErrInvalidJob)
	}
	for _, v := range spec.Process.Env {
		key, _, ok := strings.Cut(v, "=")
		if !ok || !map[string]bool{
			"PATH": true, "HOME": true, "LANG": true, "TEXMFVAR": true,
			"TEXMFCONFIG": true, "TEXMFCACHE": true,
		}[key] {
			return fmt.Errorf("%w: unapproved environment variable", ErrInvalidJob)
		}
	}
	approvedMounts := map[string]string{
		"/proc":       "proc",
		"/dev":        "tmpfs",
		"/dev/pts":    "devpts",
		"/dev/shm":    "tmpfs",
		"/dev/mqueue": "mqueue",
		"/sys":        "sysfs",
		"/tmp":        "tmpfs",
		"/output":     "bind",
	}
	outputMount := false
	if len(spec.Mounts) > 20 {
		return fmt.Errorf("%w: too many mounts", ErrInvalidJob)
	}
	for _, m := range spec.Mounts {
		if approvedMounts[m.Destination] != m.Type {
			return fmt.Errorf("%w: unapproved mount %s", ErrInvalidJob, m.Destination)
		}
		if m.Destination == "/output" {
			if filepath.Clean(m.Source) != filepath.Clean(outputDir) ||
				!hasAllMountOptions(m.Options, "rw", "nodev", "nosuid") {
				return fmt.Errorf("%w: unexpected writable output bind", ErrInvalidJob)
			}
			outputMount = true
		}
		if m.Destination == "/sys" && !hasAllMountOptions(m.Options, "ro") {
			return fmt.Errorf("%w: writable sysfs", ErrInvalidJob)
		}
		if m.Destination == "/tmp" {
			if !hasAllMountOptions(m.Options, "nodev", "nosuid", "size=67108864") {
				return fmt.Errorf("%w: unbounded scratch tmpfs", ErrInvalidJob)
			}
		}
	}
	if !outputMount {
		return fmt.Errorf("%w: no approved writable output", ErrInvalidJob)
	}
	return nil
}

func hasAllMountOptions(have []string, required ...string) bool {
	for _, want := range required {
		found := false
		for _, v := range have {
			if v == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
