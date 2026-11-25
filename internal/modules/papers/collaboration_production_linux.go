//go:build linux

package papers

import (
	"bytes"
	"context"

	"atoman/internal/platform/paperscrdt"
)

// EnableProductionCollaboration is called only during application composition,
// before the router is registered. Only a root-authenticated Unix runtime is
// supported in production, never a local decoder process or HTTP service.
func (s *Service) EnableProductionCollaboration(ctx context.Context, client *paperscrdt.UnixClient) error {
	if s == nil || s.db == nil || s.db.Dialector.Name() != "postgres" || s.collaboration != nil || ctx == nil || client == nil {
		return paperscrdt.ErrUnavailable
	}
	// This is a read-only startup probe, not a migration. Require the exact
	// columns used by collaboration before advertising the capability.
	for _, query := range []string{
		"SELECT project_id,user_id,role,created_at,updated_at FROM paper_project_members WHERE false",
		"SELECT file_id,project_id,epoch,sequence,file_revision,file_sha256,path,state,state_vector,state_sha256,vector_sha256,created_at,updated_at FROM paper_collaborative_documents WHERE false",
	} {
		rows, err := s.db.WithContext(ctx).Raw(query).Rows()
		if err != nil {
			return paperscrdt.ErrUnavailable
		}
		if err = rows.Close(); err != nil {
			return paperscrdt.ErrUnavailable
		}
	}
	return s.verifyCollaborationRuntime(ctx, client)
}

func (s *Service) verifyCollaborationRuntime(ctx context.Context, engine collabEngine) error {
	if ctx == nil || engine == nil {
		return paperscrdt.ErrUnavailable
	}
	const fixture = "Atoman Papers CRDT startup probe"
	seeded, err := engine.Run(ctx, paperscrdt.Request{Op: "seed", Text: fixture})
	if err != nil || !paperscrdt.ValidCheckpoint(seeded) || seeded.Text != fixture {
		return paperscrdt.ErrUnavailable
	}
	read, err := engine.Run(ctx, paperscrdt.Request{Op: "read", Text: fixture, State: seeded.State})
	if err != nil || !paperscrdt.ValidCheckpoint(read) || read.Text != fixture ||
		!bytes.Equal(seeded.State, read.State) || !bytes.Equal(seeded.Vector, read.Vector) {
		return paperscrdt.ErrUnavailable
	}
	return s.configureCollaboration(engine)
}
