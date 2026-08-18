package music

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type fakeAlbumLinkSuggestionProvider struct {
	releases []MusicBrainzReleaseCandidate
}

func (p fakeAlbumLinkSuggestionProvider) FindArtist(_ context.Context, _ string) (MusicBrainzArtistCandidate, error) {
	return MusicBrainzArtistCandidate{}, nil
}

func (p fakeAlbumLinkSuggestionProvider) ListArtistReleases(_ context.Context, _ string, _ int) ([]MusicBrainzReleaseCandidate, error) {
	return p.releases, nil
}

func TestRegisterRoutesAlbumLinkSuggestionsMatchMusicBrainzSources(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	musicBrainzArtistID := uuid.NewString()
	localReleaseID := uuid.NewString()
	externalReleaseID := uuid.NewString()

	artistSources, err := json.Marshal([]model.MusicSource{{
		Type: "url",
		URL:  "https://musicbrainz.org/artist/" + musicBrainzArtistID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	artist := model.Artist{Name: "Matching Artist", EntryStatus: "open", SourcesJSON: string(artistSources)}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	albumSources, err := json.Marshal([]model.MusicSource{{
		Type: "url",
		URL:  "https://musicbrainz.org/release/" + localReleaseID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	album := model.Album{Title: "Matched locally", EntryStatus: "open", Status: "open", SourcesJSON: string(albumSources)}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	service.WithAlbumLinkSuggestionProvider(fakeAlbumLinkSuggestionProvider{releases: []MusicBrainzReleaseCandidate{
		{ReleaseID: localReleaseID, Title: "Matched release", SourceURL: "https://musicbrainz.org/release/" + localReleaseID},
		{ReleaseID: externalReleaseID, Title: "External release", SourceURL: "https://musicbrainz.org/release/" + externalReleaseID},
	}})

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodGet, "/api/v1/music/artists/"+artist.ID.String()+"/album-link-suggestions", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			MetadataStatus string `json:"metadata_status"`
			LocalMatches   []struct {
				Album struct {
					ID string `json:"id"`
				} `json:"album"`
				MatchKind string `json:"match_kind"`
			} `json:"local_matches"`
			ExternalOnly []struct {
				ReleaseID string `json:"release_id"`
			} `json:"external_only"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.MetadataStatus != "ready" || len(payload.Data.LocalMatches) != 1 || payload.Data.LocalMatches[0].Album.ID != album.ID.String() || payload.Data.LocalMatches[0].MatchKind != "musicbrainz_release" {
		t.Fatalf("unexpected local matches: %#v", payload.Data)
	}
	if len(payload.Data.ExternalOnly) != 1 || payload.Data.ExternalOnly[0].ReleaseID != externalReleaseID {
		t.Fatalf("unexpected external matches: %#v", payload.Data.ExternalOnly)
	}
}
