package reference

import (
	"testing"

	"atoman/internal/model"

	"github.com/stretchr/testify/require"
)

func TestSearchPatternsEscapeLikeWildcards(t *testing.T) {
	if got, want := searchContainsPattern(`A_b%\\c`), `%a\_b\%\\\\c%`; got != want {
		t.Fatalf("searchContainsPattern() = %q, want %q", got, want)
	}
	if got, want := searchPrefixPattern(`A_b%\\c`), `a\_b\%\\\\c%`; got != want {
		t.Fatalf("searchPrefixPattern() = %q, want %q", got, want)
	}
}

func TestEscapedSearchTermNormalizesWhitespaceAndCase(t *testing.T) {
	if got, want := escapedSearchTerm("  YeS  "), "yes"; got != want {
		t.Fatalf("escapedSearchTerm() = %q, want %q", got, want)
	}
}

func TestIsUUIDOrPrefix(t *testing.T) {
	cases := []struct {
		input    string
		isUUID   bool
		isPrefix bool
	}{
		{"123e4567-e89b-12d3-a456-426614174000", true, false},
		{"123e4567-e89b", false, true},
		{"hello", false, false},
		{"1234", false, true},
	}
	for _, tc := range cases {
		gotUUID, gotPrefix := inspectUUIDQuery(tc.input)
		if gotUUID != tc.isUUID || gotPrefix != tc.isPrefix {
			t.Errorf("inspectUUIDQuery(%q) = (%v, %v), want (%v, %v)", tc.input, gotUUID, gotPrefix, tc.isUUID, tc.isPrefix)
		}
	}
}

func TestRegistrySearchMusicAndVideoByUUIDAndName(t *testing.T) {
	db, ids := seedReferenceRegistry(t)
	registry := NewRegistry(db)

	songID := ids["song"]
	albumID := ids["album"]
	videoID := ids["video"]

	// 1. Song search
	items, err := registry.Search(Viewer{}, "song", "Song Title", 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, songID, items[0].ID)

	items, err = registry.Search(Viewer{}, "song", songID.String(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, songID, items[0].ID)

	songPrefix := songID.String()[:8]
	items, err = registry.Search(Viewer{}, "song", songPrefix, 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, songID, items[0].ID)

	// 2. Album search
	items, err = registry.Search(Viewer{}, "album", "Album Title", 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, albumID, items[0].ID)

	items, err = registry.Search(Viewer{}, "album", albumID.String(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, albumID, items[0].ID)

	albumPrefix := albumID.String()[:8]
	items, err = registry.Search(Viewer{}, "album", albumPrefix, 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, albumID, items[0].ID)

	// 3. Video search
	items, err = registry.Search(Viewer{}, "video", "Video Title", 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, videoID, items[0].ID)

	items, err = registry.Search(Viewer{}, "video", videoID.String(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, videoID, items[0].ID)

	videoPrefix := videoID.String()[:8]
	items, err = registry.Search(Viewer{}, "video", videoPrefix, 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, videoID, items[0].ID)

	// 4. Ranking test: full UUID takes top priority
	otherSong := model.Song{Title: "Song Other", AudioURL: "other.mp3"}
	require.NoError(t, db.Create(&otherSong).Error)

	items, err = registry.Search(Viewer{}, "song", otherSong.ID.String(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, otherSong.ID, items[0].ID)
}
