package music

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	revisionservice "atoman/internal/service"

	"gorm.io/gorm"
)

func TestMusicEntryStateRequestsBindRevisionAndAuditTransitions(t *testing.T) {
	service, db, user := newMusicTestService(t)
	if err := db.AutoMigrate(&model.MusicEntryStateRequest{}, &model.MusicEntryStateEvent{}); err != nil {
		t.Fatalf("migrate music state tables: %v", err)
	}
	admin := model.User{Username: "state-admin", Email: "state-admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	artist := model.Artist{
		Name: "Wiki Artist", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive,
		EditStatus: model.MusicEditDevelopment, CreatedBy: &user.ID,
	}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	revisions := revisionservice.NewRevisionService(db)
	if _, err := revisions.EnsureInitialRevision("artist", artist.ID, user.ID); err != nil {
		t.Fatalf("create baseline: %v", err)
	}

	closeRequest, err := service.CreateMusicEntryStateRequest(user, "artist", artist.ID, CreateMusicStateRequestInput{
		Action: model.MusicStateActionClose, Reason: "资料已经稳定",
	})
	if err != nil || closeRequest.BaseRevisionID == nil {
		t.Fatalf("create close request: request=%#v err=%v", closeRequest, err)
	}
	if _, conflicts, err := revisions.CreateRevision("artist", artist.ID, user.ID, map[string]any{"bio": "new information"}, "继续修改", 0, true); err != nil || len(conflicts) != 0 {
		t.Fatalf("apply later revision: conflicts=%v err=%v", conflicts, err)
	}
	if err := db.First(&closeRequest, "id = ?", closeRequest.ID).Error; err != nil {
		t.Fatalf("reload close request: %v", err)
	}
	if closeRequest.Status != model.MusicStateRequestSuperseded {
		t.Fatalf("close request status=%q, want superseded", closeRequest.Status)
	}

	if err := db.Model(&artist).Update("edit_status", model.MusicEditLocked).Error; err != nil {
		t.Fatalf("lock artist: %v", err)
	}
	unlockRequest, err := service.CreateMusicEntryStateRequest(user, "artist", artist.ID, CreateMusicStateRequestInput{
		Action: model.MusicStateActionUnlock, Reason: "争议已经解决",
	})
	if err != nil {
		t.Fatalf("create unlock request: %v", err)
	}
	adminUser := authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: authctx.RoleAdmin}
	if _, err := service.ReviewMusicEntryStateRequest(adminUser, unlockRequest.ID, ReviewMusicStateRequestInput{Decision: model.MusicStateRequestApproved, Reason: "同意解除"}); err != nil {
		t.Fatalf("approve unlock request: %v", err)
	}
	if err := db.First(&artist, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if artist.EditStatus != model.MusicEditDevelopment {
		t.Fatalf("artist edit status=%q, want development", artist.EditStatus)
	}
	var eventCount int64
	if err := db.Model(&model.MusicEntryStateEvent{}).Where("request_id = ? AND trigger = ?", unlockRequest.ID, "request").Count(&eventCount).Error; err != nil {
		t.Fatalf("count state events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("state event count=%d, want 1", eventCount)
	}
}

func TestMusicEntryStateRequestRejectsAdminSelfReview(t *testing.T) {
	service, db, _ := newMusicTestService(t)
	if err := db.AutoMigrate(&model.MusicEntryStateRequest{}, &model.MusicEntryStateEvent{}); err != nil {
		t.Fatalf("migrate music state tables: %v", err)
	}
	admin := model.User{Username: "self-review-admin", Email: "self-review@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	adminUser := authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: authctx.RoleAdmin}
	artist := model.Artist{Name: "Admin Artist", LifecycleStatus: model.MusicLifecycleActive, EditStatus: model.MusicEditLocked, CreatedBy: &admin.UUID}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	request, err := service.CreateMusicEntryStateRequest(adminUser, "artist", artist.ID, CreateMusicStateRequestInput{Action: model.MusicStateActionUnlock, Reason: "请求解除"})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	_, err = service.ReviewMusicEntryStateRequest(adminUser, request.ID, ReviewMusicStateRequestInput{Decision: model.MusicStateRequestApproved, Reason: "自行同意"})
	if err == nil {
		t.Fatal("expected self review to be rejected")
	}
	var stored model.MusicEntryStateRequest
	if err := db.First(&stored, "id = ?", request.ID).Error; err != nil && err != gorm.ErrRecordNotFound {
		t.Fatalf("reload request: %v", err)
	}
	if stored.Status != model.MusicStateRequestPending {
		t.Fatalf("request status=%q, want pending", stored.Status)
	}
}
