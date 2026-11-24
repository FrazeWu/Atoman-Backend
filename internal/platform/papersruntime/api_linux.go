//go:build linux

package papersruntime

import (
	"atoman/internal/platform/papersdaemon"
	"atoman/internal/platform/paperslauncher"
	"context"
	"log"
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
	svc, _, err := APIWithWorker(ctx, db)
	return svc, err
}

func APIWithWorker(ctx context.Context, db *gorm.DB) (*papers.Service, <-chan struct{}, error) {
	if os.Getenv("PAPERS_COMPILE_ENABLE") != "1" {
		return papers.NewService(db), closedWorker(), nil
	}
	return newEnabledAPI(ctx, db)
}

func closedWorker() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func newEnabledAPI(ctx context.Context, db *gorm.DB) (*papers.Service, <-chan struct{}, error) {
	disabled := papers.NewService(db)
	if os.Geteuid() == 0 || db == nil || db.Dialector.Name() != "postgres" {
		return disabled, closedWorker(), ErrConfig
	}
	cfg, err := loadAPI()
	if err != nil {
		return disabled, closedWorker(), err
	}
	roots, err := cfg.Trust.roots()
	if err != nil {
		return disabled, closedWorker(), err
	}
	reader, err := storageClient(cfg.Reader)
	if err != nil {
		return disabled, closedWorker(), err
	}
	probe, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	if err = verifyPrivateBucket(probe, reader, cfg.Trust.RetentionDays); err != nil {
		return disabled, closedWorker(), err
	}
	transport, err := paperspubgate.NewTransport(SnapshotSocket, PrivateBucket, reader)
	if err != nil {
		return disabled, closedWorker(), err
	}
	snapshots, trusted, err := trustedRegistry(transport, roots, cfg.Trust.RetentionDays)
	if err != nil {
		return disabled, closedWorker(), err
	}
	if err = snapshots.Probe(probe); err != nil {
		return disabled, closedWorker(), ErrConfig
	}
	if err = verifyReleasePin(probe, trusted, cfg.Trust); err != nil {
		return disabled, closedWorker(), err
	}
	results, err := papersassets.NewPDFRegistry(reader, PrivateBucket, SharedBucket)
	if err != nil {
		return disabled, closedWorker(), err
	}
	queue, err := papersqueue.NewWithDurablePDF(db, 30*time.Second)
	if err != nil {
		return disabled, closedWorker(), err
	}
	if _, err = queue.SlotState(probe); err != nil {
		return disabled, closedWorker(), ErrConfig
	}
	svc, err := papers.NewServiceWithCompile(db, papers.CompileRuntime{
		Publisher: snapshots, Release: trusted, Jobs: queue, Results: results, EnvironmentDigest: cfg.Trust.EnvironmentDigest,
	})
	if err != nil {
		return disabled, closedWorker(), err
	}
	if _, _, err := serviceIdentity("atoman", "atoman"); err != nil {
		return disabled, closedWorker(), err
	}
	if err := paperslauncher.CheckLauncher(probe, LauncherSocket); err != nil {
		return disabled, closedWorker(), err
	}
	client, err := paperslauncher.NewClient(LauncherSocket)
	if err != nil {
		return disabled, closedWorker(), err
	}
	daemon, err := papersdaemon.New(&papersqueue.Runner{Queue: queue, Executor: client}, papersdaemon.Config{
		WorkerID: "atoman-embedded-papers", PollInterval: time.Second, ErrorBackoff: 2 * time.Second, MaxBackoff: 30 * time.Second,
	})
	if err != nil {
		return disabled, closedWorker(), err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	bridge, err := paperslauncher.StartLeaseBridge(workerCtx, LeaseBridgeSocket, queue.ResolveLease)
	if err != nil {
		cancel()
		return disabled, closedWorker(), err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		log.Println("PAPERS_EMBEDDED_QUEUE_READY")
		_ = daemon.Run(workerCtx)
		cancel()
		<-bridge
	}()
	return svc, done, nil
}
