//go:build linux

package papersruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersdaemon"
	"atoman/internal/platform/paperslauncher"
	"atoman/internal/platform/paperspubgate"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
	"atoman/internal/platform/papersworker"
)

func uid(name string) (uint32, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, ErrConfig
	}
	n, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil || n < 100 {
		return 0, ErrConfig
	}
	return uint32(n), nil
}
func gid(name string) (uint32, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, ErrConfig
	}
	n, err := strconv.ParseUint(g.Gid, 10, 32)
	if err != nil || n < 100 {
		return 0, ErrConfig
	}
	return uint32(n), nil
}
func serviceIdentity(account, group string) (uint32, uint32, error) {
	u, err := uid(account)
	if err != nil {
		return 0, 0, err
	}
	g, err := gid(group)
	if err != nil {
		return 0, 0, err
	}
	if uint32(os.Geteuid()) != u || uint32(os.Getegid()) != g {
		return 0, 0, ErrConfig
	}
	return u, g, nil
}

func Run(ctx context.Context, role string) error {
	if os.Getenv("PAPERS_SERVICE_ENABLE") != "1" {
		return ErrConfig
	}
	switch role {
	case "worker":
		return ErrConfig // queue worker is embedded in Atoman
	case "launcher":
		return runLauncher(ctx)
	case "publish-release":
		return runPublisher(ctx)
	case "snapshot-gateway", "pdf-gateway", "environment-gateway":
		return runGateway(ctx, role)
	default:
		return ErrConfig
	}
}

func runWorker(ctx context.Context) error {
	if _, _, err := serviceIdentity("atoman-papers-worker", "atoman-papers-worker"); err != nil {
		return err
	}
	cfg, err := loadWorker()
	if err != nil {
		return err
	}
	start, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	db, err := queueDB(start, cfg.DatabasePassword, false)
	if err != nil {
		return err
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	q, err := papersqueue.NewWithDurablePDF(db, 30*time.Second)
	if err != nil {
		return err
	}
	client, err := paperslauncher.NewClient(LauncherSocket)
	if err != nil {
		return err
	}
	runner := papersqueue.Runner{Queue: q, Executor: client}
	daemon, err := papersdaemon.New(&runner, papersdaemon.Config{
		WorkerID: "papers-production-worker", PollInterval: time.Second, ErrorBackoff: 2 * time.Second, MaxBackoff: 30 * time.Second,
	})
	if err != nil {
		return err
	}
	fmt.Println("PAPERS_WORKER_READY")
	return daemon.Run(ctx)
}

// Each privileged execution must refer to exactly the deployed signed pin.
type pinnedArchive struct {
	reader *papersassets.TrustedCompileAssetReader
	cache  *papersassets.TrustedArchiveCache
	digest string
}

func (p pinnedArchive) LoadCompileSnapshot(ctx context.Context, d string) (papersassets.Snapshot, error) {
	return p.reader.LoadCompileSnapshot(ctx, d)
}
func (p pinnedArchive) VerifyCompileArchive(ctx context.Context, d, path string) (papersassets.Environment, error) {
	if d != p.digest {
		return papersassets.Environment{}, ErrConfig
	}
	return p.reader.VerifyCompileArchive(ctx, d, path)
}
func (p pinnedArchive) FetchArchive(ctx context.Context, d string) (string, error) {
	if d != p.digest {
		return "", ErrConfig
	}
	return p.cache.FetchArchive(ctx, d)
}

func verifyRootExecutable(path, digest string) error {
	if !digestRE.MatchString(digest) || !filepath.IsAbs(path) {
		return ErrConfig
	}
	// Every path component must be root-owned and immutable to other users.
	for current := path; ; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if err != nil {
			return ErrConfig
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || st.Mode().Perm()&0022 != 0 || st.Mode()&os.ModeSymlink != 0 {
			return ErrConfig
		}
		if current == path && (!st.Mode().IsRegular() || st.Mode().Perm()&0100 == 0) {
			return ErrConfig
		}
		if current != path && !st.IsDir() {
			return ErrConfig
		}
		if current == "/" {
			break
		}
	}
	actual, err := papersassets.HashFile(path)
	if err != nil || actual != digest {
		return errors.New("papers runtime: executable integrity check failed")
	}
	return nil
}

func runLauncher(ctx context.Context) error {
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		return ErrConfig
	}
	cfg, err := loadLauncher()
	if err != nil {
		return err
	}
	if err = verifyRootExecutable(RunscPath, cfg.RunscDigest); err != nil {
		return err
	}
	if err = verifyRootExecutable(ScopeHelper, cfg.HelperDigest); err != nil {
		return err
	}
	roots, err := cfg.Trust.roots()
	if err != nil {
		return err
	}
	start, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	reader, err := storageClient(cfg.Reader)
	if err != nil {
		return err
	}
	if err = verifyPrivateBucket(start, reader, cfg.Trust.RetentionDays); err != nil {
		return err
	}
	sources, trusted, err := trustedRegistry(reader, roots, cfg.Trust.RetentionDays)
	if err != nil {
		return err
	}
	if err = verifyReleasePin(start, trusted, cfg.Trust); err != nil {
		return err
	}
	cache, err := papersassets.NewTrustedArchiveCache(trusted, ArchiveCache)
	if err != nil {
		return err
	}
	assets := pinnedArchive{reader: &papersassets.TrustedCompileAssetReader{Sources: sources, Releases: trusted}, cache: cache, digest: cfg.Trust.EnvironmentDigest}
	transport, err := paperspubgate.NewTransport(PDFSocket, PrivateBucket, reader)
	if err != nil {
		return err
	}
	publisher, err := papersassets.NewPDFRegistry(transport, PrivateBucket, SharedBucket)
	if err != nil {
		return err
	}
	executor, err := papersworker.New(papersworker.Config{
		WorkRoot: WorkRoot, Store: assets, ArchiveFetcher: assets, ArchivePath: filepath.Join(ArchiveCache, "never-fallback.tar"),
		PDFPublisher: publisher, BoundedWorkspace: true, EnableLuaLaTeX: cfg.EnableLuaLaTeX,
		RunscBinary: RunscPath, ScopeHelper: ScopeHelper, SystemdRunBinary: "/usr/bin/systemd-run", SystemctlBinary: "/usr/bin/systemctl",
		MountpointBinary: "/usr/bin/mountpoint", FindmntBinary: "/usr/bin/findmnt", UmountBinary: "/usr/bin/umount", TarBinary: "/usr/bin/tar",
		Limits:  paperssandbox.Limits{MemoryMaxBytes: 768 << 20, PIDsMax: 256, CPUQuotaMicros: 100000, CPUPeriodMicros: 100000},
		Timeout: 2 * time.Minute, WrapperMemory: "1024M", WrapperCPUQuota: "100%", WrapperTasksMax: 256,
	})
	if err != nil {
		return err
	}
	workerUID, err := uid("atoman")
	if err != nil {
		return err
	}
	workerGID, err := gid("atoman")
	if err != nil {
		return err
	}
	launcher, err := paperslauncher.NewServer(paperslauncher.Config{
		AllowedUID: workerUID, AllowedGID: workerGID, Executor: executor, Verify: paperslauncher.RemoteLeaseVerifier(LeaseBridgeSocket, workerUID), LeaseCheckInterval: 2 * time.Second,
	})
	if err != nil {
		return err
	}
	listener, err := paperslauncher.ListenRecoverStale(LauncherSocket, workerGID)
	if err != nil {
		return err
	}
	defer listener.Close()
	fmt.Println("PAPERS_LAUNCHER_READY")
	return launcher.Serve(ctx, listener)
}

func runGateway(ctx context.Context, mode string) error {
	var role, account, group, socket, peer, staging string
	switch mode {
	case "snapshot-gateway":
		role, account, group, socket, peer, staging = "snapshot", "atoman-papers-snapshot", "atoman-papers-api", SnapshotSocket, "atoman", "/var/lib/atoman-papers-snapshot"
	case "pdf-gateway":
		role, account, group, socket, staging = "pdf", "atoman-papers-pdf", "atoman-papers-pdf", PDFSocket, "/var/lib/atoman-papers-pdf"
	case "environment-gateway":
		role, account, group, socket, peer, staging = "environment", "atoman-papers-env", "atoman-papers-publish", EnvironmentSocket, "atoman-papers-publisher", "/var/lib/atoman-papers-environment"
	default:
		return ErrConfig
	}
	_, g, err := serviceIdentity(account, group)
	if err != nil {
		return err
	}
	var peerUID uint32
	if peer != "" {
		peerUID, err = uid(peer)
		if err != nil {
			return err
		}
	}
	cfg, err := loadGateway(role)
	if err != nil {
		return err
	}
	writer, err := storageClient(cfg.Writer)
	if err != nil {
		return err
	}
	start, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	if err = verifyPrivateBucket(start, writer, cfg.RetentionDays); err != nil {
		return err
	}
	probe, err := papersassets.NewPrivateS3Registry(writer, PrivateBucket, SharedBucket)
	if err != nil {
		return err
	}
	if err = probe.Probe(start); err != nil {
		return ErrConfig
	}
	var gate *paperspubgate.Gateway
	switch role {
	case "snapshot":
		gate, err = paperspubgate.NewSnapshotGateway(writer, PrivateBucket, staging, peerUID)
	case "pdf":
		gate, err = paperspubgate.NewPDFGateway(writer, PrivateBucket, staging, peerUID)
	case "environment":
		gate, err = paperspubgate.NewEnvironmentGateway(writer, PrivateBucket, staging, peerUID)
	}
	if err != nil {
		return err
	}
	server, err := paperspubgate.NewUnixServer(gate, socket, int(g))
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	fmt.Println("PAPERS_PUBLICATION_GATEWAY_READY", role)
	select {
	case err := <-done:
		return errors.Join(err, server.Close())
	case <-ctx.Done():
		closeErr := server.Close()
		return errors.Join(closeErr, <-done)
	}
}
