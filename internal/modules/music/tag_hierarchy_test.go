package music

import (
	"testing"

	"atoman/internal/model"
)

func TestReplaceAlbumImportTagsCreatesStyleParentFromParentName(t *testing.T) {
	service, db, user := newMusicTestService(t)
	_ = service
	album := model.Album{Title: "层级标签专辑", LifecycleStatus: model.MusicLifecycleActive, Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	if err := replaceAlbumImportTags(db, user.ID, album.ID, []AlbumImportTagPayload{
		{Kind: model.MusicTagKindType, Name: "硬核说唱", ParentName: "说唱"},
	}); err != nil {
		t.Fatal(err)
	}
	var tags []model.MusicTag
	if err := db.Order("depth ASC").Find(&tags).Error; err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Name != "说唱" || tags[1].Name != "硬核说唱" || tags[1].ParentID == nil || *tags[1].ParentID != tags[0].ID {
		t.Fatalf("unexpected hierarchy: %#v", tags)
	}
}
