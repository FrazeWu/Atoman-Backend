// Command papers_queue_p0 is a local, disposable PostgreSQL test helper.
// It is NOT a Worker binary and MUST NOT be exposed to user requests.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	testOwner   = "11111111-1111-4111-8111-111111111111"
	testProject = "22222222-2222-4222-8222-222222222222"
	shaA        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB        = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "P0_TEST_FAIL:", err)
	os.Exit(1)
}
func mustID(value string) uuid.UUID {
	id, err := uuid.Parse(value)
	if err != nil {
		fatal(err)
	}
	return id
}
func main() {
	if os.Getenv("PAPERS_P0_QUEUE_CONFIRM") != "1" || len(os.Args) != 2 {
		fatal(fmt.Errorf("only local disposable P0 DB testing permitted"))
	}
	dsn := os.Getenv("PAPERS_P0_TEST_DSN")
	parsed, err := url.Parse(dsn)
	if err != nil {
		fatal(err)
	}
	if parsed.Scheme != "postgres" || parsed.Hostname() != "127.0.0.1" ||
		parsed.User.Username() != "atoman_test" || parsed.Path != "/atoman_test" ||
		parsed.Query().Get("sslmode") != "disable" {
		fatal(fmt.Errorf("refusing connection to anything but a disposable loopback test DB"))
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port <= 1024 || port == 5432 || port > 65535 {
		fatal(fmt.Errorf("unsafe or missing disposable test port"))
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		fatal(err)
	}
	queue, err := papersqueue.New(db, 15*time.Second)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()
	owner, project := mustID(testOwner), mustID(testProject)
	switch os.Args[1] {
	case "seed":
		if err := db.AutoMigrate(&model.PaperProject{}, &model.PaperAccessGrant{}, &model.PaperCompileSlot{}, &model.PaperCompileJob{}); err != nil {
			fatal(err)
		}
		if err := db.Create(&model.PaperCompileSlot{ID: 1}).Error; err != nil {
			fatal(err)
		}
		if err := db.Create(&model.PaperProject{Base: model.Base{ID: project}, OwnerID: owner, Name: "Disposable P0 compile", HeadRevision: 2}).Error; err != nil {
			fatal(err)
		}
		if err := db.Create(&model.PaperAccessGrant{UserID: owner, Status: "active"}).Error; err != nil {
			fatal(err)
		}
		_, err = queue.Submit(ctx, papersqueue.SubmitInput{
			OwnerID: owner, ProjectID: project, SourceRevision: 2,
			SnapshotDigest: shaA, EnvironmentDigest: shaB, MainFile: "main.tex",
			IdempotencyKey: "p0-lease-crash", Priority: papersqueue.PriorityManual, MaxAttempts: 2,
		})
		if err != nil {
			fatal(err)
		}
		fmt.Println("P0_QUEUE_SEEDED")
	case "claim-crash":
		claim, ok, err := queue.ClaimNext(ctx, "worker-process-1")
		if err != nil || !ok || claim.Kind != papersqueue.ClaimRun {
			fatal(fmt.Errorf("first lease unavailable: %v", err))
		}
		fmt.Println(claim.SandboxID)
		// Intentional process exit without Complete, simulating a dead worker.
	case "recover":
		claim, ok, err := queue.ClaimNext(ctx, "worker-process-2")
		if err != nil || !ok || claim.Kind != papersqueue.ClaimRecover {
			fatal(fmt.Errorf("recovery lease unavailable: %v", err))
		}
		// This test has NEVER created an OS sandbox. An actual worker must replace
		// this with verified physical gVisor cleanup before acknowledging.
		if err := queue.FinishRecovery(ctx, claim, true); err != nil {
			fatal(err)
		}
		fmt.Println("P0_OLD_LEASE_RECLAIMED_AFTER_MOCK_CLEANUP")
	case "complete":
		claim, ok, err := queue.ClaimNext(ctx, "worker-process-3")
		if err != nil || !ok || claim.Kind != papersqueue.ClaimRun {
			fatal(fmt.Errorf("new run lease unavailable: %v", err))
		}
		result := paperssandbox.Result{
			JobID: claim.SandboxID, Status: paperssandbox.Completed, UnderlyingStatus: paperssandbox.Completed,
			ExitCode: 0, ExitKnown: true, SentryObserved: true, LimitsVerified: true,
			ArtifactVerified: true, CleanupVerified: true,
		}
		// Synthetic result, never a claim that this process ran actual LaTeX.
		if err := queue.Complete(ctx, claim, result); err != nil {
			fatal(err)
		}
		fmt.Println(claim.SandboxID)
	case "verify":
		var tasks []model.PaperCompileJob
		if err := db.Find(&tasks).Error; err != nil {
			fatal(err)
		}
		if len(tasks) != 1 || tasks[0].Attempts != 2 || tasks[0].Status != papersqueue.StatusSucceeded {
			fatal(fmt.Errorf("incorrect persisted task: %+v", tasks))
		}
		slot, err := queue.SlotState(ctx)
		if err != nil {
			fatal(err)
		}
		if slot.JobID != nil || slot.Blocked || slot.Epoch != 3 {
			fatal(fmt.Errorf("slot was not recovered and released: %+v", slot))
		}
		fmt.Println("P0_MULTI_PROCESS_POSTGRES_RECOVERY_PASSED")
	default:
		fatal(fmt.Errorf("unsupported P0 mode"))
	}
}
