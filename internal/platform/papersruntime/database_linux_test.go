//go:build linux

package papersruntime

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/papersqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This integration test can only create roles in the fresh disposable DB
// supplied by scripts/test-isolated.sh. It cannot run against production.
func TestProductionDatabaseColumnRoleContract(t *testing.T) {
	if os.Getenv("PAPERS_ROLE_TEST_CONFIRM") != "1" {
		t.Skip("requires disposable DB role fixture")
	}
	raw := os.Getenv("TEST_POSTGRES_DSN")
	u, err := url.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "postgres", u.Scheme)
	require.Equal(t, "127.0.0.1", u.Hostname())
	require.Equal(t, "atoman_test", u.User.Username())
	require.Equal(t, "/atoman_test", u.Path)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	require.Greater(t, port, 1024)
	require.NotEqual(t, 5432, port)
	require.Equal(t, "sslmode=disable", u.RawQuery)
	db, err := gorm.Open(postgres.Open(raw), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	var count int64
	require.NoError(t, db.Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&count).Error)
	require.Zero(t, count, "role test requires a new database")
	require.NoError(t, db.AutoMigrate(&model.PaperProject{}, &model.PaperAccessGrant{}, &model.PaperCompileJob{}, &model.PaperCompileSlot{}))
	require.NoError(t, db.Exec("CREATE TABLE public.role_test_unrelated (secret text)").Error)
	require.NoError(t, db.Create(&model.PaperCompileSlot{ID: 1}).Error)
	owner := uuid.New()
	project := model.PaperProject{OwnerID: owner, Name: "Role fixture", HeadRevision: 1}
	require.NoError(t, db.Create(&project).Error)
	require.NoError(t, db.Create(&model.PaperAccessGrant{UserID: owner, Status: "active"}).Error)
	ctx := context.Background()
	adminQ, err := papersqueue.NewWithDurablePDF(db, 30*time.Second)
	require.NoError(t, err)
	job, err := adminQ.Submit(ctx, papersqueue.SubmitInput{OwnerID: owner, ProjectID: project.ID, SourceRevision: 1, SnapshotDigest: "sha256:" + strings.Repeat("a", 64), EnvironmentDigest: "sha256:" + strings.Repeat("b", 64), MainFile: "main.tex", IdempotencyKey: "role-test", Priority: papersqueue.PriorityManual, MaxAttempts: 2})
	require.NoError(t, err)
	for _, readonly := range []bool{true, false} {
		role := "paper_role_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		password := strings.ReplaceAll(uuid.NewString(), "-", "") + strings.ReplaceAll(uuid.NewString(), "-", "")
		require.NoError(t, db.Exec(fmt.Sprintf("CREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '%s'", role, password)).Error)
		require.NoError(t, db.Exec("GRANT USAGE ON SCHEMA public TO "+role).Error)
		for table := range readableTables {
			require.NoError(t, db.Exec("GRANT SELECT ON public."+table+" TO "+role).Error)
		}
		if readonly {
			require.NoError(t, db.Exec("ALTER ROLE "+role+" SET default_transaction_read_only = on").Error)
		} else {
			for table, allowed := range workerUpdates {
				cols := []string{}
				for col := range allowed {
					cols = append(cols, col)
				}
				sort.Strings(cols)
				require.NoError(t, db.Exec("GRANT UPDATE ("+strings.Join(cols, ",")+") ON public."+table+" TO "+role).Error)
			}
		}
		scopedURL := *u
		scopedURL.User = url.UserPassword(role, password)
		scoped, err := gorm.Open(postgres.Open(scopedURL.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		require.NoError(t, verifyDatabaseRole(ctx, scoped, role, "atoman_test", readonly))
		require.Error(t, scoped.Exec("UPDATE public.paper_compile_jobs SET snapshot_digest='forbidden'").Error)
		require.Error(t, scoped.Exec("DELETE FROM public.paper_compile_jobs").Error)
		var value string
		require.Error(t, scoped.Raw("SELECT secret FROM public.role_test_unrelated").Scan(&value).Error)
		if !readonly {
			queue, err := papersqueue.NewWithDurablePDF(scoped, 30*time.Second)
			require.NoError(t, err)
			claim, ok, err := queue.ClaimNext(ctx, "role-fixture-worker")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, job.ID, claim.Job.ID)
			require.NoError(t, queue.Heartbeat(ctx, claim))
		}
		require.NoError(t, db.Exec("GRANT SELECT ON public.role_test_unrelated TO "+role).Error)
		require.Error(t, verifyDatabaseRole(ctx, scoped, role, "atoman_test", readonly))
		conn, _ := scoped.DB()
		require.NoError(t, conn.Close())
	}
	t.Log("PRODUCTION_COLUMN_GRANT_CONTRACT_VERIFIED")
}
