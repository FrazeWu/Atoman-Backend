// Command papers_p0_durable_worker is a LOCAL TEST HARNESS ONLY.
// It requires deliberate root authorization and accepts exclusively a
// disposable loopback PostgreSQL database and pinned, offline TeX assets.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
	"atoman/internal/platform/papersworker"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	p0Owner   = "11111111-1111-4111-8111-111111111111"
	p0Project = "22222222-2222-4222-8222-222222222222"
	p0Image   = "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4"
)

// A fixed, finite but intentionally nontrivial LaTeX loop gives the outer
// test harness time to observe the real gVisor scope before SIGKILL.
// This source is written by the maintainer's trusted P0 executable, never a user.
const p0Tex = `\documentclass{article}
\newcount\pzero
\begin{document}
Atoman Papers persistent queue actual recovery.
\pzero=0
\loop\advance\pzero by1\ifnum\pzero<60000000\repeat
After recovery $E=mc^2$.
\end{document}
`

func fail(err error) { fmt.Fprintln(os.Stderr, "P0_DURABLE_FAIL:", err); os.Exit(1) }
func lookup(name string) string {
	found, err := exec.LookPath(name)
	if err != nil {
		fail(err)
	}
	path, err := filepath.Abs(found)
	if err != nil {
		fail(err)
	}
	return path
}
func main() {
	if os.Geteuid() != 0 || os.Getenv("PAPERS_P0_DURABLE_CONFIRM") != "1" {
		fail(errors.New("P0 only: local root with explicit PAPERS_P0_DURABLE_CONFIRM=1"))
	}
	var root, runsc, helper, workerID string
	flag.StringVar(&root, "root", "", "dedicated P0 workroot")
	flag.StringVar(&runsc, "runsc", "", "pinned runsc binary")
	flag.StringVar(&helper, "helper", "", "fixed scope helper")
	flag.StringVar(&workerID, "worker", "durable-worker", "P0 worker name")
	flag.Parse()
	if flag.NArg() != 1 || !filepath.IsAbs(root) || !filepath.IsAbs(runsc) || !filepath.IsAbs(helper) ||
		!strings.HasPrefix(root, "/var/tmp/atoman-papers-p0-durable.") {
		fail(errors.New("unrecognized P0 arguments"))
	}
	st, err := os.Lstat(root)
	if err != nil {
		fail(err)
	}
	if !st.IsDir() || st.Mode().Perm()&0067 != 0 || st.Mode()&os.ModeSymlink != 0 {
		fail(errors.New("private P0 workroot required"))
	}
	dsn := os.Getenv("PAPERS_P0_TEST_DSN")
	parsed, err := url.Parse(dsn)
	if err != nil {
		fail(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || parsed.Scheme != "postgres" || parsed.Hostname() != "127.0.0.1" ||
		parsed.User.Username() != "atoman_test" || parsed.Path != "/atoman_test" ||
		parsed.Query().Get("sslmode") != "disable" || port <= 1024 || port == 5432 || port > 65535 {
		fail(errors.New("refusing any database other than a disposable loopback test database"))
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fail(err)
	}
	q, err := papersqueue.New(db, 12*time.Second)
	if err != nil {
		fail(err)
	}
	store, err := papersassets.OpenStore(filepath.Join(root, "assets"))
	if err != nil {
		fail(err)
	}
	id := uuid.MustParse(p0Project)
	owner := uuid.MustParse(p0Owner)
	ctx := context.Background()
	switch flag.Arg(0) {
	case "seed":
		if err = db.AutoMigrate(&model.PaperProject{}, &model.PaperAccessGrant{}, &model.PaperCompileSlot{}, &model.PaperCompileJob{}); err != nil {
			fail(err)
		}
		if err = db.Create(&model.PaperCompileSlot{ID: 1}).Error; err != nil {
			fail(err)
		}
		if err = db.Create(&model.PaperProject{Base: model.Base{ID: id}, OwnerID: owner, Name: "P0 Trusted Real TeX", HeadRevision: 2}).Error; err != nil {
			fail(err)
		}
		if err = db.Create(&model.PaperAccessGrant{UserID: owner, Status: "active"}).Error; err != nil {
			fail(err)
		}
		snapshot, err := store.PublishSnapshot(papersassets.Snapshot{
			Version: 1, ProjectID: id, Revision: 2,
			Files: []papersassets.SourceFile{{Path: "main.tex", Content: p0Tex}},
		})
		if err != nil {
			fail(err)
		}
		archive := filepath.Join(root, "rootfs.tar")
		hash, err := papersassets.HashFile(archive)
		if err != nil {
			fail(err)
		}
		env, err := store.PublishEnvironment(papersassets.Environment{
			Version: 1, Name: "texlive-2026-p0", ImageDigest: p0Image,
			ArchiveDigest: hash, RunscRelease: "20261019.0",
		})
		if err != nil {
			fail(err)
		}
		job, err := q.Submit(ctx, papersqueue.SubmitInput{
			OwnerID: owner, ProjectID: id, SourceRevision: 2,
			SnapshotDigest: snapshot, EnvironmentDigest: env, MainFile: "main.tex",
			IdempotencyKey: "p0-killed-real-worker", Priority: papersqueue.PriorityManual, MaxAttempts: 2,
		})
		if err != nil {
			fail(err)
		}
		fmt.Printf("P0_DURABLE_SEEDED job=%s snapshot=%s environment=%s\n", job.ID, snapshot, env)
	case "run":
		execWorker, err := papersworker.New(papersworker.Config{
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
			fail(err)
		}
		runner := &papersqueue.Runner{Queue: q, Executor: execWorker}
		worked, err := runner.Step(ctx, workerID)
		if err != nil {
			fail(err)
		}
		if !worked {
			fail(errors.New("no task was available: unexpected for P0 stage"))
		}
		var current []model.PaperCompileJob
		if err = db.Find(&current).Error; err != nil {
			fail(err)
		}
		if len(current) == 1 {
			fmt.Printf("P0_DURABLE_RESULT status=%s code=%s attempts=%d sandbox=%s\n",
				current[0].Status, current[0].FailureCode, current[0].Attempts, current[0].SandboxID)
		}
		fmt.Printf("P0_DURABLE_STEP_COMPLETE worker=%s\n", workerID)
	case "status":
		var jobs []model.PaperCompileJob
		if err = db.Order("created_at ASC").Find(&jobs).Error; err != nil {
			fail(err)
		}
		if len(jobs) != 1 {
			fail(fmt.Errorf("unexpected jobs %d", len(jobs)))
		}
		slot, err := q.SlotState(ctx)
		if err != nil {
			fail(err)
		}
		fmt.Printf("job=%s status=%s attempts=%d sandbox=%s epoch=%d blocked=%v reason=%s\n",
			jobs[0].ID, jobs[0].Status, jobs[0].Attempts, jobs[0].SandboxID,
			slot.Epoch, slot.Blocked, slot.BlockReason)
	case "verify":
		var jobs []model.PaperCompileJob
		if err = db.Find(&jobs).Error; err != nil {
			fail(err)
		}
		slot, err := q.SlotState(ctx)
		if err != nil {
			fail(err)
		}
		if len(jobs) != 1 || jobs[0].Status != papersqueue.StatusSucceeded ||
			jobs[0].Attempts != 2 || slot.Epoch != 3 || slot.JobID != nil || slot.Blocked {
			fail(fmt.Errorf("unexpected real recovery state: jobs=%+v slot=%+v", jobs, slot))
		}
		fmt.Println("P0_REAL_GVISOR_WORKER_KILLED_RECOVERED_AND_LATEX_SUCCEEDED")
	default:
		fail(fmt.Errorf("P0 mode must be seed/run/status/verify"))
	}
}
