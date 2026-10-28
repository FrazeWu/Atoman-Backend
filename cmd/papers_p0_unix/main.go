//go:build linux

// Command papers_p0_unix is a disposable localhost-only P0 launcher
// integration harness, NOT a production service or an Atoman web API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersdaemon"
	"atoman/internal/platform/paperslauncher"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
	"atoman/internal/platform/papersworker"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func fatal(err error) { fmt.Fprintln(os.Stderr, "P0_UNIX_FAIL:", err); os.Exit(1) }

func lookup(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		fatal(err)
	}
	return path
}
func restrictedDB(role string) (*gorm.DB, error) {
	key := "PAPERS_P0_TEST_DSN"
	if role == "server" {
		key = "PAPERS_P0_LAUNCHER_RO_DSN"
	}
	dsn := os.Getenv(key)
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(u.Port())
	user := "atoman_test"
	if role == "server" {
		user = "papers_launcher_ro"
	}
	if err != nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" ||
		u.User.Username() != user || u.Path != "/atoman_test" ||
		u.Query().Get("sslmode") != "disable" || port <= 1024 || port == 5432 || port > 65535 {
		return nil, errors.New("P0 Unix harness refuses all non-disposable PostgreSQL endpoints")
	}
	return gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
}
func main() {
	if os.Getenv("PAPERS_P0_UNIX_CONFIRM") != "1" {
		fatal(errors.New("explicit local P0 authorization required"))
	}
	var root, runsc, helper, workerName string
	flag.StringVar(&root, "root", "", "disposable P0 work root")
	flag.StringVar(&runsc, "runsc", "", "verified runsc executable")
	flag.StringVar(&helper, "helper", "", "fixed P0 systemd scope helper")
	flag.StringVar(&workerName, "worker", "unix-test-worker", "worker identity")
	flag.Parse()
	if flag.NArg() != 1 || !filepath.IsAbs(root) ||
		!strings.HasPrefix(root, "/var/tmp/atoman-papers-p0-durable.") ||
		len(root) > 85 {
		fatal(errors.New("invalid work root or mode"))
	}
	mode := flag.Arg(0)
	if mode != "server" && mode != "worker" && mode != "daemon" {
		fatal(errors.New("unsupported operation"))
	}
	db, err := restrictedDB(mode)
	if err != nil {
		fatal(err)
	}
	q, err := papersqueue.New(db, 12*time.Second)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()
	socket := filepath.Join(root, "uds", "launcher.sock")
	if mode == "worker" || mode == "daemon" {
		if os.Geteuid() != 65534 || os.Getegid() != 65534 {
			fatal(errors.New("P0 Worker MUST run as unprivileged UID/GID 65534"))
		}
		if runsc != "" || helper != "" {
			fatal(errors.New("Worker cannot provide privileged tool paths"))
		}
		client, err := paperslauncher.NewClient(socket)
		if err != nil {
			fatal(err)
		}
		runner := papersqueue.Runner{Queue: q, Executor: client}
		if mode == "daemon" {
			scoped, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			daemon, err := papersdaemon.New(&runner, papersdaemon.Config{
				WorkerID: workerName, PollInterval: 500 * time.Millisecond,
				ErrorBackoff: time.Second, MaxBackoff: 15 * time.Second,
			})
			if err != nil {
				fatal(err)
			}
			fmt.Printf("P0_WORKER_DAEMON_STARTED uid=%d\n", os.Geteuid())
			if err := daemon.Run(scoped); err != nil {
				fatal(err)
			}
			fmt.Println("P0_WORKER_DAEMON_EXITED")
			return
		}
		worked, err := runner.Step(ctx, workerName)
		if err != nil {
			fatal(err)
		}
		if !worked {
			fatal(errors.New("no queued or recoverable work available"))
		}
		fmt.Println("P0_UNPRIVILEGED_WORKER_STEP_PASSED uid=65534")
		return
	}
	if os.Geteuid() != 0 || !filepath.IsAbs(runsc) || !filepath.IsAbs(helper) {
		fatal(errors.New("P0 launcher must be root with operator-pinned binaries"))
	}
	// The root launcher must not receive a mutable database account.
	if os.Getenv("PAPERS_P0_TEST_DSN") != "" {
		fatal(errors.New("refusing mutable database credentials in privileged launcher environment"))
	}
	var proof struct {
		User      string
		Readonly  string
		CanUpdate bool
	}
	if err = db.Raw(`SELECT current_user AS user, current_setting('default_transaction_read_only') AS readonly,
	  has_table_privilege(current_user,'public.paper_compile_jobs','UPDATE') AS can_update`).Scan(&proof).Error; err != nil {
		fatal(err)
	}
	if proof.User != "papers_launcher_ro" || proof.Readonly != "on" || proof.CanUpdate {
		fatal(errors.New("privileged launcher database role is not read-only"))
	}
	// Server has fixed privileged tool paths from startup flags, NOT values
	// supplied by the request over the socket.
	store, err := papersassets.OpenStore(filepath.Join(root, "assets"))
	if err != nil {
		fatal(err)
	}
	executor, err := papersworker.New(papersworker.Config{
		WorkRoot: filepath.Join(root, "runs"), Store: store,
		ArchivePath: filepath.Join(root, "rootfs.tar"),
		RunscBinary: runsc, ScopeHelper: helper,
		SystemdRunBinary: lookup("systemd-run"), SystemctlBinary: lookup("systemctl"),
		MountpointBinary: lookup("mountpoint"), FindmntBinary: lookup("findmnt"),
		UmountBinary: lookup("umount"), TarBinary: lookup("tar"),
		Limits: paperssandbox.Limits{
			MemoryMaxBytes: 768 << 20, MemorySwapMaxBytes: 0, PIDsMax: 256,
			CPUQuotaMicros: 100000, CPUPeriodMicros: 100000,
		},
		Timeout: 3 * time.Minute, WrapperMemory: "1024M", WrapperCPUQuota: "100%", WrapperTasksMax: 256,
	})
	if err != nil {
		fatal(err)
	}
	svc, err := paperslauncher.NewServer(paperslauncher.Config{
		AllowedUID: 65534, AllowedGID: 65534, Executor: executor,
		Verify: q.ResolveLease, LeaseCheckInterval: 2 * time.Second,
	})
	if err != nil {
		fatal(err)
	}
	listener, err := paperslauncher.ListenRecoverStale(socket, 65534)
	if err != nil {
		fatal(err)
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fmt.Printf("P0_UNIX_LAUNCHER_READY socket=%s uid=%d\n", socket, os.Geteuid())
	if err = svc.Serve(ctx, listener); err != nil {
		fatal(err)
	}
	fmt.Println("P0_UNIX_LAUNCHER_STOPPED")
}
