package music

import (
	"atoman/internal/model"
	"encoding/json"
	"github.com/google/uuid"
	"time"
)

type artistListReference struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name,omitempty"`
	ImageURL    string    `json:"image_url,omitempty"`
}

type albumListReference struct {
	ID                   uuid.UUID `json:"id"`
	Title                string    `json:"title"`
	CoverURL             string    `json:"cover_url"`
	AlbumType            string    `json:"album_type"`
	ReleaseDate          time.Time `json:"release_date"`
	ReleaseDatePrecision string    `json:"release_date_precision,omitempty"`
	MatchStatus          string    `json:"match_status,omitempty"`
	MatchProvider        string    `json:"match_provider,omitempty"`
	LifecycleStatus      string    `json:"lifecycle_status"`
	EditStatus           string    `json:"edit_status"`
}

type songListCredit struct {
	model.SongArtist
	Artist *artistListReference `json:"artist,omitempty"`
}

type songListSummary struct {
	model.Song
	Lyrics        *string               `json:"lyrics,omitempty"`
	WaveformPeaks *json.RawMessage      `json:"waveform_peaks,omitempty"`
	Description   *string               `json:"description,omitempty"`
	Album         *albumListReference   `json:"album,omitempty"`
	Artists       []artistListReference `json:"artists,omitempty"`
	ArtistCredits []songListCredit      `json:"artist_credits,omitempty"`
	SummaryOnly   bool                  `json:"summary_only"`
}

func summarizeSongs(songs []model.Song) []songListSummary {
	result := make([]songListSummary, 0, len(songs))
	for _, song := range songs {
		dto := songListSummary{Song: song, SummaryOnly: true}
		for _, artist := range song.Artists {
			dto.Artists = append(dto.Artists, artistListReference{artist.ID, artist.Name, artist.DisplayName, artist.ImageURL})
		}
		for _, credit := range song.ArtistCredits {
			item := songListCredit{SongArtist: credit}
			if artist := credit.Artist; artist != nil {
				item.Artist = &artistListReference{artist.ID, artist.Name, artist.DisplayName, artist.ImageURL}
			}
			dto.ArtistCredits = append(dto.ArtistCredits, item)
		}
		if album := song.Album; album != nil {
			dto.Album = &albumListReference{ID: album.ID, Title: album.Title, CoverURL: album.CoverURL, AlbumType: album.AlbumType, ReleaseDate: album.ReleaseDate, ReleaseDatePrecision: album.ReleaseDatePrecision, MatchStatus: album.MatchStatus, MatchProvider: album.MatchProvider, LifecycleStatus: album.LifecycleStatus, EditStatus: album.EditStatus}
		}
		result = append(result, dto)
	}
	return result
}

const songSummarySelection = `"Songs".id, "Songs".created_at, "Songs".updated_at, "Songs".title, "Songs".release_type, "Songs".release_date, "Songs".release_date_precision, "Songs".track_number, "Songs".disc_number, "Songs".audio_url, "Songs".audio_status, "Songs".cover_url, "Songs".sources_json, "Songs".status, "Songs".lifecycle_status, "Songs".edit_status, "Songs".album_id, "Songs".uploaded_by, "Songs".play_count, "Songs".duration_sec`
