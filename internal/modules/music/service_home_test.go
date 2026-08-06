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

	home, err := service.Home(nil)
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
