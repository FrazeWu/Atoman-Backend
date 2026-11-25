//go:build linux

// Package papersruntime composes the production-only Papers service roles.
// P0 harnesses remain isolated and cannot provide production credentials.
package papersruntime

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const (
	PrivateBucket     = "atoman-papers-private-v1"
	SharedBucket      = "atoman"
	StorageEndpoint   = "http://127.0.0.1:9100"
	LauncherSocket    = "/run/atoman-papers/uds/launcher.sock"
	LeaseBridgeSocket = "/run/atoman-papers-queue/lease.sock"
	SnapshotSocket    = "/run/atoman-papers-snapshot/publish.sock"
	PDFSocket         = "/run/atoman-papers-pdf/publish.sock"
	EnvironmentSocket = "/run/atoman-papers-environment/publish.sock"
	WorkRoot          = "/var/lib/atoman-papers/runs"
	ArchiveCache      = "/var/lib/atoman-papers/trusted-cache"
	RunscPath         = "/opt/atoman/papers/bin/runsc"
	ScopeHelper       = "/opt/atoman/papers/bin/scope-exec.sh"
	RunscRelease      = "20261019.0"
)

var ErrConfig = errors.New("papers runtime: invalid or unavailable protected configuration")
var digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var keyIDRE = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

// Distinct credential documents intentionally prohibit fields for other roles.
// Credentials are loaded only through systemd LoadCredential, never flags,
// broad app S3 credentials, API input, or inherited database environment.
type StorageCredential struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}
type TrustConfig struct {
	EnvironmentDigest string `json:"environment_digest"`
	SignerID          string `json:"signer_id"`
	PublicKey         string `json:"public_key"`
	RetentionDays     int64  `json:"retention_days"`
}
type APIConfig struct {
	EnableXeLaTeX  bool              `json:"enable_xelatex,omitempty"`
	EnableLuaLaTeX bool              `json:"enable_lualatex,omitempty"`
	Reader         StorageCredential `json:"reader"`
	Trust          TrustConfig       `json:"trust"`
}
type WorkerConfig struct {
	DatabasePassword string `json:"database_password"`
}
type LauncherConfig struct {
	EnableXeLaTeX  bool              `json:"enable_xelatex,omitempty"`
	EnableLuaLaTeX bool              `json:"enable_lualatex,omitempty"`
	Reader         StorageCredential `json:"reader"`
	Trust          TrustConfig       `json:"trust"`
	RunscDigest    string            `json:"runsc_digest"`
	HelperDigest   string            `json:"helper_digest"`
}
type GatewayConfig struct {
	Writer        StorageCredential `json:"writer"`
	RetentionDays int64             `json:"retention_days"`
}

func (s StorageCredential) validate(role string) error {
	if !strings.HasPrefix(s.AccessKey, "papers-"+role+"-") || len(s.AccessKey) > 64 ||
		len(s.SecretKey) < 32 || len(s.SecretKey) > 256 || strings.TrimSpace(s.SecretKey) != s.SecretKey {
		return ErrConfig
	}
	return nil
}
func (t TrustConfig) roots() (map[string]ed25519.PublicKey, error) {
	key, err := base64.StdEncoding.DecodeString(t.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || !digestRE.MatchString(t.EnvironmentDigest) ||
		!keyIDRE.MatchString(t.SignerID) || t.RetentionDays < 1 || t.RetentionDays > 365 {
		return nil, ErrConfig
	}
	return map[string]ed25519.PublicKey{t.SignerID: ed25519.PublicKey(key)}, nil
}
func validDBPassword(s string) bool {
	return len(s) >= 32 && len(s) <= 256 && strings.TrimSpace(s) == s
}

// systemd LoadCredential grants the service a private read-only tmpfs
// credential mount. Its root:root 0440 files are not ordinary group-readable
// configuration files; only the named service's protected mount is accepted.
func systemdCredential(dir, name string, owner *syscall.Stat_t, mode os.FileMode) bool {
	if owner.Uid != 0 || owner.Gid != 0 || mode.Perm() != 0440 || filepath.Dir(dir) != "/run/credentials" {
		return false
	}
	units := map[string]string{
		"papers-api.json":                 "atoman-backend.service",
		"papers-crdt.json":                "atoman-backend.service",
		"papers-worker.json":              "atoman-papers-worker.service",
		"papers-launcher.json":            "atoman-papers-launcher.service",
		"papers-snapshot-gateway.json":    "atoman-papers-snapshot.service",
		"papers-pdf-gateway.json":         "atoman-papers-pdf.service",
		"papers-environment-gateway.json": "atoman-papers-environment.service",
		"papers-publisher.json":           "atoman-papers-publisher.service",
	}
	if units[name] != filepath.Base(dir) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return false
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return false
	}
	d, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || d.Uid != 0 || d.Gid != 0 || st.Mode().Perm() != 0550 {
		return false
	}
	var fs unix.Statfs_t
	flags := int64(unix.ST_RDONLY | unix.ST_NOSUID | unix.ST_NODEV | unix.ST_NOEXEC)
	return unix.Statfs(dir, &fs) == nil && fs.Type == unix.TMPFS_MAGIC && int64(fs.Flags)&flags == flags
}

func readCredential(dir, name string, out any) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return ErrConfig
	}
	// The service manager supplies a private credential directory. Refuse
	// symlinks, writable-by-others files, special files and oversized JSON.
	st, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		return ErrConfig
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || (st.Mode().Perm()&0077 != 0 && !systemdCredential(dir, name, owner, st.Mode())) ||
		(owner.Uid != 0 && owner.Uid != uint32(os.Geteuid())) || st.Size() < 2 || st.Size() > 16384 {
		return ErrConfig
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return ErrConfig
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return ErrConfig
	}
	dec := json.NewDecoder(io.LimitReader(f, 16385))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return ErrConfig
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return ErrConfig
	}
	return nil
}
func loadCredential(name string, out any) error {
	return readCredential(os.Getenv("CREDENTIALS_DIRECTORY"), name, out)
}

func loadAPI() (APIConfig, error) {
	var c APIConfig
	if err := loadCredential("papers-api.json", &c); err != nil {
		return c, err
	}
	if err := c.Reader.validate("api-reader"); err != nil {
		return c, err
	}
	_, err := c.Trust.roots()
	return c, err
}
func loadWorker() (WorkerConfig, error) {
	var c WorkerConfig
	if err := loadCredential("papers-worker.json", &c); err != nil {
		return c, err
	}
	if !validDBPassword(c.DatabasePassword) {
		return c, ErrConfig
	}
	return c, nil
}
func loadLauncher() (LauncherConfig, error) {
	var c LauncherConfig
	if err := loadCredential("papers-launcher.json", &c); err != nil {
		return c, err
	}
	if !digestRE.MatchString(c.RunscDigest) || !digestRE.MatchString(c.HelperDigest) {
		return c, ErrConfig
	}
	if err := c.Reader.validate("launcher-reader"); err != nil {
		return c, err
	}
	_, err := c.Trust.roots()
	return c, err
}
func loadGateway(role string) (GatewayConfig, error) {
	var c GatewayConfig
	switch role {
	case "snapshot", "pdf", "environment":
	default:
		return c, ErrConfig
	}
	if err := loadCredential("papers-"+role+"-gateway.json", &c); err != nil {
		return c, err
	}
	if err := c.Writer.validate(role + "-writer"); err != nil {
		return c, err
	}
	if c.RetentionDays < 1 || c.RetentionDays > 365 {
		return c, ErrConfig
	}
	return c, nil
}
