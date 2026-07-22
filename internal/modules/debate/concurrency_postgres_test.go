package debate

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	debatevoting "atoman/internal/modules/debate_voting"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestConcurrentCrossReferencesFinishWithoutDeadlockInPostgres(t *testing.T) {
	db := newDebateConcurrencyPostgresDB(t)
	ctx := newDebateContextForDB(t, db)
	a := createDebateForTest(t, ctx, "A", "a")
	b := createDebateForTest(t, ctx, "B", "b")
	setConclusionForTest(t, db, a.ID, model.DebateVoteYes)
	setConclusionForTest(t, db, b.ID, model.DebateVoteNo)

	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	service := NewService(db.WithContext(testContext))
	errs := runConcurrentDebateOperations(t, cancel,
		func() error {
			_, err := service.SaveWiki(ctx.editor, a.ID, SaveWikiRequest{Title: "A", Content: "@debate:" + b.ID.String() + ":support", EditSummary: "cite B", BaseRevisionID: *a.CurrentRevisionID})
			return err
		},
		func() error {
			_, err := service.SaveWiki(ctx.editor, b.ID, SaveWikiRequest{Title: "B", Content: "@debate:" + a.ID.String() + ":support", EditSummary: "cite A", BaseRevisionID: *b.CurrentRevisionID})
			return err
		},
	)
	assertNoPostgresDeadlock(t, errs)
	var success, cycle int
	for _, err := range errs {
		if err == nil {
			success++
			continue
		}
		var appErr *apperr.AppError
		if errors.As(err, &appErr) && appErr.Code == "debate.reference_cycle" {
			cycle++
			continue
		}
		t.Fatalf("unexpected cross-save error: %v", err)
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, cycle)
	var relationCount int64
	require.NoError(t, db.Model(&model.DebateRelation{}).Count(&relationCount).Error)
	require.EqualValues(t, 1, relationCount)
}

func TestConcurrentConclusionReversalAndReconfirmFinishWithoutDeadlockInPostgres(t *testing.T) {
	db := newDebateConcurrencyPostgresDB(t)
	ctx := newDebateContextForDB(t, db)
	source := createDebateForTest(t, ctx, "Source", "source")
	target := createDebateForTest(t, ctx, "Target", "target")
	oldEvent := setConclusionForTest(t, db, source.ID, model.DebateVoteYes)
	raw := "@debate:" + source.ID.String() + ":support"
	targetWithReference, err := ctx.service.SaveWiki(ctx.editor, target.ID, SaveWikiRequest{Title: "Target", Content: raw, EditSummary: "cite", BaseRevisionID: *target.CurrentRevisionID})
	require.NoError(t, err)
	relationID := *targetWithReference.References[0].RelationID
	require.NoError(t, db.Model(&model.DebateRelation{}).Where("id = ?", relationID).Update("status", model.DebateRelationStale).Error)

	voters := make([]model.User, 10)
	for i := range voters {
		voters[i] = model.User{UUID: uuid.New(), Username: "voter-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	}
	require.NoError(t, db.Create(&voters).Error)
	for i, voter := range voters {
		direction := model.DebateVoteNo
		if i < 2 {
			direction = model.DebateVoteYes
		}
		require.NoError(t, db.Create(&model.DebateVote{DebateID: source.ID, UserID: voter.UUID, Direction: direction}).Error)
	}

	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	wikiService := NewService(db.WithContext(testContext))
	votingService := debatevoting.NewService(db.WithContext(testContext))
	errs := runConcurrentDebateOperations(t, cancel,
		func() error {
			_, err := votingService.SetVote(authctx.CurrentUser{ID: voters[0].UUID, Username: voters[0].Username, Role: voters[0].Role}, source.ID, model.DebateVoteNo)
			return err
		},
		func() error {
			_, err := wikiService.ReconfirmReference(ctx.editor, target.ID, relationID, ReconfirmReferenceRequest{BaseRevisionID: *targetWithReference.CurrentRevisionID, EditSummary: "reconfirm"})
			return err
		},
	)
	assertNoPostgresDeadlock(t, errs)
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	var storedSource model.Debate
	require.NoError(t, db.First(&storedSource, "id = ?", source.ID).Error)
	require.Equal(t, model.DebateVoteNo, storedSource.ConclusionType)
	require.NotNil(t, storedSource.CurrentConclusionEventID)
	var relation model.DebateRelation
	require.NoError(t, db.First(&relation, "id = ?", relationID).Error)
	switch relation.Status {
	case model.DebateRelationActive:
		require.Equal(t, *storedSource.CurrentConclusionEventID, relation.SourceConclusionEventID)
	case model.DebateRelationStale:
		require.Equal(t, oldEvent.ID, relation.SourceConclusionEventID)
	default:
		t.Fatalf("unexpected relation status after concurrency: %s", relation.Status)
	}
}

func TestProtectionCommitsBeforeWaitingSaveAndAdminCanStillSaveInPostgres(t *testing.T) {
	db := newDebateConcurrencyPostgresDB(t)
	ctx := newDebateContextForDB(t, db)
	created := createDebateForTest(t, ctx, "Protected", "body")
	locked := make(chan struct{})
	saveAttempted := make(chan struct{})
	releaseProtection := make(chan struct{})
	var lockedOnce, attemptedOnce sync.Once
	callback := "test:coordinate-protection-save"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(debateConcurrencyMarker{}) != "protect" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; ok {
			if _, ok := tx.Statement.Dest.(*model.Debate); ok {
				lockedOnce.Do(func() { close(locked) })
				<-releaseProtection
			}
		}
	}))
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback+":save", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(debateConcurrencyMarker{}) != "save" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; ok {
			if _, ok := tx.Statement.Dest.(*model.Debate); ok {
				attemptedOnce.Do(func() { close(saveAttempted) })
			}
		}
	}))
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callback)
		_ = db.Callback().Query().Remove(callback + ":save")
	})

	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	protectContext := context.WithValue(testContext, debateConcurrencyMarker{}, "protect")
	saveContext := context.WithValue(testContext, debateConcurrencyMarker{}, "save")
	protectErr := make(chan error, 1)
	go func() {
		protectErr <- NewService(db.WithContext(protectContext)).SetProtection(ctx.admin, created.ID, ProtectionRequest{ProtectionLevel: "full"})
	}()
	awaitDebateSignal(t, cancel, locked, "protection did not acquire debate lock")
	saveErr := make(chan error, 1)
	go func() {
		_, err := NewService(db.WithContext(saveContext)).SaveWiki(ctx.editor, created.ID, SaveWikiRequest{Title: "Blocked", Content: "body", EditSummary: "blocked", BaseRevisionID: *created.CurrentRevisionID})
		saveErr <- err
	}()
	awaitDebateSignal(t, cancel, saveAttempted, "save did not attempt debate lock")
	close(releaseProtection)
	require.NoError(t, awaitDebateError(t, cancel, protectErr, "protection did not finish"))
	err := awaitDebateError(t, cancel, saveErr, "save did not finish")
	requireAppError(t, err, "debate.protected", 403)

	adminSaved, err := NewService(db).SaveWiki(ctx.admin, created.ID, SaveWikiRequest{Title: "Admin", Content: "body", EditSummary: "admin", BaseRevisionID: *created.CurrentRevisionID})
	require.NoError(t, err)
	require.Equal(t, "Admin", adminSaved.Title)
}

type debateConcurrencyMarker struct{}

func newDebateConcurrencyPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	require.NoError(t, adminSQL.Ping())
	schema := "debate_concurrency_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)

	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	query.Set("options", "-c statement_timeout=6000 -c lock_timeout=4000")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = adminSQL.Close()
	})
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Debate{}, &model.Revision{}, &model.ContentProtection{},
		&model.DebateConclusionEvent{}, &model.DebateRevisionReference{}, &model.DebateRelation{}, &model.DebateVote{},
	))
	return db
}

func newDebateContextForDB(t *testing.T, db *gorm.DB) debateTestContext {
	t.Helper()
	users := []model.User{
		{UUID: uuid.New(), Username: "owner-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{UUID: uuid.New(), Username: "editor-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{UUID: uuid.New(), Username: "admin-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true},
	}
	require.NoError(t, db.Create(&users).Error)
	return debateTestContext{
		db: db, service: NewService(db),
		owner:  authctx.CurrentUser{ID: users[0].UUID, Username: users[0].Username, Role: users[0].Role},
		editor: authctx.CurrentUser{ID: users[1].UUID, Username: users[1].Username, Role: users[1].Role},
		admin:  authctx.CurrentUser{ID: users[2].UUID, Username: users[2].Username, Role: users[2].Role},
	}
}

func runConcurrentDebateOperations(t *testing.T, cancel context.CancelFunc, operations ...func() error) []error {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, len(operations))
	for _, operation := range operations {
		operation := operation
		go func() {
			<-start
			results <- operation()
		}()
	}
	close(start)
	errs := make([]error, 0, len(operations))
	for range operations {
		select {
		case err := <-results:
			errs = append(errs, err)
		case <-time.After(9 * time.Second):
			cancel()
			t.Fatal("concurrent debate operations did not finish before timeout")
		}
	}
	return errs
}

func assertNoPostgresDeadlock(t *testing.T, errs []error) {
	t.Helper()
	for _, err := range errs {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "deadlock") {
			t.Fatalf("PostgreSQL deadlock: %v", err)
		}
	}
}

func awaitDebateSignal(t *testing.T, cancel context.CancelFunc, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal(message)
	}
}

func awaitDebateError(t *testing.T, cancel context.CancelFunc, result <-chan error, message string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(8 * time.Second):
		cancel()
		t.Fatal(message)
		return nil
	}
}
