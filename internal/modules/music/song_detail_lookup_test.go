package music

import (
	"errors"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestMusicSongDetailLookupAvoidsRedundantQueryAndPreservesVisibility(t *testing.T) {
	_, db, owner := newMusicHTTPTestService(t)
	active := model.Song{Title: "Public recording", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
	draft := model.Song{Title: "Private draft", Status: "draft", LifecycleStatus: model.MusicLifecycleDraft, UploadedBy: &owner.ID}
	for _, song := range []*model.Song{&active, &draft} {
		if err := db.Create(song).Error; err != nil {
			t.Fatal(err)
		}
	}
	alias := model.Song{Title: "Merged recording", Status: "closed", LifecycleStatus: model.MusicLifecycleMerged, RedirectTo: &active.ID}
	hiddenAlias := model.Song{Title: "Merged to draft", Status: "closed", LifecycleStatus: model.MusicLifecycleMerged, RedirectTo: &draft.ID}
	for _, song := range []*model.Song{&alias, &hiddenAlias} {
		if err := db.Create(song).Error; err != nil {
			t.Fatal(err)
		}
	}
	rootQueries := 0
	if err := db.Callback().Query().After("gorm:query").Register("test:count_song_detail", func(tx *gorm.DB) {
		if tx.Statement.Table == "Songs" {
			rootQueries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	got, err := loadMusicSongDetailRow(db, active.ID, nil)
	if err != nil || got.ID != active.ID || rootQueries != 1 {
		t.Fatalf("active lookup: id=%v err=%v root queries=%d; want one query", got.ID, err, rootQueries)
	}
	admin := owner
	admin.Role = authctx.RoleAdmin
	for _, viewer := range []*authctx.CurrentUser{nil, &owner, &admin} {
		got, err = loadMusicSongDetailRow(db, alias.ID, viewer)
		if err != nil || got.ID != active.ID {
			t.Fatalf("merged alias did not resolve: id=%v err=%v", got.ID, err)
		}
	}
	for _, id := range []uuid.UUID{draft.ID, hiddenAlias.ID, uuid.New()} {
		if _, err := loadMusicSongDetailRow(db, id, nil); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("anonymous lookup of %v must fail closed: %v", id, err)
		}
	}
	got, err = loadMusicSongDetailRow(db, draft.ID, &owner)
	if err != nil || got.ID != draft.ID {
		t.Fatalf("owner draft lookup: id=%v err=%v", got.ID, err)
	}
}
