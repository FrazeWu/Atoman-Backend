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
