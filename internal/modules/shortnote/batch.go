package shortnote

import (
	"fmt"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type voteCounts struct {
	likes    int64
	dislikes int64
	viewer   string
}

type noteAttachmentKey struct {
	typeName string
	id       uuid.UUID
}

func (s *Service) dtoBatch(notes []model.ShortNote, viewerID uuid.UUID) ([]NoteDTO, error) {
	if len(notes) == 0 {
		return []NoteDTO{}, nil
	}
	ids := make([]uuid.UUID, 0, len(notes))
	for _, note := range notes {
		ids = append(ids, note.ID)
	}
	votes, err := s.batchVoteCounts(ids, viewerID)
	if err != nil {
		return nil, err
	}
	commentCounts, err := s.batchCommentCounts(ids)
	if err != nil {
		return nil, err
	}
	attachments, err := s.batchMediaAttachments(notes)
	if err != nil {
		return nil, err
	}

	result := make([]NoteDTO, 0, len(notes))
	for _, note := range notes {
		counts := votes[note.ID]
		media := make([]MediaDTO, 0, len(note.Media))
		for _, item := range note.Media {
			media = append(media, MediaDTO{ID: item.ID, URL: item.URL, Position: item.Position})
		}
		key := noteAttachmentKey{}
		if note.MediaAttachmentType != nil && note.MediaAttachmentID != nil {
			key = noteAttachmentKey{typeName: *note.MediaAttachmentType, id: *note.MediaAttachmentID}
		}
		result = append(result, NoteDTO{
			ID: note.ID, UserID: note.UserID, User: model.PublicUserFromUser(note.User),
			Content: note.Content, Media: media, MediaAttachment: attachments[key],
			LikesCount: counts.likes, DislikesCount: counts.dislikes, VoteScore: counts.likes - counts.dislikes,
			ViewerVote: counts.viewer, CommentsCount: commentCounts[note.ID], Liked: counts.viewer == "up",
			Edited: note.UpdatedAt.After(note.CreatedAt), CreatedAt: note.CreatedAt, UpdatedAt: note.UpdatedAt,
		})
	}
	return result, nil
}

func (s *Service) batchVoteCounts(noteIDs []uuid.UUID, viewerID uuid.UUID) (map[uuid.UUID]voteCounts, error) {
	result := make(map[uuid.UUID]voteCounts, len(noteIDs))
	for _, id := range noteIDs {
		result[id] = voteCounts{viewer: "none"}
	}
	type voteCountRow struct {
		NoteID    uuid.UUID `gorm:"column:short_note_id"`
		Direction string    `gorm:"column:direction"`
		Count     int64     `gorm:"column:count"`
	}
	var explicitCounts []voteCountRow
	if err := s.db.Model(&model.ShortNoteVote{}).
		Select("short_note_id, direction, COUNT(*) AS count").
		Where("short_note_id IN ?", noteIDs).Group("short_note_id, direction").Scan(&explicitCounts).Error; err != nil {
		return nil, err
	}
	for _, row := range explicitCounts {
		counts := result[row.NoteID]
		if row.Direction == "up" {
			counts.likes += row.Count
		} else {
			counts.dislikes += row.Count
		}
		result[row.NoteID] = counts
	}

	viewerVotes := map[uuid.UUID]string{}
	if viewerID != uuid.Nil {
		var rows []voteCountRow
		if err := s.db.Model(&model.ShortNoteVote{}).
			Select("short_note_id, direction").Where("short_note_id IN ? AND user_id = ?", noteIDs, viewerID).
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			viewerVotes[row.NoteID] = row.Direction
		}
	}

	var legacyCounts []voteCountRow
	legacyBase := s.db.Table("likes AS legacy_likes").
		Where("legacy_likes.target_type = ? AND legacy_likes.target_id IN ?", "short_note", noteIDs).
		Where(`NOT EXISTS (SELECT 1 FROM short_note_votes AS explicit_votes WHERE explicit_votes.short_note_id = legacy_likes.target_id AND explicit_votes.user_id = legacy_likes.user_id)`)
	if err := legacyBase.Select("legacy_likes.target_id AS short_note_id, COUNT(*) AS count").
		Group("legacy_likes.target_id").Scan(&legacyCounts).Error; err != nil {
		return nil, err
	}
	for _, row := range legacyCounts {
		counts := result[row.NoteID]
		counts.likes += row.Count
		result[row.NoteID] = counts
	}

	viewerLegacyVotes := map[uuid.UUID]bool{}
	if viewerID != uuid.Nil {
		var ids []uuid.UUID
		if err := s.db.Table("likes AS legacy_likes").
			Where("legacy_likes.target_type = ? AND legacy_likes.target_id IN ? AND legacy_likes.user_id = ?", "short_note", noteIDs, viewerID).
			Where(`NOT EXISTS (SELECT 1 FROM short_note_votes AS explicit_votes WHERE explicit_votes.short_note_id = legacy_likes.target_id AND explicit_votes.user_id = legacy_likes.user_id)`).
			Pluck("legacy_likes.target_id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			viewerLegacyVotes[id] = true
		}
	}
	for id, counts := range result {
		if direction, ok := viewerVotes[id]; ok {
			counts.viewer = direction
		} else if viewerLegacyVotes[id] {
			counts.viewer = "up"
		}
		result[id] = counts
	}
	return result, nil
}

func (s *Service) batchCommentCounts(noteIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	type commentCountRow struct {
		ResourceID uuid.UUID `gorm:"column:resource_id"`
		Count      int       `gorm:"column:comment_count"`
	}
	var rows []commentCountRow
	if err := s.db.Model(&model.DiscussionTarget{}).
		Select("resource_id, comment_count").Where("kind = ? AND resource_id IN ?", "short_note", noteIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]int, len(rows))
	for _, row := range rows {
		result[row.ResourceID] = row.Count
	}
	return result, nil
}

func (s *Service) batchMediaAttachments(notes []model.ShortNote) (map[noteAttachmentKey]*MediaAttachmentDTO, error) {
	idsByType := map[string][]uuid.UUID{"song": {}, "album": {}, "video": {}}
	seen := map[noteAttachmentKey]bool{}
	for _, note := range notes {
		if note.MediaAttachmentType == nil || note.MediaAttachmentID == nil || *note.MediaAttachmentID == uuid.Nil {
			continue
		}
		key := noteAttachmentKey{typeName: *note.MediaAttachmentType, id: *note.MediaAttachmentID}
		if _, ok := idsByType[key.typeName]; !ok || seen[key] {
			continue
		}
		seen[key] = true
		idsByType[key.typeName] = append(idsByType[key.typeName], key.id)
	}
	result := map[noteAttachmentKey]*MediaAttachmentDTO{}

	if ids := idsByType["song"]; len(ids) > 0 {
		var songs []model.Song
		if err := s.db.Preload("Album").Preload("Album.Artists").Preload("Artists").Where("id IN ?", ids).Find(&songs).Error; err != nil {
			return nil, err
		}
		for _, song := range songs {
			result[noteAttachmentKey{typeName: "song", id: song.ID}] = mediaAttachmentFromSong(song)
		}
	}
	if ids := idsByType["album"]; len(ids) > 0 {
		var albums []model.Album
		if err := s.db.Preload("Artists").Preload("Songs").Where("id IN ?", ids).Find(&albums).Error; err != nil {
			return nil, err
		}
		for _, album := range albums {
			result[noteAttachmentKey{typeName: "album", id: album.ID}] = mediaAttachmentFromAlbum(album)
		}
	}
	if ids := idsByType["video"]; len(ids) > 0 {
		var videos []struct {
			ID           uuid.UUID `gorm:"column:id"`
			Title        string    `gorm:"column:title"`
			ThumbnailURL string    `gorm:"column:thumbnail_url"`
			DurationSec  int       `gorm:"column:duration_sec"`
		}
		if err := s.db.Model(&model.Video{}).Select("id, title, thumbnail_url, duration_sec").Where("id IN ?", ids).Find(&videos).Error; err != nil {
			return nil, err
		}
		for _, video := range videos {
			result[noteAttachmentKey{typeName: "video", id: video.ID}] = &MediaAttachmentDTO{
				Type: "video", ID: video.ID, Title: video.Title, CoverURL: video.ThumbnailURL, Duration: video.DurationSec,
			}
		}
	}
	for key := range seen {
		if result[key] == nil {
			result[key] = &MediaAttachmentDTO{Type: key.typeName, ID: key.id}
		}
	}
	return result, nil
}

func mediaAttachmentFromSong(song model.Song) *MediaAttachmentDTO {
	artistName := ""
	if len(song.Artists) > 0 && strings.TrimSpace(song.Artists[0].Name) != "" {
		artistName = strings.TrimSpace(song.Artists[0].Name)
	} else if song.Album != nil && len(song.Album.Artists) > 0 && strings.TrimSpace(song.Album.Artists[0].Name) != "" {
		artistName = strings.TrimSpace(song.Album.Artists[0].Name)
	}
	var albumTitle string
	var albumID *uuid.UUID
	year := 0
	if song.Album != nil {
		albumTitle, albumID, year = strings.TrimSpace(song.Album.Title), song.AlbumID, song.Album.Year
		if year == 0 {
			year = song.Album.ReleaseYear
		}
	}
	if year == 0 && !song.ReleaseDate.IsZero() {
		year = song.ReleaseDate.Year()
	}
	coverURL := song.CoverURL
	if coverURL == "" && song.Album != nil {
		coverURL = song.Album.CoverURL
	}
	parts := make([]string, 0, 2)
	if artistName != "" {
		parts = append(parts, artistName)
	}
	if albumTitle != "" {
		parts = append(parts, albumTitle)
	}
	return &MediaAttachmentDTO{
		Type: "song", ID: song.ID, Title: song.Title, Subtitle: strings.Join(parts, " · "), CoverURL: coverURL,
		AudioURL: song.AudioURL, Duration: song.DurationSec, Artist: artistName, AlbumID: albumID, AlbumTitle: albumTitle, Year: year,
	}
}

func mediaAttachmentFromAlbum(album model.Album) *MediaAttachmentDTO {
	artistName := ""
	if len(album.Artists) > 0 && strings.TrimSpace(album.Artists[0].Name) != "" {
		artistName = strings.TrimSpace(album.Artists[0].Name)
	}
	year := album.Year
	if year == 0 {
		year = album.ReleaseYear
	}
	if year == 0 && !album.ReleaseDate.IsZero() {
		year = album.ReleaseDate.Year()
	}
	parts := make([]string, 0, 3)
	if artistName != "" {
		parts = append(parts, artistName)
	}
	if year > 0 {
		parts = append(parts, fmt.Sprintf("%d", year))
	}
	if len(album.Songs) > 0 {
		parts = append(parts, fmt.Sprintf("%d 首曲目", len(album.Songs)))
	}
	return &MediaAttachmentDTO{
		Type: "album", ID: album.ID, Title: album.Title, Subtitle: strings.Join(parts, " · "),
		CoverURL: album.CoverURL, Artist: artistName, Year: year, SongCount: len(album.Songs),
	}
}
