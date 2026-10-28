package paperssandbox

import (
	"encoding/json"
	"testing"
)

func validOCITestConfig(id, output string, limits Limits) []byte {
	obj := map[string]any{
		"root": map[string]any{"readonly": true, "path": "rootfs"},
		"process": map[string]any{
			"args":            []string{"pdflatex", "-no-shell-escape", "-interaction=nonstopmode", "-halt-on-error", "-output-directory=/output", "main.tex"},
			"cwd":             "/inputs",
			"noNewPrivileges": true,
			"user":            map[string]any{"uid": 65534, "gid": 65534},
			"capabilities": map[string]any{
				"bounding": []string{}, "effective": []string{}, "permitted": []string{},
				"inheritable": []string{}, "ambient": []string{},
			},
			"env": []string{"PATH=/usr/bin", "HOME=/tmp"},
		},
		"linux": map[string]any{
			"cgroupsPath": "system.slice:atomanp0:" + id,
			"namespaces": []any{
				map[string]any{"type": "network"},
				map[string]any{"type": "mount"},
				map[string]any{"type": "pid"},
			},
			"resources": map[string]any{
				"memory": map[string]any{"limit": limits.MemoryMaxBytes, "swap": limits.MemoryMaxBytes},
				"pids":   map[string]any{"limit": limits.PIDsMax},
				"cpu":    map[string]any{"quota": limits.CPUQuotaMicros, "period": limits.CPUPeriodMicros},
			},
		},
		"mounts": []any{
			map[string]any{"destination": "/output", "source": output, "type": "bind", "options": []string{"rw", "nodev", "nosuid"}},
			map[string]any{"destination": "/tmp", "source": "tmpfs", "type": "tmpfs", "options": []string{"nodev", "nosuid", "size=67108864"}},
		},
	}
	data, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return data
}

func TestOCIPolicyAllowsOnlyControlledCompile(t *testing.T) {
	good := validOCITestConfig("job-one", "/tmp/owned-output", testLimits)
	if err := validateOCISpec(good, "job-one", testLimits, "/tmp/owned-output"); err != nil {
		t.Fatal(err)
	}
	if err := validateOCISpec(good, "job-two", testLimits, "/tmp/owned-output"); err == nil {
		t.Fatal("wrong cgroup identity accepted")
	}
}

func TestOCIPolicyRejectsUnsafeInputs(t *testing.T) {
	base := validOCITestConfig("job-one", "/tmp/owned-output", testLimits)
	scenarios := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"uid-root", func(m map[string]any) { m["process"].(map[string]any)["user"] = map[string]any{"uid": 0, "gid": 0} }},
		{"writable-root", func(m map[string]any) { m["root"].(map[string]any)["readonly"] = false }},
		{"shell", func(m map[string]any) {
			m["process"].(map[string]any)["args"] = []string{"/bin/sh", "-c", "pdflatex main.tex"}
		}},
		{"shell-escape", func(m map[string]any) {
			m["process"].(map[string]any)["args"] = []string{"pdflatex", "-shell-escape", "-interaction=nonstopmode", "-halt-on-error", "-output-directory=/output", "main.tex"}
		}},
		{"host-hooks", func(m map[string]any) {
			m["hooks"] = map[string]any{"prestart": []any{map[string]any{"path": "/bin/sh"}}}
		}},
		{"host-rootfs", func(m map[string]any) { m["root"].(map[string]any)["path"] = "/" }},
		{"join-host-namespace", func(m map[string]any) {
			m["linux"].(map[string]any)["namespaces"].([]any)[0].(map[string]any)["path"] = "/proc/1/ns/net"
		}},
		{"unapproved-oci-field", func(m map[string]any) { m["annotations"] = map[string]any{"p0": "test"} }},
		{"extra-capability", func(m map[string]any) {
			m["process"].(map[string]any)["capabilities"].(map[string]any)["effective"] = []string{"CAP_SYS_ADMIN"}
		}},
		{"wrong-memory", func(m map[string]any) {
			m["linux"].(map[string]any)["resources"].(map[string]any)["memory"].(map[string]any)["limit"] = float64(999)
		}},
		{"memory-swap", func(m map[string]any) {
			m["linux"].(map[string]any)["resources"].(map[string]any)["memory"].(map[string]any)["swap"] = float64(999)
		}},
		{"unsafe-env", func(m map[string]any) { m["process"].(map[string]any)["env"] = []string{"LD_PRELOAD=/malicious"} }},
		{"output-outside-root", func(m map[string]any) { m["mounts"].([]any)[0].(map[string]any)["source"] = "/etc" }},
		{"extra-mount", func(m map[string]any) {
			m["mounts"] = append(m["mounts"].([]any), map[string]any{"destination": "/host", "source": "/root", "type": "bind"})
		}},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(base, &m); err != nil {
				t.Fatal(err)
			}
			tc.mutate(m)
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateOCISpec(raw, "job-one", testLimits, "/tmp/owned-output"); err == nil {
				t.Fatalf("unsafe OCI config accepted: %s", tc.name)
			}
		})
	}
}
