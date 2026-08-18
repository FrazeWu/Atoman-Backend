package music

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const albumLinkSuggestionLimit = 30

type MusicBrainzArtistCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type MusicBrainzReleaseCandidate struct {
	ReleaseID      string   `json:"release_id"`
	ReleaseGroupID string   `json:"release_group_id,omitempty"`
	Title          string   `json:"title"`
	ReleaseDate    string   `json:"release_date,omitempty"`
	ArtistNames    []string `json:"artist_names,omitempty"`
	SourceURL      string   `json:"source_url"`
}

// AlbumLinkSuggestionProvider keeps MusicBrainz I/O outside the request handler
// and makes matching deterministic in service tests.
type AlbumLinkSuggestionProvider interface {
	FindArtist(context.Context, string) (MusicBrainzArtistCandidate, error)
	ListArtistReleases(context.Context, string, int) ([]MusicBrainzReleaseCandidate, error)
}

type AlbumLinkSuggestion struct {
	Album         model.Album                 `json:"album"`
	MusicBrainz   MusicBrainzReleaseCandidate `json:"musicbrainz"`
	AlreadyLinked bool                        `json:"already_linked"`
	MatchKind     string                      `json:"match_kind"`
}

type AlbumLinkSuggestionResponse struct {
	LocalMatches   []AlbumLinkSuggestion         `json:"local_matches"`
	ExternalOnly   []MusicBrainzReleaseCandidate `json:"external_only"`
	MetadataStatus string                        `json:"metadata_status"`
}

func (s *Service) AlbumLinkSuggestions(ctx context.Context, viewer *authctx.CurrentUser, artistID uuid.UUID) (AlbumLinkSuggestionResponse, error) {
	response := AlbumLinkSuggestionResponse{
		LocalMatches:   []AlbumLinkSuggestion{},
		ExternalOnly:   []MusicBrainzReleaseCandidate{},
		MetadataStatus: "unavailable",
	}

	var artist model.Artist
	artistQuery := scopeVisibleMusicEntries(s.db.WithContext(ctx), "\"Artists\"", "created_by", viewer, false)
	if err := artistQuery.First(&artist, "\"Artists\".id = ?", artistID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return response, apperr.NotFound("music.artist_not_found", "Artist not found")
		}
		return response, err
	}
	if s.albumLinkSuggestions == nil {
		return response, nil
	}

	musicBrainzArtistID := musicBrainzIDFromSources(artist.Sources, "artist")
	if musicBrainzArtistID == "" {
		candidate, err := s.albumLinkSuggestions.FindArtist(ctx, artist.Name)
		if err != nil {
			response.MetadataStatus = "unavailable"
			return response, nil
		}
		musicBrainzArtistID = candidate.ID
	}
	if musicBrainzArtistID == "" {
		return response, nil
	}

	releases, err := s.albumLinkSuggestions.ListArtistReleases(ctx, musicBrainzArtistID, albumLinkSuggestionLimit)
	if err != nil {
		response.MetadataStatus = "unavailable"
		return response, nil
	}
	response.MetadataStatus = "ready"
	releases = uniqueMusicBrainzReleases(releases)
	if len(releases) == 0 {
		return response, nil
	}

	albums, err := s.findAlbumsByMusicBrainzReleases(ctx, viewer, releases)
	if err != nil {
		return response, err
	}
	matchedReleaseIDs := make(map[string]bool, len(releases))
	for _, album := range albums {
		matchedRelease, matchKind := matchingMusicBrainzRelease(album.Sources, releases)
		if matchedRelease.ReleaseID == "" && matchedRelease.ReleaseGroupID == "" {
			continue
		}
		matchedReleaseIDs[musicBrainzReleaseKey(matchedRelease)] = true
		response.LocalMatches = append(response.LocalMatches, AlbumLinkSuggestion{
			Album:         album,
			MusicBrainz:   matchedRelease,
			AlreadyLinked: albumHasArtist(album, artistID),
			MatchKind:     matchKind,
		})
	}
	for _, release := range releases {
		if !matchedReleaseIDs[musicBrainzReleaseKey(release)] {
			response.ExternalOnly = append(response.ExternalOnly, release)
		}
	}
	return response, nil
}

func (s *Service) findAlbumsByMusicBrainzReleases(ctx context.Context, viewer *authctx.CurrentUser, releases []MusicBrainzReleaseCandidate) ([]model.Album, error) {
	ids := make([]string, 0, len(releases)*2)
	seen := map[string]bool{}
	for _, release := range releases {
		for _, id := range []string{release.ReleaseID, release.ReleaseGroupID} {
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return []model.Album{}, nil
	}

	query := scopeVisibleMusicEntries(s.db.WithContext(ctx).Model(&model.Album{}), "\"Albums\"", "uploaded_by", viewer, false).
		Preload("Artists")
	conditions := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		conditions = append(conditions, "CAST(sources_json AS TEXT) LIKE ?")
		args = append(args, "%"+id+"%")
	}
	var albums []model.Album
	if err := query.Where("("+strings.Join(conditions, " OR ")+")", args...).Find(&albums).Error; err != nil {
		return nil, err
	}
	return albums, nil
}

func musicBrainzIDFromSources(sources []model.MusicSource, kind string) string {
	for _, source := range sources {
		parsed, err := url.Parse(strings.TrimSpace(source.URL))
		host := strings.ToLower(parsed.Hostname())
		if err != nil || (host != "musicbrainz.org" && host != "www.musicbrainz.org") {
			continue
		}
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) != 2 || segments[0] != kind {
			continue
		}
		parsedID, parseErr := uuid.Parse(segments[1])
		if parseErr == nil {
			return parsedID.String()
		}
	}
	return ""
}

func matchingMusicBrainzRelease(sources []model.MusicSource, releases []MusicBrainzReleaseCandidate) (MusicBrainzReleaseCandidate, string) {
	for _, source := range sources {
		for _, kind := range []string{"release", "release-group"} {
			id := musicBrainzIDFromSources([]model.MusicSource{source}, kind)
			if id == "" {
				continue
			}
			for _, release := range releases {
				if id == release.ReleaseID {
					return release, "musicbrainz_release"
				}
				if id == release.ReleaseGroupID {
					return release, "musicbrainz_release_group"
				}
			}
		}
	}
	return MusicBrainzReleaseCandidate{}, ""
}

func albumHasArtist(album model.Album, artistID uuid.UUID) bool {
	for _, artist := range album.Artists {
		if artist.ID == artistID {
			return true
		}
	}
	return false
}

func uniqueMusicBrainzReleases(releases []MusicBrainzReleaseCandidate) []MusicBrainzReleaseCandidate {
	unique := make([]MusicBrainzReleaseCandidate, 0, len(releases))
	seen := map[string]bool{}
	for _, release := range releases {
		if strings.TrimSpace(release.ReleaseID) == "" || strings.TrimSpace(release.Title) == "" {
			continue
		}
		key := musicBrainzReleaseKey(release)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, release)
	}
	return unique
}

func musicBrainzReleaseKey(release MusicBrainzReleaseCandidate) string {
	if release.ReleaseGroupID != "" {
		return "group:" + release.ReleaseGroupID
	}
	return "release:" + release.ReleaseID
}

func (e *ExternalAlbumMetadataEnricher) FindArtist(ctx context.Context, name string) (MusicBrainzArtistCandidate, error) {
	artistID, err := e.findExactArtistID(ctx, name)
	if err != nil {
		return MusicBrainzArtistCandidate{}, err
	}
	return MusicBrainzArtistCandidate{ID: artistID, Name: name}, nil
}

func (e *ExternalAlbumMetadataEnricher) ListArtistReleases(ctx context.Context, artistID string, limit int) ([]MusicBrainzReleaseCandidate, error) {
	if e == nil || e.musicBrainzBase == "" || e.userAgent == "" {
		return nil, errors.New("MusicBrainz is not configured")
	}
	parsedArtistID, err := uuid.Parse(strings.TrimSpace(artistID))
	if err != nil {
		return nil, fmt.Errorf("invalid MusicBrainz artist id")
	}
	if limit < 1 {
		limit = albumLinkSuggestionLimit
	}
	if limit > 100 {
		limit = 100
	}
	var response struct {
		Releases []musicBrainzRelease `json:"releases"`
	}
	endpoint := e.musicBrainzBase + "/ws/2/release?fmt=json&limit=" + fmt.Sprint(limit) + "&artist=" + url.QueryEscape(parsedArtistID.String()) + "&inc=release-groups+artist-credits"
	if err := e.musicBrainzJSON(ctx, endpoint, &response); err != nil {
		return nil, err
	}
	candidates := make([]MusicBrainzReleaseCandidate, 0, len(response.Releases))
	for _, release := range response.Releases {
		if release.ID == "" || release.Title == "" {
			continue
		}
		candidates = append(candidates, MusicBrainzReleaseCandidate{
			ReleaseID:      release.ID,
			ReleaseGroupID: release.ReleaseGroup.ID,
			Title:          release.Title,
			ReleaseDate:    release.Date,
			ArtistNames:    musicBrainzReleaseArtistNames(release),
			SourceURL:      e.musicBrainzBase + "/release/" + release.ID,
		})
	}
	return candidates, nil
}
