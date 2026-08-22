package music

import (
	"encoding/json"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestHomeReturnsEmptyPersonalizationForAnonymousListeners(t *testing.T) {
	service, _, _ := newMusicTestService(t)

	home, err := service.Home(nil)
	if err != nil {
		t.Fatalf("load anonymous home: %v", err)
	}
	if home.Personalized || len(home.RecentlyPlayed) != 0 || len(home.ForYou) != 0 {
		t.Fatalf("expected empty anonymous personalization, got %#v", home)
	}
}

func TestRecommendHomeAlbumsFillsWithPopularAlbumsWhenAffinityIsSparse(t *testing.T) {
	service, db, _ := newMusicTestService(t)
	createAlbum := func(title string, artistName string) (model.Album, uuid.UUID) {
		t.Helper()
		artist := model.Artist{Name: artistName, EntryStatus: "open"}
		if err := db.Create(&artist).Error; err != nil {
			t.Fatal(err)
		}
		album := model.Album{Title: title, CoverURL: "/" + title + ".jpg", Status: "open", EntryStatus: "open"}
		if err := db.Create(&album).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.Song{Title: title + " song", AlbumID: &album.ID, AudioURL: "/" + title + ".mp3", Status: "open"}).Error; err != nil {
			t.Fatal(err)
		}
		return album, artist.ID
	}

	related, relatedArtistID := createAlbum("Related", "Affinity Artist")
	for _, name := range []string{"Fallback One", "Fallback Two", "Fallback Three", "Fallback Four", "Fallback Five", "Fallback Six", "Fallback Seven"} {
		createAlbum(name, name+" Artist")
	}

	recommendations, err := service.recommendHomeAlbums(
		map[uuid.UUID]float64{relatedArtistID: 1},
		map[uuid.UUID]struct{}{},
		map[uuid.UUID]struct{}{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recommendations) != musicHomeForYouLimit {
		t.Fatalf("expected %d recommendations after hot fallback, got %d: %#v", musicHomeForYouLimit, len(recommendations), recommendations)
	}
	if recommendations[0].ID != related.ID {
		t.Fatalf("expected personalized recommendation first, got %#v", recommendations[0])
	}
	if recommendations[1].Reason != "热门音乐推荐" {
		t.Fatalf("expected fallback recommendation reason, got %q", recommendations[1].Reason)
	}
}

func TestRecommendHomeAlbumsExcludesAlbumsContainingSeenSongs(t *testing.T) {
	service, db, _ := newMusicTestService(t)
	artist := model.Artist{Name: "Affinity Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatal(err)
	}

	createAlbum := func(title string) (model.Album, model.Song) {
		t.Helper()
		album := model.Album{Title: title, CoverURL: "/" + title + ".jpg", Status: "open", EntryStatus: "open"}
		if err := db.Create(&album).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
			t.Fatal(err)
		}
		song := model.Song{Title: title + " song", AlbumID: &album.ID, AudioURL: "/" + title + ".mp3", Status: "open"}
		if err := db.Create(&song).Error; err != nil {
			t.Fatal(err)
		}
		return album, song
	}

	excludedAlbum, seenSong := createAlbum("Excluded")
	includedAlbum, _ := createAlbum("Included")
	recommendations, err := service.recommendHomeAlbums(
		map[uuid.UUID]float64{artist.ID: 1},
		map[uuid.UUID]struct{}{},
		map[uuid.UUID]struct{}{seenSong.ID: {}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recommendations) != 1 || recommendations[0].ID != includedAlbum.ID {
		t.Fatalf("unexpected recommendations: %#v", recommendations)
	}
	var payload map[string]any
	encoded, err := json.Marshal(recommendations[0])
	if err != nil {
		t.Fatalf("marshal home recommendation: %v", err)
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode home recommendation: %v", err)
	}
	if _, exists := payload["songs"]; exists {
		t.Fatalf("home recommendation must not include song details: %#v", payload)
	}
	for _, recommendation := range recommendations {
		if recommendation.ID == excludedAlbum.ID {
			t.Fatalf("returned album containing a seen song: %s", excludedAlbum.ID)
		}
	}
}

func TestHomeUsesSearchClicksWithoutRecommendingTheOpenedAlbum(t *testing.T) {
	service, db, user := newMusicTestService(t)
	artist := model.Artist{Name: "Search Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatal(err)
	}
	opened := model.Album{Title: "Opened", CoverURL: "/opened.jpg", Status: "open", EntryStatus: "open"}
	candidate := model.Album{Title: "Candidate", CoverURL: "/candidate.jpg", Status: "open", EntryStatus: "open"}
	for _, album := range []*model.Album{&opened, &candidate} {
		if err := db.Create(album).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(album).Association("Artists").Append(&artist); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.Song{Title: album.Title + " song", AlbumID: &album.ID, AudioURL: "/audio.mp3", Status: "open"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.MusicSearchInteraction{UserID: user.ID, Query: "Opened", EntityType: "album", EntityID: opened.ID}).Error; err != nil {
		t.Fatal(err)
	}
	home, err := service.Home(&user)
	if err != nil {
		t.Fatal(err)
	}
	if !home.Personalized || len(home.ForYou) != 1 || home.ForYou[0].ID != candidate.ID {
		t.Fatalf("unexpected search-based recommendations: %#v", home.ForYou)
	}
	if home.ForYou[0].Reason != "基于你与 Search Artist 相关的记录" {
		t.Fatalf("expected item-level recommendation reason, got %q", home.ForYou[0].Reason)
	}
}
