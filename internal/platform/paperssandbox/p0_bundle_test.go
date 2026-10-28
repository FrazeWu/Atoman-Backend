package paperssandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func p0TestDirectories(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	output := filepath.Join(root, "output")
	for _, dir := range []string{filepath.Join(bundle, "rootfs", "inputs"), output} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"main.tex", "p0-sandbox-probe"} {
		if err := os.WriteFile(filepath.Join(bundle, "rootfs", "inputs", f), []byte("P0 fixed sample"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return bundle, output
}

func TestWriteP0BundleConfigAndValidate(t *testing.T) {
	for _, mode := range []P0Mode{P0Latex, P0PIDPressure} {
		t.Run(string(mode), func(t *testing.T) {
			bundle, output := p0TestDirectories(t)
			id := "p0-test-" + string(mode)
			if err := WriteP0BundleConfig(bundle, output, id, testLimits, mode); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(bundle, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := validateOCISpec(raw, id, testLimits, output, mode == P0PIDPressure); err != nil {
				t.Fatalf("generated OCI failed its own narrow policy: %v", err)
			}
			if mode == P0PIDPressure {
				if err := validateOCISpec(raw, id, testLimits, output); err == nil {
					t.Fatal("trusted P0 PID probe permitted by ordinary compile policy")
				}
			}
			var spec map[string]any
			if err := json.Unmarshal(raw, &spec); err != nil {
				t.Fatal(err)
			}
			root := spec["root"].(map[string]any)
			if root["readonly"] != true || root["path"] != "rootfs" {
				t.Fatal("rootfs must be immutable and relative")
			}
			if err := WriteP0BundleConfig(bundle, output, id, testLimits, mode); err == nil {
				t.Fatal("overwrote sealed OCI configuration")
			}
		})
	}
}

func TestP0BundleRejectsUnsafeLocationsAndModes(t *testing.T) {
	bundle, output := p0TestDirectories(t)
	for _, mode := range []P0Mode{"", "malicious", "/bin/sh"} {
		if err := WriteP0BundleConfig(bundle, output, "p0-test", testLimits, mode); err == nil {
			t.Fatalf("accepted invalid mode %q", mode)
		}
	}
	if err := WriteP0BundleConfig(bundle, output, "../p0-escape", testLimits, P0Latex); err == nil {
		t.Fatal("accepted path traversal as job id")
	}
	outside := t.TempDir()
	link := filepath.Join(filepath.Dir(bundle), "linked-output")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteP0BundleConfig(bundle, link, "p0-test", testLimits, P0Latex); err == nil {
		t.Fatal("accepted symlink output directory")
	}
}
