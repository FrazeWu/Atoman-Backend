package lifecycle

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type lifecyclePostgresMarker string

type lifecyclePostgresFixture struct {
	lifecycleFixture
	dsn string
}

type lifecyclePostgresWorker struct {
	db              *gorm.DB
	applicationName string
	pid             int
}

const (
	lifecyclePostgresOldWorker lifecyclePostgresMarker = "old-worker"
	lifecyclePostgresNewWorker lifecyclePostgresMarker = "new-worker"
)

func TestDispatchPendingPublicationsPostgresWorkersClaimAndNotifyOnce(t *testing.T) {
	fixture := newLifecyclePostgresFixture(t)
	prepareLifecyclePostgresRecipient(t, fixture)
	require.NoError(t, fixture.service.EnqueuePublication("blog", fixture.post.ID))
	firstWorker := fixture.newWorker(t, "claim")
	secondWorker := fixture.newWorker(t, "claim")
	require.NotEqual(t, firstWorker.pid, secondWorker.pid, "workers must use distinct PostgreSQL backend connections")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	firstCandidateLoaded := make(chan struct{})
	bothCandidatesLoaded := make(chan struct{})
	releaseFirstCandidate := make(chan struct{})
	var candidates atomic.Int32
	var successfulClaims atomic.Int32
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirstCandidate) }) }
	t.Cleanup(release)

	callback := "test:lifecycle-postgres-concurrent-candidates:" + uuid.NewString()
	queryCallback := func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentPublicationEvent{}).TableName() || tx.Statement.SQL.String() == "" {
			return
		}
		switch candidates.Add(1) {
		case 1:
			close(firstCandidateLoaded)
			select {
			case <-bothCandidatesLoaded:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		case 2:
			close(bothCandidatesLoaded)
			select {
			case <-releaseFirstCandidate:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		}
	}
	require.NoError(t, firstWorker.db.Callback().Query().After("gorm:query").Register(callback, queryCallback))
	require.NoError(t, secondWorker.db.Callback().Query().After("gorm:query").Register(callback, queryCallback))
	t.Cleanup(func() {
		_ = firstWorker.db.Callback().Query().Remove(callback)
		_ = secondWorker.db.Callback().Query().Remove(callback)
	})
	claimCallback := "test:lifecycle-postgres-successful-claim:" + uuid.NewString()
	claimUpdateCallback := func(tx *gorm.DB) {
		if tx.Error != nil || tx.RowsAffected != 1 || tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentPublicationEvent{}).TableName() {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]any)
		if ok && updates["status"] == "processing" && updates["lease_version"] != nil {
			successfulClaims.Add(1)
		}
	}
	require.NoError(t, firstWorker.db.Callback().Update().After("gorm:update").Register(claimCallback, claimUpdateCallback))
	require.NoError(t, secondWorker.db.Callback().Update().After("gorm:update").Register(claimCallback, claimUpdateCallback))
	t.Cleanup(func() {
		_ = firstWorker.db.Callback().Update().Remove(claimCallback)
		_ = secondWorker.db.Callback().Update().Remove(claimCallback)
	})

	results := make(chan error, 2)
	go func() { results <- NewService(firstWorker.db.WithContext(ctx)).DispatchPendingPublications(1) }()
	awaitLifecyclePostgres(t, ctx, firstCandidateLoaded, "first worker did not load publication candidate")
	go func() { results <- NewService(secondWorker.db.WithContext(ctx)).DispatchPendingPublications(1) }()
	awaitLifecyclePostgres(t, ctx, bothCandidatesLoaded, "second worker did not load publication candidate")
	release()
	for range 2 {
		require.NoError(t, awaitLifecyclePostgresError(ctx, results, "concurrent publication worker did not finish"))
	}

	assertLifecyclePostgresSingleDelivery(t, fixture)
	require.EqualValues(t, 1, successfulClaims.Load(), "exactly one worker must successfully claim the event")
	var event model.ContentPublicationEvent
	require.NoError(t, fixture.db.Where("content_type = ? AND content_id = ?", "blog", fixture.post.ID).First(&event).Error)
	require.EqualValues(t, 1, event.LeaseVersion, "a concurrent dispatch must increment the lease exactly once")
}

func TestDispatchPendingPublicationsPostgresLeaseLockPreventsRecoveryInterleaving(t *testing.T) {
	fixture := newLifecyclePostgresFixture(t)
	prepareLifecyclePostgresRecipient(t, fixture)
	oldWorker := fixture.newWorker(t, "lease-old")
	newWorker := fixture.newWorker(t, "lease-new")
	require.NotEqual(t, oldWorker.pid, newWorker.pid, "workers must use distinct PostgreSQL backend connections")
	event := model.ContentPublicationEvent{ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID, ContentType: "blog", ContentID: fixture.post.ID, Status: "pending"}
	require.NoError(t, fixture.db.Create(&event).Error)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	oldBeforeLeaseLock := make(chan struct{})
	oldLeaseLocked := make(chan struct{})
	releaseBeforeLeaseLock := make(chan struct{})
	releaseLeaseLock := make(chan struct{})
	newReclaimStarted := make(chan struct{})
	var releaseBeforeOnce, releaseLeaseOnce sync.Once
	releaseBeforeLock := func() { releaseBeforeOnce.Do(func() { close(releaseBeforeLeaseLock) }) }
	releaseLease := func() { releaseLeaseOnce.Do(func() { close(releaseLeaseLock) }) }
	t.Cleanup(func() {
		releaseBeforeLock()
		releaseLease()
	})

	pauseBeforeLock := "test:lifecycle-postgres-pause-before-lease-lock:" + uuid.NewString()
	require.NoError(t, oldWorker.db.Callback().Query().After("gorm:query").Register(pauseBeforeLock, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(lifecyclePostgresMarker("worker")) != lifecyclePostgresOldWorker || tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentNotificationPreference{}).TableName() || tx.Statement.SQL.String() == "" {
			return
		}
		select {
		case <-oldBeforeLeaseLock:
			return
		default:
			close(oldBeforeLeaseLock)
		}
		select {
		case <-releaseBeforeLeaseLock:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}))
	t.Cleanup(func() { _ = oldWorker.db.Callback().Query().Remove(pauseBeforeLock) })

	leaseLock := "test:lifecycle-postgres-hold-lease-lock:" + uuid.NewString()
	require.NoError(t, oldWorker.db.Callback().Query().After("gorm:query").Register(leaseLock, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(lifecyclePostgresMarker("worker")) != lifecyclePostgresOldWorker || tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentPublicationEvent{}).TableName() {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		select {
		case <-oldLeaseLocked:
			return
		default:
			close(oldLeaseLocked)
		}
		select {
		case <-releaseLeaseLock:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}))
	t.Cleanup(func() { _ = oldWorker.db.Callback().Query().Remove(leaseLock) })

	reclaimAttempt := "test:lifecycle-postgres-reclaim-attempt:" + uuid.NewString()
	require.NoError(t, newWorker.db.Callback().Update().Before("gorm:update").Register(reclaimAttempt, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(lifecyclePostgresMarker("worker")) != lifecyclePostgresNewWorker || tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentPublicationEvent{}).TableName() {
			return
		}
		select {
		case <-newReclaimStarted:
		default:
			close(newReclaimStarted)
		}
	}))
	t.Cleanup(func() { _ = newWorker.db.Callback().Update().Remove(reclaimAttempt) })

	oldCtx := context.WithValue(ctx, lifecyclePostgresMarker("worker"), lifecyclePostgresOldWorker)
	oldResult := make(chan error, 1)
	go func() { oldResult <- NewService(oldWorker.db.WithContext(oldCtx)).DispatchPendingPublications(1) }()
	awaitLifecyclePostgres(t, ctx, oldBeforeLeaseLock, "old worker did not reach notification dispatch")
	require.NoError(t, fixture.db.Model(&model.ContentPublicationEvent{}).Where("id = ?", event.ID).Update("updated_at", time.Now().UTC().Add(-publicationProcessingTimeout-time.Second)).Error)

	// Allow the old worker to acquire the lease-row lock, then hold that transaction open.
	releaseBeforeLock()
	awaitLifecyclePostgres(t, ctx, oldLeaseLocked, "old worker did not acquire notification lease lock")

	newCtx := context.WithValue(ctx, lifecyclePostgresMarker("worker"), lifecyclePostgresNewWorker)
	newResult := make(chan error, 1)
	go func() { newResult <- NewService(newWorker.db.WithContext(newCtx)).DispatchPendingPublications(1) }()
	awaitLifecyclePostgres(t, ctx, newReclaimStarted, "recovery worker did not attempt lease reclaim")
	awaitLifecyclePostgresReclaimBlocked(t, ctx, fixture, oldWorker, newWorker)

	// PostgreSQL confirms the recovery UPDATE is blocked on the old transaction's lock.
	releaseLease()
	require.NoError(t, awaitLifecyclePostgresError(ctx, oldResult, "old worker did not finish"))
	require.NoError(t, awaitLifecyclePostgresError(ctx, newResult, "recovery worker did not finish"))
	assertLifecyclePostgresSingleDelivery(t, fixture)
}

func TestContentPublicationDispatchCandidateIndexExistsInPostgres(t *testing.T) {
	fixture := newLifecyclePostgresFixture(t)
	expectedIndexName := "idx_cpe_expected_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, fixture.db.Exec(`CREATE INDEX `+expectedIndexName+`
		ON content_publication_events (created_at, id)
		WHERE deleted_at IS NULL AND status IN ('pending', 'processing')`).Error)

	var predicates struct {
		Actual   string
		Expected string
	}
	require.NoError(t, fixture.db.Raw(`
		SELECT
			pg_get_expr(actual_index.indpred, actual_index.indrelid) AS actual,
			pg_get_expr(expected_index.indpred, expected_index.indrelid) AS expected
		FROM pg_index actual_index
		JOIN pg_class actual_relation ON actual_relation.oid = actual_index.indexrelid
		JOIN pg_class actual_table ON actual_table.oid = actual_index.indrelid
		JOIN pg_namespace actual_schema ON actual_schema.oid = actual_relation.relnamespace
		JOIN pg_namespace actual_table_schema ON actual_table_schema.oid = actual_table.relnamespace
		JOIN pg_index expected_index ON true
		JOIN pg_class expected_relation ON expected_relation.oid = expected_index.indexrelid
		JOIN pg_class expected_table ON expected_table.oid = expected_index.indrelid
		JOIN pg_namespace expected_schema ON expected_schema.oid = expected_relation.relnamespace
		JOIN pg_namespace expected_table_schema ON expected_table_schema.oid = expected_table.relnamespace
		WHERE actual_relation.relname = ?
			AND actual_table.relname = 'content_publication_events'
			AND actual_schema.nspname = current_schema()
			AND actual_table_schema.nspname = current_schema()
			AND expected_relation.relname = ?
			AND expected_table.relname = 'content_publication_events'
			AND expected_schema.nspname = current_schema()
			AND expected_table_schema.nspname = current_schema()
	`, "idx_content_publication_events_dispatch_candidates", expectedIndexName).Scan(&predicates).Error)
	require.NotEmpty(t, predicates.Expected)
	require.Equal(t, predicates.Expected, predicates.Actual)
	var keyColumns string
	require.NoError(t, fixture.db.Raw(`
		SELECT string_agg(attribute.attname, ',' ORDER BY key_column.ordinality)
		FROM pg_index index_definition
		JOIN pg_class index_relation ON index_relation.oid = index_definition.indexrelid
		JOIN pg_class table_relation ON table_relation.oid = index_definition.indrelid
		JOIN pg_namespace index_schema ON index_schema.oid = index_relation.relnamespace
		JOIN pg_namespace table_schema ON table_schema.oid = table_relation.relnamespace
		JOIN LATERAL unnest(index_definition.indkey) WITH ORDINALITY AS key_column(attnum, ordinality) ON true
		JOIN pg_attribute attribute ON attribute.attrelid = table_relation.oid AND attribute.attnum = key_column.attnum
		WHERE index_relation.relname = 'idx_content_publication_events_dispatch_candidates'
			AND table_relation.relname = 'content_publication_events'
			AND index_schema.nspname = current_schema()
			AND table_schema.nspname = current_schema()
	`).Scan(&keyColumns).Error)
	require.Equal(t, "created_at,id", keyColumns)
}

func newLifecyclePostgresFixture(t *testing.T) lifecyclePostgresFixture {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminSQL.Close() })
	require.NoError(t, adminSQL.Ping())
	schema := "lifecycle_concurrency_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error })

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
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PodcastEpisode{}, &model.Video{},
		&model.ContentLifecycleEvent{}, &model.ContentProgress{}, &model.ContentNotificationPreference{},
		&model.ContentPublicationEvent{}, &model.FeedSource{}, &model.Subscription{}, &model.Follow{}, &model.Notification{},
	))
	require.NoError(t, migrations.RunNotificationDMIndexes(db))
	require.NoError(t, migrations.RunContentPublicationEventIndexes(db))

	owner := model.User{Username: "lifecycle-pg-owner-" + uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "hash", IsActive: true}
	viewer := model.User{Username: "lifecycle-pg-viewer-" + uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&viewer).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Lifecycle PostgreSQL", Slug: "lifecycle-pg-" + uuid.NewString()[:8]}
	require.NoError(t, db.Create(&channel).Error)
	collection := model.Collection{ChannelID: channel.ID, ContentType: "blog", Name: "Articles"}
	require.NoError(t, db.Create(&collection).Error)
	now := time.Now().UTC()
	post := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, CollectionID: &collection.ID, Title: "Lifecycle PostgreSQL article", Content: "body", Status: "published", Visibility: "public", PublishedAt: &now}
	require.NoError(t, db.Create(&post).Error)
	return lifecyclePostgresFixture{lifecycleFixture: lifecycleFixture{db: db, service: NewService(db), channel: channel, post: post, owner: authctx.CurrentUser{ID: owner.UUID, Username: owner.Username, Role: authctx.RoleUser}, viewer: authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser}}, dsn: parsed.String()}
}

func (fixture lifecyclePostgresFixture) newWorker(t *testing.T, role string) lifecyclePostgresWorker {
	t.Helper()
	applicationName := "lifecycle-concurrency-" + role + "-" + uuid.NewString()
	parsed, err := url.Parse(fixture.dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("application_name", applicationName)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	var pid int
	require.NoError(t, db.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
	return lifecyclePostgresWorker{db: db, applicationName: applicationName, pid: pid}
}

func prepareLifecyclePostgresRecipient(t *testing.T, fixture lifecyclePostgresFixture) {
	t.Helper()
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &fixture.channel.ID, Hash: uuid.NewString(), Title: fixture.channel.Name}
	require.NoError(t, fixture.db.Create(&source).Error)
	require.NoError(t, fixture.db.Create(&model.Subscription{UserID: fixture.viewer.ID, FeedSourceID: source.ID}).Error)
	_, err := fixture.service.SaveNotificationPreference(fixture.viewer, NotificationPreferenceInput{SourceType: "internal_channel", SourceID: fixture.channel.ID, Mode: "all"})
	require.NoError(t, err)
}

func assertLifecyclePostgresSingleDelivery(t *testing.T, fixture lifecyclePostgresFixture) {
	t.Helper()
	var notifications int64
	require.NoError(t, fixture.db.Model(&model.Notification{}).Where("recipient_id = ? AND type = ?", fixture.viewer.ID, "content_published").Count(&notifications).Error)
	require.EqualValues(t, 1, notifications)
	var event model.ContentPublicationEvent
	require.NoError(t, fixture.db.Where("content_type = ? AND content_id = ?", "blog", fixture.post.ID).First(&event).Error)
	require.Equal(t, "delivered", event.Status)
}

func awaitLifecyclePostgresReclaimBlocked(t *testing.T, ctx context.Context, fixture lifecyclePostgresFixture, oldWorker, newWorker lifecyclePostgresWorker) {
	t.Helper()
	for {
		var newWorkerBlocked bool
		require.NoError(t, fixture.db.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE pid = ?
					AND datname = current_database()
					AND wait_event_type = 'Lock'
					AND ? = ANY(pg_blocking_pids(pid))
			)
		`, newWorker.pid, oldWorker.pid).Scan(&newWorkerBlocked).Error)

		var oldWorkerHoldsLeaseLock bool
		require.NoError(t, fixture.db.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks held_lock
				JOIN pg_class relation ON relation.oid = held_lock.relation
				JOIN pg_namespace schema ON schema.oid = relation.relnamespace
				JOIN pg_stat_activity holder ON holder.pid = held_lock.pid
				WHERE held_lock.pid = ?
					AND held_lock.granted
					AND holder.xact_start IS NOT NULL
					AND schema.nspname = current_schema()
					AND relation.relname = 'content_publication_events'
			)
		`, oldWorker.pid).Scan(&oldWorkerHoldsLeaseLock).Error)
		if newWorkerBlocked && oldWorkerHoldsLeaseLock {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("recovery UPDATE did not enter PostgreSQL lock wait: " + ctx.Err().Error())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func awaitLifecyclePostgres(t *testing.T, ctx context.Context, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal(message + ": " + ctx.Err().Error())
	}
}

func awaitLifecyclePostgresError(ctx context.Context, result <-chan error, message string) error {
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
