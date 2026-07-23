package dm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestReportMessageCapturesMessageSnapshot(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMImage{}, &model.DMMessageReport{}); err != nil {
		t.Fatal(err)
	}
	reporter, sender := testUser(t, db), testUser(t, db)
	conversation := createReportConversation(t, db, reporter, sender)
	image := model.DMImage{UploadedByUserID: sender, ObjectKey: "images/sender/message.png", ContentType: "image/png", SizeBytes: 12}
	if err := db.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	message := createReportMessage(t, db, conversation.ID, sender, "abusive content", &image.ID)
	service := NewService(NewRepo(db), nil, nil, nil)

	receipt, err := service.ReportMessage(context.Background(), authctx.CurrentUser{ID: reporter}, message.ID, ReportInput{Reason: "harassment", Detail: "context"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != model.DMReportPending {
		t.Fatalf("unexpected public receipt: %#v", receipt)
	}

	var report model.DMMessageReport
	if err := db.First(&report, "message_id = ? AND reporter_user_id = ?", message.ID, reporter).Error; err != nil {
		t.Fatal(err)
	}
	if report.MessageID != message.ID || report.ReporterUserID != reporter || report.ReportedActorUserID != sender || report.SnapshotContent != message.Content || report.SnapshotImageKey != image.ObjectKey {
		t.Fatalf("unexpected report snapshot: %#v", report)
	}
}

func TestReportMessageRequiresConversationAccessAndOtherActor(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMMessageReport{}); err != nil {
		t.Fatal(err)
	}
	reporter, sender, stranger := testUser(t, db), testUser(t, db), testUser(t, db)
	conversation := createReportConversation(t, db, reporter, sender)
	otherMessage := createReportMessage(t, db, conversation.ID, sender, "other", nil)
	ownMessage := createReportMessage(t, db, conversation.ID, reporter, "own", nil)
	service := NewService(NewRepo(db), nil, nil, nil)

	_, err := service.ReportMessage(context.Background(), authctx.CurrentUser{ID: reporter}, ownMessage.ID, ReportInput{Reason: "spam"})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected own message denial, got %v", err)
	}
	_, err = service.ReportMessage(context.Background(), authctx.CurrentUser{ID: stranger}, otherMessage.ID, ReportInput{Reason: "spam"})
	if !errors.Is(err, ErrConversationForbidden) {
		t.Fatalf("expected stranger denial, got %v", err)
	}
	_, err = service.ReportMessage(context.Background(), authctx.CurrentUser{ID: reporter}, otherMessage.ID, ReportInput{Reason: "invalid"})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected invalid reason denial, got %v", err)
	}
	_, err = service.ReportMessage(context.Background(), authctx.CurrentUser{ID: reporter}, otherMessage.ID, ReportInput{Reason: "other", Detail: strings.Repeat("界", 1001)})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected oversized detail denial, got %v", err)
	}
}

func TestReportMessageRejectsDuplicate(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMMessageReport{}); err != nil {
		t.Fatal(err)
	}
	reporter, sender := testUser(t, db), testUser(t, db)
	conversation := createReportConversation(t, db, reporter, sender)
	message := createReportMessage(t, db, conversation.ID, sender, "reported", nil)
	service := NewService(NewRepo(db), nil, nil, nil)

	if _, err := service.ReportMessage(context.Background(), authctx.CurrentUser{ID: reporter}, message.ID, ReportInput{Reason: "spam"}); err != nil {
		t.Fatal(err)
	}
	_, err := service.ReportMessage(context.Background(), authctx.CurrentUser{ID: reporter}, message.ID, ReportInput{Reason: "spam"})
	if !errors.Is(err, ErrAlreadyReported) {
		t.Fatalf("expected duplicate report denial, got %v", err)
	}
}

func TestReportMessageRejectsChannelOwnersOwnChannelMessage(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMMessageReport{}); err != nil {
		t.Fatal(err)
	}
	visitor, owner := testUser(t, db), testUser(t, db)
	channelID := uuid.New()
	if err := db.Create(&model.Channel{Base: model.Base{ID: channelID}, UserID: &owner, Name: "report-channel", Slug: "report-channel"}).Error; err != nil {
		t.Fatal(err)
	}
	conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: visitor, ParticipantBType: model.DMPartyChannel, ParticipantB: channelID}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	message := createReportMessage(t, db, conversation.ID, owner, "channel response", nil)
	service := NewService(NewRepo(db), nil, nil, nil)

	_, err := service.ReportMessage(context.Background(), authctx.CurrentUser{ID: owner}, message.ID, ReportInput{Reason: "spam"})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected owner self-report denial, got %v", err)
	}
	if _, err := service.ReportMessage(context.Background(), authctx.CurrentUser{ID: visitor}, message.ID, ReportInput{Reason: "spam"}); err != nil {
		t.Fatal(err)
	}
}

func TestListReportsAndReviewReportRequireAdminAndOnlyReviewPending(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&model.DMMessageReport{}); err != nil {
		t.Fatal(err)
	}
	reporter, sender, moderator := testUser(t, db), testUser(t, db), testUser(t, db)
	conversation := createReportConversation(t, db, reporter, sender)
	first := createReportMessage(t, db, conversation.ID, sender, "first", nil)
	second := createReportMessage(t, db, conversation.ID, sender, "second", nil)
	service := NewService(NewRepo(db), nil, nil, nil)
	for _, message := range []model.DMMessage{first, second} {
		if _, err := service.ReportMessage(context.Background(), authctx.CurrentUser{ID: reporter}, message.ID, ReportInput{Reason: "privacy"}); err != nil {
			t.Fatal(err)
		}
	}

	_, err := service.ListReports(context.Background(), authctx.CurrentUser{ID: reporter, Role: authctx.RoleUser}, "", 1)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected user list denial, got %v", err)
	}
	page, err := service.ListReports(context.Background(), authctx.CurrentUser{ID: moderator, Role: authctx.RoleAdmin}, "", 1)
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].HasSnapshotImage || page.Items[0].ReportedActorUserID != sender.String() {
		t.Fatalf("unexpected admin report page: %#v %v", page, err)
	}
	if _, err := service.ListReports(context.Background(), authctx.CurrentUser{ID: moderator, Role: authctx.RoleOwner}, page.NextCursor, 1); err != nil {
		t.Fatal(err)
	}

	_, err = service.ReviewReport(context.Background(), authctx.CurrentUser{ID: reporter, Role: authctx.RoleUser}, uuid.MustParse(page.Items[0].ID), ReviewReportInput{Status: model.DMReportResolved})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected user review denial, got %v", err)
	}
	reviewed, err := service.ReviewReport(context.Background(), authctx.CurrentUser{ID: moderator, Role: authctx.RoleAdmin}, uuid.MustParse(page.Items[0].ID), ReviewReportInput{Status: model.DMReportResolved})
	if err != nil || reviewed.Status != model.DMReportResolved || reviewed.ReviewedByUserID == nil || *reviewed.ReviewedByUserID != moderator.String() || reviewed.ReviewedAt == nil {
		t.Fatalf("unexpected review result: %#v %v", reviewed, err)
	}
	_, err = service.ReviewReport(context.Background(), authctx.CurrentUser{ID: moderator, Role: authctx.RoleAdmin}, uuid.MustParse(page.Items[0].ID), ReviewReportInput{Status: model.DMReportDismissed})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected repeat review denial, got %v", err)
	}
}

func createReportConversation(t *testing.T, db *gorm.DB, first, second uuid.UUID) model.DMConversation {
	t.Helper()
	conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: first, ParticipantBType: model.DMPartyUser, ParticipantB: second}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	return conversation
}

func createReportMessage(t *testing.T, db *gorm.DB, conversationID, actorID uuid.UUID, content string, imageID *uuid.UUID) model.DMMessage {
	t.Helper()
	message := model.DMMessage{ConversationID: conversationID, SenderType: model.DMPartyUser, SenderID: actorID, ActorUserID: actorID, ClientMessageID: uuid.New(), Content: content, ImageID: imageID}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	return message
}
