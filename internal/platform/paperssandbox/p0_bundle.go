package paperssandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// P0Mode is intentionally not a production API option. It is used only by the
// offline integration harness with a pinned image and fixed trusted inputs.
type P0Mode string

const (
	P0Latex       P0Mode = "latex"
	P0PIDPressure P0Mode = "pids"
)

// WriteP0BundleConfig generates a narrowly approved OCI specification.
// The caller must first stage a pinned verified image under bundle/rootfs;
// this function never pulls images, invokes Docker, or executes user commands.
func WriteP0BundleConfig(bundle, outputDir, jobID string, limits Limits, mode P0Mode) error {
	job := Job{ID: jobID}
	if !jobIDPattern.MatchString(job.ID) {
		return ErrInvalidJob
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(bundle) || !filepath.IsAbs(outputDir) {
		return ErrInvalidJob
	}
	for _, dir := range []string{bundle, filepath.Join(bundle, "rootfs"), outputDir} {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: P0 directory is not a real directory: %s", ErrInvalidJob, dir)
		}
	}
	var args []string
	switch mode {
	case P0Latex:
		args = []string{"pdflatex", "-no-shell-escape", "-interaction=nonstopmode", "-halt-on-error", "-output-directory=/output", "main.tex"}
		if _, err := os.Stat(filepath.Join(bundle, "rootfs", "inputs", "main.tex")); err != nil {
			return err
		}
	case P0PIDPressure:
		args = []string{"/inputs/p0-sandbox-probe", "pids"}
		if _, err := os.Stat(filepath.Join(bundle, "rootfs", "inputs", "p0-sandbox-probe")); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: invalid P0 mode", ErrInvalidJob)
	}
	obj := map[string]any{
		"ociVersion": "1.0.2",
		"root":       map[string]any{"path": "rootfs", "readonly": true},
		"process": map[string]any{
			"args": args, "cwd": "/inputs",
			"user":            map[string]any{"uid": 65534, "gid": 65534},
			"noNewPrivileges": true,
			"capabilities": map[string]any{
				"bounding": []string{}, "effective": []string{},
				"inheritable": []string{}, "permitted": []string{}, "ambient": []string{},
			},
			"env": []string{
				"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				"HOME=/tmp", "LANG=C.UTF-8",
				"TEXMFVAR=/tmp/texmf-var", "TEXMFCONFIG=/tmp/texmf-config",
				"TEXMFCACHE=/tmp/texmf-cache",
			},
		},
		"linux": map[string]any{
			"cgroupsPath": "system.slice:atomanp0:" + jobID,
			"namespaces": []any{
				map[string]any{"type": "mount"},
				map[string]any{"type": "pid"},
				map[string]any{"type": "network"},
				map[string]any{"type": "ipc"},
				map[string]any{"type": "uts"},
			},
			"resources": map[string]any{
				"memory": map[string]any{"limit": limits.MemoryMaxBytes, "swap": limits.MemoryMaxBytes},
				"pids":   map[string]any{"limit": limits.PIDsMax},
				"cpu":    map[string]any{"quota": limits.CPUQuotaMicros, "period": limits.CPUPeriodMicros},
			},
		},
		"mounts": []any{
			map[string]any{"destination": "/proc", "type": "proc", "source": "proc", "options": []string{"nosuid", "noexec", "nodev"}},
			map[string]any{"destination": "/dev", "type": "tmpfs", "source": "tmpfs", "options": []string{"nosuid", "mode=755", "size=16777216"}},
			map[string]any{"destination": "/dev/pts", "type": "devpts", "source": "devpts", "options": []string{"nosuid", "noexec"}},
			map[string]any{"destination": "/sys", "type": "sysfs", "source": "sysfs", "options": []string{"ro", "nosuid", "noexec", "nodev"}},
			map[string]any{"destination": "/tmp", "type": "tmpfs", "source": "tmpfs", "options": []string{"nosuid", "nodev", "mode=1777", "size=67108864"}},
			map[string]any{"destination": "/output", "type": "bind", "source": outputDir, "options": []string{"rbind", "rw", "nosuid", "nodev", "noexec"}},
		},
	}
	raw, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	if err := validateOCISpec(raw, jobID, limits, outputDir, mode == P0PIDPressure); err != nil {
		return err
	}
	config := filepath.Join(bundle, "config.json")
	f, err := os.OpenFile(config, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
