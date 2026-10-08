package music

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type asyncMetadataTestEnricher struct {
	calls atomic.Int32
	fn    func(context.Context, AlbumImportMetadataInput) (AlbumImportMetadataResult, error)
}

func (enricher *asyncMetadataTestEnricher) Enrich(ctx context.Context, input AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
	enricher.calls.Add(1)
	if enricher.fn != nil {
		return enricher.fn(ctx, input)
	}
	return asyncMetadataResult(input), nil
}

func asyncMetadataResult(input AlbumImportMetadataInput) AlbumImportMetadataResult {
	return AlbumImportMetadataResult{
		AlbumTitle: input.AlbumTitle, MatchStatus: model.MusicMatchMatched,
		MetadataSource: "musicbrainz", ExternalID: "async-test-release", Tracks: baseMetadataTracks(input.Tracks),
	}
}

func newAsyncMetadataTestSession(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser, model.AlbumImportSession, *asyncMetadataTestEnricher) {
	t.Helper()
	svc, db, user := newMusicTestService(t)
	if err := db.AutoMigrate(&model.MusicMetadataMatchJob{}); err != nil {
		t.Fatal(err)
	}
	enricher := &asyncMetadataTestEnricher{}
	svc.WithAlbumImportMetadataEnricher(enricher)
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatal(err)
	}
	return svc, db, user, session, enricher
}

func asyncMetadataInput(title string) AlbumImportMetadataPreviewInput {
	return AlbumImportMetadataPreviewInput{Async: true, AlbumTitle: title, Artist: "测试艺人", TrackTitles: []string{"测试曲目"}}
}

func TestAsyncMetadataMatchEnqueuesWithoutExternalLookupAndWorkerCompletes(t *testing.T) {
	svc, db, user, session, enricher := newAsyncMetadataTestSession(t)
	queued, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, asyncMetadataInput("异步专辑"))
	if err != nil {
		t.Fatal(err)
	}
	values, err := readAlbumImportPayloadMap(queued.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	if stringValue(values["metadata_match_status"]) != "matching" || enricher.calls.Load() != 0 {
		t.Fatalf("提交应只入队：status=%v, external calls=%d", values["metadata_match_status"], enricher.calls.Load())
	}
	var count int64
	if err := db.Model(&model.MusicMetadataMatchJob{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("持久任务 count=%d, err=%v", count, err)
	}
	// 用新 Service 模拟 API 请求结束和进程重建，任务不能依赖请求 goroutine。
	worker := NewService(db).WithAlbumImportMetadataEnricher(enricher)
	worked, err := worker.RunMetadataMatchOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("worker result=%v, %v", worked, err)
	}
	_, values = loadCreationRegressionPayload(t, db, session.ID)
	if stringValue(values["metadata_match_status"]) != model.MusicMatchMatched || stringValue(values["derived_album_title"]) != "异步专辑" {
		t.Fatalf("后台结果没有持久保存：%#v", values)
	}
	if enricher.calls.Load() != 1 {
		t.Fatalf("external calls=%d, want 1", enricher.calls.Load())
	}
	worked, err = worker.RunMetadataMatchOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("已完成任务再次执行：worked=%v, err=%v", worked, err)
	}
}

func TestAsyncMetadataMatchDuplicatePendingInputCreatesOneJob(t *testing.T) {
	svc, db, user, session, enricher := newAsyncMetadataTestSession(t)
	input := asyncMetadataInput("重复专辑")
	for range 2 {
		if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&model.MusicMetadataMatchJob{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("重复请求创建任务 count=%d, err=%v", count, err)
	}
	_, values := loadCreationRegressionPayload(t, db, session.ID)
	if int64Value(values["metadata_match_generation"]) != 1 || enricher.calls.Load() != 0 {
		t.Fatalf("重复请求递增generation或调用外部：generation=%v, calls=%d", values["metadata_match_generation"], enricher.calls.Load())
	}
}

func TestAsyncMetadataMatchCannotQueueAnotherUsersSession(t *testing.T) {
	svc, db, _, session, enricher := newAsyncMetadataTestSession(t)
	_, err := svc.MatchAlbumImportMetadata(context.Background(), authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}, session.ID, asyncMetadataInput("越权专辑"))
	if err == nil {
		t.Fatal("另一用户可以匹配别人的导入")
	}
	var count int64
	if err := db.Model(&model.MusicMetadataMatchJob{}).Count(&count).Error; err != nil || count != 0 || enricher.calls.Load() != 0 {
		t.Fatalf("越权请求产生了任务：count=%d, calls=%d, err=%v", count, enricher.calls.Load(), err)
	}
}

func TestAsyncMetadataMatchCanceledSessionSkipsExternalLookup(t *testing.T) {
	svc, db, user, session, enricher := newAsyncMetadataTestSession(t)
	if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, asyncMetadataInput("已取消专辑")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelAlbumImportSession(user, session.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := loadCreationRegressionPayload(t, db, session.ID)
	if _, err := svc.RunMetadataMatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := loadCreationRegressionPayload(t, db, session.ID)
	if enricher.calls.Load() != 0 || after.PayloadJSON != before.PayloadJSON || after.Status != AlbumImportStatusCanceled {
		t.Fatalf("取消后仍匹配或写回：calls=%d, status=%s", enricher.calls.Load(), after.Status)
	}
}

func TestAsyncMetadataMatchForceRetryPreventsOldGenerationOverwrite(t *testing.T) {
	svc, db, user, session, enricher := newAsyncMetadataTestSession(t)
	started := make(chan struct{})
	release := make(chan struct{})
	enricher.fn = func(ctx context.Context, input AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
		if input.AlbumTitle == "旧专辑" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return AlbumImportMetadataResult{}, ctx.Err()
			}
		}
		return asyncMetadataResult(input), nil
	}
	if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, asyncMetadataInput("旧专辑")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := svc.RunMetadataMatchOnce(ctx)
		done <- err
	}()
	waitAsyncMetadataSignal(t, started)
	input := asyncMetadataInput("新专辑")
	input.Force = true
	if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, input); err != nil {
		t.Fatal(err)
	}
	_, values := loadCreationRegressionPayload(t, db, session.ID)
	if int64Value(values["metadata_match_generation"]) != 2 {
		t.Fatalf("force没有递增generation：%#v", values)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("旧worker未结束")
	}
	_, values = loadCreationRegressionPayload(t, db, session.ID)
	if stringValue(values["derived_album_title"]) == "旧专辑" || stringValue(values["metadata_match_status"]) != "matching" {
		t.Fatalf("旧结果覆盖了新请求：%#v", values)
	}
	if worked, err := svc.RunMetadataMatchOnce(context.Background()); err != nil || !worked {
		t.Fatalf("重试任务未执行：worked=%v, err=%v", worked, err)
	}
	_, values = loadCreationRegressionPayload(t, db, session.ID)
	if stringValue(values["derived_album_title"]) != "新专辑" || stringValue(values["metadata_match_status"]) != model.MusicMatchMatched {
		t.Fatalf("新结果未保存：%#v", values)
	}
}

func TestAsyncMetadataMatchCanceledWorkerPersistsEndedStatus(t *testing.T) {
	svc, db, user, session, enricher := newAsyncMetadataTestSession(t)
	started := make(chan struct{})
	enricher.fn = func(ctx context.Context, _ AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
		close(started)
		<-ctx.Done()
		return AlbumImportMetadataResult{}, ctx.Err()
	}
	if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, asyncMetadataInput("超时专辑")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.RunMetadataMatchOnce(ctx)
	}()
	waitAsyncMetadataSignal(t, started)
	cancel()
	waitAsyncMetadataSignal(t, done)
	_, values := loadCreationRegressionPayload(t, db, session.ID)
	if stringValue(values["metadata_match_status"]) == "matching" || stringValue(values["metadata_error"]) == "" {
		t.Fatalf("取消后永久matching或没有错误反馈：%#v", values)
	}
}

func TestAsyncMetadataMatchCancelSessionStopsRunningExternalLookup(t *testing.T) {
	svc, db, user, session, enricher := newAsyncMetadataTestSession(t)
	started := make(chan struct{})
	externalCanceled := make(chan struct{})
	enricher.fn = func(ctx context.Context, _ AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
		close(started)
		<-ctx.Done()
		close(externalCanceled)
		return AlbumImportMetadataResult{}, ctx.Err()
	}
	if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, asyncMetadataInput("主动取消专辑")); err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.RunMetadataMatchOnce(workerCtx)
	}()
	waitAsyncMetadataSignal(t, started)
	if _, err := svc.CancelAlbumImportSession(user, session.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := loadCreationRegressionPayload(t, db, session.ID)
	select {
	case <-externalCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("用户取消导入后，外部匹配调用未及时取消")
	}
	waitAsyncMetadataSignal(t, done)
	after, _ := loadCreationRegressionPayload(t, db, session.ID)
	if after.Status != AlbumImportStatusCanceled || after.PayloadJSON != before.PayloadJSON {
		t.Fatalf("运行中匹配覆盖了取消状态：status=%s", after.Status)
	}
}

func TestMetadataMatchWorkerConsumesPersistedJobAndStopsCleanly(t *testing.T) {
	svc, db, user, session, enricher := newAsyncMetadataTestSession(t)
	started := make(chan struct{})
	enricher.fn = func(_ context.Context, input AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
		close(started)
		return asyncMetadataResult(input), nil
	}
	if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, asyncMetadataInput("后台循环专辑")); err != nil {
		t.Fatal(err)
	}
	worker := NewService(db).WithAlbumImportMetadataEnricher(enricher)
	ctx, cancel := context.WithCancel(context.Background())
	done := StartMetadataMatchWorker(ctx, worker, 10*time.Millisecond)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("取消后后台匹配循环未及时退出")
		}
	})
	waitAsyncMetadataSignal(t, started)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var persisted model.AlbumImportSession
		if err := db.First(&persisted, "id = ?", session.ID).Error; err != nil {
			t.Fatal(err)
		}
		values, err := readAlbumImportPayloadMap(persisted.PayloadJSON)
		if err != nil {
			t.Fatal(err)
		}
		if stringValue(values["metadata_match_status"]) == model.MusicMatchMatched {
			if stringValue(values["derived_album_title"]) != "后台循环专辑" {
				t.Fatalf("循环没有持久保存匹配结果：%#v", values)
			}
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("后台匹配循环未持久保存结果")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("完成任务后取消，后台循环未及时退出")
	}
	if enricher.calls.Load() != 1 {
		t.Fatalf("持久任务重复执行：external calls=%d", enricher.calls.Load())
	}
}

func waitAsyncMetadataSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("等待元数据任务超时")
	}
}
