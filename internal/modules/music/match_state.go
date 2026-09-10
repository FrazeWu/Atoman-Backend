package music

import (
	"sort"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func hydrateAlbumMatchStates(db *gorm.DB, albums []model.Album) error {
	if len(albums) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(albums))
	for _, album := range albums {
		ids = append(ids, album.ID)
	}
	records, err := loadMusicMatchRecords(db, "album", ids)
	if err != nil {
		return err
	}
	for index := range albums {
		applyAlbumMatchState(&albums[index], chooseMusicMatchRecord(records[albums[index].ID]))
	}
	return nil
}

func hydrateSongMatchStates(db *gorm.DB, songs []model.Song) error {
	if len(songs) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(songs))
	for _, song := range songs {
		ids = append(ids, song.ID)
	}
	records, err := loadMusicMatchRecords(db, "song", ids)
	if err != nil {
		return err
	}
	for index := range songs {
		applySongMatchState(&songs[index], chooseMusicMatchRecord(records[songs[index].ID]))
	}
	return nil
}

func loadMusicMatchRecords(db *gorm.DB, entityType string, ids []uuid.UUID) (map[uuid.UUID][]model.MusicMatchRecord, error) {
	result := make(map[uuid.UUID][]model.MusicMatchRecord, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var records []model.MusicMatchRecord
	if err := db.Where("entity_type = ? AND entity_id IN ?", entityType, ids).Find(&records).Error; err != nil {
		return nil, err
	}
	for _, record := range records {
		result[record.EntityID] = append(result[record.EntityID], record)
	}
	return result, nil
}

func chooseMusicMatchRecord(records []model.MusicMatchRecord) *model.MusicMatchRecord {
	if len(records) == 0 {
		return nil
	}
	sort.SliceStable(records, func(left, right int) bool {
		if records[left].UserOverridden != records[right].UserOverridden {
			return records[left].UserOverridden
		}
		if musicMatchStatusRank(records[left].Status) != musicMatchStatusRank(records[right].Status) {
			return musicMatchStatusRank(records[left].Status) > musicMatchStatusRank(records[right].Status)
		}
		if musicMatchProviderRank(records[left].Provider) != musicMatchProviderRank(records[right].Provider) {
			return musicMatchProviderRank(records[left].Provider) > musicMatchProviderRank(records[right].Provider)
		}
		return records[left].UpdatedAt.After(records[right].UpdatedAt)
	})
	return &records[0]
}

func musicMatchStatusRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case model.MusicMatchManual:
		return 4
	case model.MusicMatchMatched:
		return 3
	case model.MusicMatchAmbiguous:
		return 2
	default:
		return 1
	}
}

func musicMatchProviderRank(provider string) int {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "discogs":
		return 3
	case "musicbrainz":
		return 2
	default:
		return 1
	}
}

func applyAlbumMatchState(album *model.Album, record *model.MusicMatchRecord) {
	if album == nil || record == nil {
		return
	}
	album.MatchStatus = record.Status
	album.MatchProvider = record.Provider
	album.MatchExternalID = record.ExternalID
	album.MatchSourceURL = record.SourceURL
	album.MatchConfidence = record.Confidence
	album.MatchUserOverridden = record.UserOverridden
}

func applySongMatchState(song *model.Song, record *model.MusicMatchRecord) {
	if song == nil || record == nil {
		return
	}
	song.MatchStatus = record.Status
	song.MatchProvider = record.Provider
	song.MatchExternalID = record.ExternalID
	song.MatchSourceURL = record.SourceURL
	song.MatchConfidence = record.Confidence
	song.MatchUserOverridden = record.UserOverridden
}
