package paperssandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// This exact, versioned driver is installed by the trusted host code into
// the read-only rootfs outside /inputs. Only the fixed guest executable
// path below is allowed by the OCI policy. Project files, configuration,
// bibliography data and auxiliary files are never evaluated as shell code.
const compileDriverGuestPath = "/atoman-papers-compile-v2"
const luaCompileDriverGuestPath = "/atoman-papers-lualatex-v1"
const compileDriver = `#!/bin/sh
set -eu
umask 077
export BIBINPUTS=/inputs:
export BSTINPUTS=/inputs:
export TEXINPUTS=/output:/inputs:
export shell_escape=f
export openout_any=p

latex_pass() {
    printf 'AT0MAN_LATEX_PASS=%s\n' "$1"
    cd /inputs
    /usr/bin/pdflatex -synctex=1 -file-line-error -no-shell-escape -interaction=nonstopmode -halt-on-error -output-directory=/output main.tex
}

latex_pass 1
bibliography=0
# Only the generated, fixed main.aux is inspected; it is not sourced.
# BibTeX receives a literal basename and a fixed working directory.
if [ -f /output/main.aux ] && /usr/bin/grep -Fq '\bibdata{' /output/main.aux; then
    printf 'AT0MAN_BIBTEX_PASS=1\n'
    cd /output
    rc=0
    /usr/bin/bibtex main || rc=$?
    # BibTeX exit 1 denotes warnings. Only accept it if a real bibliography
    # was generated; exit >= 2 is an error, not a successful compile.
    if [ "$rc" -gt 1 ] || [ ! -s /output/main.bbl ]; then
        exit 1
    fi
    bibliography=1
fi
latex_pass 2
if [ "$bibliography" -eq 1 ]; then
    latex_pass 3
    pass=3
else
    pass=2
fi
# Bound the entire job to at most four pdfLaTeX invocations, in the same
# sandbox, cgroup, output quota and wall-clock timeout. LaTeX warnings
# cannot trigger an unlimited loop.
while [ "$pass" -lt 4 ] && [ -f /output/main.log ] && /usr/bin/grep -Eq 'Rerun to get (cross-references|outlines) right|Label\(s\) may have changed|Please rerun LaTeX' /output/main.log; do
    pass=$((pass+1))
    latex_pass "$pass"
done
test -s /output/main.pdf
printf 'AT0MAN_LATEX_COMPLETE=%s\n' "$pass"
`

// The only alternate driver is derived from trusted constants, never user
// text. LuaLaTeX font loading requires Lua; --safer is incompatible with
// luaotfload. Keep no-shell-escape and nosocket, and enforce the same outer
// gVisor network, read-only filesystem, bounded output and cgroup policies.
// This driver remains separately disabled unless an operator approves it.
func luaCompileDriver() string {
	driver := strings.ReplaceAll(compileDriver, "/usr/bin/pdflatex ", "/usr/bin/lualatex --nosocket ")
	return strings.Replace(driver, "umask 077\n", "umask 077\nexport TEXMFOUTPUT=/output\nexport TEXMFCACHE=/output/texmf-cache\nexport TEXMFVAR=/output/texmf-var\nexport TEXMFCONFIG=/output/texmf-config\n/usr/bin/mkdir -p /output/texmf-cache /output/texmf-var /output/texmf-config\n", 1)
}
func compileDriverBody(path string) (string, bool) {
	switch path {
	case compileDriverGuestPath:
		return compileDriver, true
	case luaCompileDriverGuestPath:
		return luaCompileDriver(), true
	default:
		return "", false
	}
}
func installCompileDriver(bundle, guestPath string) error {
	body, ok := compileDriverBody(guestPath)
	if !ok {
		return ErrInvalidJob
	}
	target := filepath.Join(bundle, "rootfs", guestPath[1:])
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(0555); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
func isCompileDriverArgs(args []string) bool {
	if len(args) != 1 {
		return false
	}
	_, ok := compileDriverBody(args[0])
	return ok
}

// Verify the exact sealed driver independently immediately before execution.
// The rootfs is private and read-only, and the file must not be a symlink.
func verifyCompileDriverForSpec(bundle string, raw []byte) error {
	var spec struct{ Process struct{ Args []string } }
	if err := json.Unmarshal(raw, &spec); err != nil {
		return err
	}
	if !isCompileDriverArgs(spec.Process.Args) {
		return nil
	}
	bodyExpected, _ := compileDriverBody(spec.Process.Args[0])
	target := filepath.Join(bundle, "rootfs", spec.Process.Args[0][1:])
	st, err := os.Lstat(target)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0555 || st.Size() != int64(len(bodyExpected)) {
		return fmt.Errorf("%w: unsealed compile driver", ErrInvalidJob)
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: invalid compile driver owner", ErrInvalidJob)
	}
	f, err := os.Open(target)
	if err != nil {
		return err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return ErrInvalidJob
	}
	body, err := io.ReadAll(io.LimitReader(f, int64(len(bodyExpected)+1)))
	if err != nil || !bytes.Equal(body, []byte(bodyExpected)) {
		return fmt.Errorf("%w: compile driver content mismatch", ErrInvalidJob)
	}
	return nil
}
