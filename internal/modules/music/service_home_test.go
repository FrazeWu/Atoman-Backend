package music

import (
	"testing"

	"atoman/internal/model"
)

func TestIsDiscoverableHomeAlbum(t *testing.T) {
	tests := []struct {
		name  string
		album model.Album
		want  bool
	}{
		{
			name: "includes complete album",
			album: model.Album{
				CoverURL: "https://assets.example.test/cover.webp",
				Songs:    []model.Song{{AudioURL: "https://assets.example.test/track.mp3"}},
			},
			want: true,
		},
		{
			name: "excludes album without cover",
			album: model.Album{
				Songs: []model.Song{{AudioURL: "https://assets.example.test/track.mp3"}},
			},
			want: false,
		},
		{
			name: "excludes album without playable songs",
			album: model.Album{
				CoverURL: "https://assets.example.test/cover.webp",
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isDiscoverableHomeAlbum(test.album); got != test.want {
				t.Fatalf("isDiscoverableHomeAlbum() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHomeReturnsPublicSectionsForAnonymousListeners(t *testing.T) {
	service, db, _ := newMusicTestService(t)
	artist := model.Artist{Name: "Discovery Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{Title: "Discovery Album", CoverURL: "https://assets.example.test/cover.webp", Status: "open", EntryStatus: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("link album artist: %v", err)
	}
	if err := db.Create(&model.Song{Title: "Discovery Track", AudioURL: "https://assets.example.test/track.mp3", AlbumID: &album.ID, Status: "open"}).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	home, err := service.Home(nil, 1, 24)
	if err != nil {
		t.Fatalf("load anonymous home: %v", err)
	}
	if home.Personalized || len(home.Sections) == 0 {
		t.Fatalf("expected non-personalized public sections, got %#v", home)
	}
	for _, section := range home.Sections {
		if len(section.Albums) == 0 || section.Albums[0].ID != album.ID {
			t.Fatalf("unexpected section %#v", section)
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
	home, err := service.Home(&user, 1, 24)
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
