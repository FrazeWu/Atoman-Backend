package music

import (
	"errors"
	"testing"
	"time"

	"atoman/internal/model"

	"gorm.io/gorm"
)

func completeArtistDraftRequest(name string) CreateArtistRequest {
	return CreateArtistRequest{
		Name:        name,
		LegalName:   name,
		ImageURL:    "/artist.jpg",
		Nationality: "CN",
		BirthDate:   "1990-01-01",
		Sources:     []Source{{Title: "artist source"}},
	}
}

func TestCreateArtistCreatesOwnedDraftsWithSameName(t *testing.T) {
	service, _, user := newMusicTestService(t)
	first, err := service.CreateArtist(user, completeArtistDraftRequest("Same Name"))
	if err != nil {
		t.Fatalf("create first artist: %v", err)
	}
	if first.EntryStatus != artistEntryDraft || first.CreatedBy == nil || *first.CreatedBy != user.ID {
		t.Fatalf("expected owned draft, got %#v", first)
	}

	second, err := service.CreateArtist(user, completeArtistDraftRequest("Same Name"))
	if err != nil {
		t.Fatalf("create second artist with same name: %v", err)
	}
	if second.ID == first.ID || second.Name != first.Name {
		t.Fatalf("expected distinct same-name artists, got first=%#v second=%#v", first, second)
	}
}

func TestCommitFirstAlbumPublishesReferencedOwnedDrafts(t *testing.T) {
	service, db, user := newMusicTestService(t)
	primary, err := service.CreateArtist(user, completeArtistDraftRequest("Primary Draft"))
	if err != nil {
		t.Fatalf("create primary draft: %v", err)
	}
	featured, err := service.CreateArtist(user, CreateArtistRequest{Name: "Featured Draft", DraftContext: "member"})
	if err != nil {
		t.Fatalf("create featured draft: %v", err)
	}
	session, err := service.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatalf("create import session: %v", err)
	}
	seedReadyImportMedia(t, db, session.ID, "https://cdn.test/first.jpg", "First Track")

	_, err = service.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artists: []CommitAlbumImportArtistInput{
			{ArtistID: primary.ID.String(), Roles: []AlbumArtistRoleInput{{Role: "primary"}}},
			{ArtistID: featured.ID.String(), Roles: []AlbumArtistRoleInput{{Role: "featured"}}},
		},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
		Album: AlbumImportAlbumPayload{
			Title: "First Album", CoverURL: "https://cdn.test/first.jpg", ReleaseDate: "2020-01-01",
			Tracks: []AlbumImportTrackPayload{{Title: "First Track", TrackNumber: 1}},
		},
	})
	if err != nil {
		t.Fatalf("commit first album: %v", err)
	}
	if err := db.First(&primary, "id = ?", primary.ID).Error; err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	if err := db.First(&featured, "id = ?", featured.ID).Error; err != nil {
		t.Fatalf("reload featured: %v", err)
	}
	if primary.EntryStatus != artistEntryOpen || featured.EntryStatus != artistEntryOpen {
		t.Fatalf("unexpected artist states: primary=%s featured=%s", primary.EntryStatus, featured.EntryStatus)
	}
}

func TestCleanupExpiredArtistDraftsDeletesOnlyUnreferencedDrafts(t *testing.T) {
	_, db, user := newMusicTestService(t)
	old := time.Now().Add(-8 * 24 * time.Hour)
	orphan := model.Artist{Name: "Expired Orphan", EntryStatus: artistEntryDraft, CreatedBy: &user.ID}
	referenced := model.Artist{Name: "Referenced Draft", EntryStatus: artistEntryDraft, CreatedBy: &user.ID}
	member := model.Artist{Name: "Member Draft", EntryStatus: artistEntryDraft, CreatedBy: &user.ID}
	group := model.Artist{Name: "Group Draft", EntryStatus: artistEntryDraft, CreatedBy: &user.ID}
	for _, artist := range []*model.Artist{&orphan, &referenced, &member, &group} {
		if err := db.Create(artist).Error; err != nil {
			t.Fatalf("create draft: %v", err)
		}
		if err := db.Model(artist).Update("created_at", old).Error; err != nil {
			t.Fatalf("age draft: %v", err)
		}
	}
	album := model.Album{Title: "Draft Reference", Status: "open", EntryStatus: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&referenced); err != nil {
		t.Fatalf("link album: %v", err)
	}
	if err := db.Create(&model.ArtistMember{GroupArtistID: group.ID, MemberArtistID: member.ID}).Error; err != nil {
		t.Fatalf("link member: %v", err)
	}

	if err := cleanupExpiredArtistDrafts(db, time.Now()); err != nil {
		t.Fatalf("cleanup drafts: %v", err)
	}
	if err := db.First(&orphan, "id = ?", orphan.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected orphan deleted, got %v", err)
	}
	for _, artist := range []*model.Artist{&referenced, &member, &group} {
		if err := db.First(artist, "id = ?", artist.ID).Error; err != nil {
			t.Fatalf("referenced draft was deleted: %v", err)
		}
	}
}
