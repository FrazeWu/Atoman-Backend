//go:build linux

package papersruntime

import (
	"context"
	"os"
	"time"

	"atoman/internal/modules/papers"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/paperspubgate"
	"atoman/internal/platform/papersqueue"
	"gorm.io/gorm"
)

// API is opt-in by an explicit deployment setting plus dedicated systemd
// credentials. All ordinary website and Papers editing routes remain usable
// when the compile infrastructure is absent or startup verification fails.
func API(ctx context.Context, db *gorm.DB) (*papers.Service, error) {
	if os.Getenv("PAPERS_COMPILE_ENABLE") != "1" {
		return papers.NewService(db), nil
	}
	return newEnabledAPI(ctx, db)
}

func newEnabledAPI(ctx context.Context, db *gorm.DB) (*papers.Service, error) {
	disabled := papers.NewService(db)
	if os.Geteuid() == 0 || db == nil || db.Dialector.Name() != "postgres" {
		return disabled, ErrConfig
	}
	cfg, err := loadAPI()
	if err != nil {
		return disabled, err
	}
	roots, err := cfg.Trust.roots()
	if err != nil {
		return disabled, err
	}
	reader, err := storageClient(cfg.Reader)
	if err != nil {
		return disabled, err
	}
	probe, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	if err = verifyPrivateBucket(probe, reader, cfg.Trust.RetentionDays); err != nil {
		return disabled, err
	}
	transport, err := paperspubgate.NewTransport(SnapshotSocket, PrivateBucket, reader)
	if err != nil {
		return disabled, err
	}
	snapshots, trusted, err := trustedRegistry(transport, roots, cfg.Trust.RetentionDays)
	if err != nil {
		return disabled, err
	}
	if err = snapshots.Probe(probe); err != nil {
		return disabled, ErrConfig
	}
	if err = verifyReleasePin(probe, trusted, cfg.Trust); err != nil {
		return disabled, err
	}
	results, err := papersassets.NewPDFRegistry(reader, PrivateBucket, SharedBucket)
	if err != nil {
		return disabled, err
	}
	queue, err := papersqueue.NewWithDurablePDF(db, 30*time.Second)
	if err != nil {
		return disabled, err
	}
	if _, err = queue.SlotState(probe); err != nil {
		return disabled, ErrConfig
	}
	svc, err := papers.NewServiceWithCompile(db, papers.CompileRuntime{
		Publisher: snapshots, Release: trusted, Jobs: queue, Results: results, EnvironmentDigest: cfg.Trust.EnvironmentDigest,
	})
	if err != nil {
		return disabled, err
	}
	return svc, nil
}
