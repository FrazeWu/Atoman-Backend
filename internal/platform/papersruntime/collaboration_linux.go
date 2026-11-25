//go:build linux

package papersruntime

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"atoman/internal/modules/papers"
	"atoman/internal/platform/paperscrdt"
)

const collaborationSocket = "/run/atoman-papers-crdt.sock"
const collaborationCredential = "papers-crdt.json"

type collaborationApproval struct {
	Approved bool   `json:"approved"`
	Protocol string `json:"protocol"`
}

func readCollaborationApproval(dir string) error {
	var approval collaborationApproval
	if err := readCredential(dir, collaborationCredential, &approval); err != nil {
		return ErrConfig
	}
	if !approval.Approved || approval.Protocol != "papers-crdt-v1" {
		return ErrConfig
	}
	return nil
}

func configureApprovedCollaboration(ctx context.Context, svc *papers.Service) error {
	if os.Getenv("PAPERS_COLLABORATION_ENABLE") != "1" {
		return nil
	}
	if ctx == nil || svc == nil || os.Geteuid() == 0 {
		return ErrConfig
	}
	if _, _, err := serviceIdentity("atoman", "atoman"); err != nil {
		return ErrConfig
	}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	// Unlike test fixture credentials, the production approval must be
	// delivered from the protected, root-owned systemd credential mount.
	st, err := os.Lstat(filepath.Join(dir, collaborationCredential))
	if err != nil || !st.Mode().IsRegular() {
		return ErrConfig
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !systemdCredential(dir, collaborationCredential, owner, st.Mode()) {
		return ErrConfig
	}
	if err = readCollaborationApproval(dir); err != nil {
		return ErrConfig
	}
	client, err := paperscrdt.NewUnixClient(collaborationSocket)
	if err != nil {
		return ErrConfig
	}
	probe, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	if err = svc.EnableProductionCollaboration(probe, client); err != nil {
		return ErrConfig
	}
	return nil
}
