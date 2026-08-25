package feed

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
)

func TestRecordContentFeedbackValidatesAndIsIdempotent(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var item model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&item).Updates(map[string]any{"reader_source": "feed", "reader_version": 2}).Error; err != nil {
		t.Fatal(err)
	}

	created, err := service.RecordContentFeedback(user, item.ID, "layout", "rss")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first feedback should be created")
	}
	created, err = service.RecordContentFeedback(user, item.ID, "layout", "rss")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("duplicate feedback should be idempotent")
	}

	var feedback model.FeedContentFeedback
	if err := db.First(&feedback, "feed_item_id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if feedback.ReaderSource != "rss" || feedback.ReaderVersion != 2 {
		t.Fatalf("feedback snapshot=%+v", feedback)
	}

	if _, err := service.RecordContentFeedback(user, item.ID, "other", "rss"); err == nil {
		t.Fatal("unsupported feedback kind should fail")
	}
	if _, err := service.RecordContentFeedback(authctx.CurrentUser{}, item.ID, "missing", "rss"); err == nil {
		t.Fatal("anonymous feedback should fail")
	}
}
