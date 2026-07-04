package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/feed"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db                   *gorm.DB
	repo                 *Repo
	s3                   *s3.S3
	albumImportMultipart albumImportMultipartStore
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

func NewServiceWithS3(db *gorm.DB, s3Client *s3.S3) *Service {
	return &Service{
		db:                   db,
		repo:                 NewRepo(db),
		s3:                   s3Client,
		albumImportMultipart: newS3AlbumImportMultipartStore(s3Client),
	}
}

func (s *Service) RecommendAlbumsByMode(mode recommendation.Mode, page int, pageSize int) ([]feed.RecommendationItemDTO, int64, error) {
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)

	var albums []model.Album
	if err := s.db.Model(&model.Album{}).
		Preload("Songs").
		Where("COALESCE(\"Albums\".entry_status, '') <> ? AND COALESCE(\"Albums\".status, '') <> ?", "closed", "closed").
		Order("\"Albums\".created_at DESC").
		Find(&albums).Error; err != nil {
		return nil, 0, err
	}

	albumIDs := make([]uuid.UUID, 0, len(albums))
	for _, album := range albums {
		albumIDs = append(albumIDs, album.ID)
	}
	albumBookmarkCounts := map[uuid.UUID]int64{}
	if len(albumIDs) > 0 {
		var bookmarkRows []struct {
			AlbumID uuid.UUID
			Count   int64
		}
		if err := s.db.Model(&model.AlbumBookmark{}).
			Select("album_id, COUNT(*) AS count").
			Where("album_id IN ?", albumIDs).
			Group("album_id").
			Scan(&bookmarkRows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range bookmarkRows {
			albumBookmarkCounts[row.AlbumID] = row.Count
		}
	}

	candidates := make([]recommendation.Candidate, 0, len(albums))
	albumByID := make(map[string]model.Album, len(albums))
	for _, album := range albums {
		qualityScore := normalizeMusicDiscoverQuality(album.HotScore)
		candidates = append(candidates, recommendation.Candidate{
			Module:          "music",
			EntityType:      recommendation.EntityAlbum,
			EntityID:        album.ID.String(),
			SourceKey:       album.ID.String(),
			QualityScore:    qualityScore,
			TrendScore:      clampMusicRecommendation(album.HotScore / 10),
			FreshnessScore:  normalizeMusicAlbumFreshness(album.CreatedAt, 30*24*time.Hour),
			AuthorityScore:  0.5,
			ExposureScore:   0,
			EditorialScore:  0,
			PublishedAtUnix: album.CreatedAt.Unix(),
		})
		albumByID[album.ID.String()] = album
	}

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]feed.RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		album, ok := albumByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, feed.RecommendationItemDTO{
			ID:         album.ID.String(),
			Title:      album.Title,
			Summary:    "",
			ImageURL:   album.CoverURL,
			TargetPath: "/music/album/" + album.ID.String(),
			ScoreLabel: fmt.Sprintf("%s %.0f", musicRecommendationLabel(mode), math.Round(item.FinalScore*100)),
			PlayCount: func() int64 {
				var total int64
				for _, song := range album.Songs {
					total += song.PlayCount
				}
				return total
			}(),
			BookmarkCount: albumBookmarkCounts[album.ID],
		})
	}

	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

type artistWithHotScore struct {
	model.Artist
	MaxHotScore float64
	AlbumCount  int64
}

func (s *Service) RecommendArtistsByMode(mode recommendation.Mode, page int, pageSize int) ([]feed.RecommendationItemDTO, int64, error) {
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)

	var dbArtists []artistWithHotScore
	if err := s.db.Table("Artists").
		Select("\"Artists\".*, COALESCE(MAX(a.hot_score), 0) as max_hot_score, COUNT(a.id) as album_count").
		Joins("LEFT JOIN album_artists aa ON aa.artist_id = \"Artists\".id").
		Joins("LEFT JOIN \"Albums\" a ON a.id = aa.album_id AND COALESCE(a.entry_status, '') <> 'closed' AND COALESCE(a.status, '') <> 'closed'").
		Where("COALESCE(\"Artists\".entry_status, '') <> ?", "closed").
		Group("\"Artists\".id").
		Find(&dbArtists).Error; err != nil {
		return nil, 0, err
	}

	artistIDs := make([]uuid.UUID, 0, len(dbArtists))
	for _, art := range dbArtists {
		artistIDs = append(artistIDs, art.ID)
	}

	artistBookmarkCounts := map[uuid.UUID]int64{}
	if len(artistIDs) > 0 {
		var bookmarkRows []struct {
			ArtistID uuid.UUID
			Count    int64
		}
		if err := s.db.Model(&model.ArtistBookmark{}).
			Select("artist_id, COUNT(*) AS count").
			Where("artist_id IN ?", artistIDs).
			Group("artist_id").
			Scan(&bookmarkRows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range bookmarkRows {
			artistBookmarkCounts[row.ArtistID] = row.Count
		}
	}

	artistPlayCounts := map[uuid.UUID]int64{}
	if len(artistIDs) > 0 {
		var playRows []struct {
			ArtistID  uuid.UUID
			PlayCount int64
		}
		if err := s.db.Table("song_artists").
			Select("song_artists.artist_id AS artist_id, COALESCE(SUM(\"Songs\".play_count), 0) AS play_count").
			Joins("JOIN \"Songs\" ON \"Songs\".id = song_artists.song_id").
			Where("song_artists.artist_id IN ?", artistIDs).
			Group("song_artists.artist_id").
			Scan(&playRows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range playRows {
			artistPlayCounts[row.ArtistID] = row.PlayCount
		}
	}

	candidates := make([]recommendation.Candidate, 0, len(dbArtists))
	artistByID := make(map[string]artistWithHotScore, len(dbArtists))
	for _, art := range dbArtists {
		qualityScore := normalizeMusicDiscoverQuality(art.MaxHotScore)
		candidates = append(candidates, recommendation.Candidate{
			Module:          "music",
			EntityType:      recommendation.EntityArtist,
			EntityID:        art.ID.String(),
			SourceKey:       art.ID.String(),
			QualityScore:    qualityScore,
			TrendScore:      clampMusicRecommendation(art.MaxHotScore / 10),
			FreshnessScore:  normalizeMusicAlbumFreshness(art.CreatedAt, 30*24*time.Hour),
			AuthorityScore:  math.Min(1.0, 0.5+0.1*float64(art.AlbumCount)),
			ExposureScore:   0,
			EditorialScore:  0,
			PublishedAtUnix: art.CreatedAt.Unix(),
		})
		artistByID[art.ID.String()] = art
	}

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]feed.RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		art, ok := artistByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, feed.RecommendationItemDTO{
			ID:            art.ID.String(),
			Title:         art.Name,
			Summary:       art.Bio,
			ImageURL:      art.ImageURL,
			TargetPath:    "/music/artist/" + art.ID.String(),
			ScoreLabel:    fmt.Sprintf("%s %.0f", musicRecommendationLabel(mode), math.Round(item.FinalScore*100)),
			PlayCount:     artistPlayCounts[art.ID],
			BookmarkCount: artistBookmarkCounts[art.ID],
		})
	}

	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

func (s *Service) RecommendArtistsByMode(mode recommendation.Mode, page int, pageSize int) ([]feed.RecommendationItemDTO, int64, error) {
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)

	var artists []model.Artist
	if err := s.db.Model(&model.Artist{}).
		Where("COALESCE(entry_status, '') <> ?", "closed").
		Order("created_at DESC").
		Find(&artists).Error; err != nil {
		return nil, 0, err
	}

	candidates := make([]recommendation.Candidate, 0, len(artists))
	artistByID := make(map[string]model.Artist, len(artists))
	for _, artist := range artists {
		candidates = append(candidates, recommendation.Candidate{
			Module:          "music",
			EntityType:      recommendation.EntityAlbum,
			EntityID:        artist.ID.String(),
			SourceKey:       artist.ID.String(),
			QualityScore:    0.5,
			TrendScore:      0.5,
			FreshnessScore:  normalizeMusicAlbumFreshness(artist.CreatedAt, 30*24*time.Hour),
			AuthorityScore:  0.5,
			ExposureScore:   0,
			EditorialScore:  0,
			PublishedAtUnix: artist.CreatedAt.Unix(),
		})
		artistByID[artist.ID.String()] = artist
	}

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]feed.RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		artist, ok := artistByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, feed.RecommendationItemDTO{
			ID:         artist.ID.String(),
			Title:      artist.Name,
			Summary:    artist.Bio,
			ImageURL:   artist.ImageURL,
			TargetPath: "/music/artist/" + artist.ID.String(),
			ScoreLabel: fmt.Sprintf("%s %.0f", musicRecommendationLabel(mode), math.Round(item.FinalScore*100)),
		})
	}

	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

func normalizeMusicRecommendationPage(page int, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func normalizeMusicAlbumFreshness(createdAt time.Time, horizon time.Duration) float64 {
	if createdAt.IsZero() || horizon <= 0 {
		return 0
	}
	age := time.Since(createdAt)
	if age <= 0 {
		return 1
	}
	return clampMusicRecommendation(1 - float64(age)/float64(horizon))
}

func normalizeMusicDiscoverQuality(hotScore float64) float64 {
	return clampMusicRecommendation(0.3 + hotScore/10)
}

func clampMusicRecommendation(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func musicRecommendationLabel(mode recommendation.Mode) string {
	switch mode {
	case recommendation.ModeHot:
		return "热度"
	case recommendation.ModeFeatured:
		return "精选"
	case recommendation.ModeDiscover:
		return "探索"
	default:
		return "推荐"
	}
}

func (s *Service) RecordSongPlay(songID uuid.UUID) error {
	if songID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "song_id is required")
	}

	result := s.db.Model(&model.Song{}).Where("id = ?", songID)
	var count int64
	if err := result.Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.NotFound("music.song_not_found", "Song not found")
	}

	return s.repo.IncrementSongPlayCount(songID)
}

func (s *Service) MergeArtists(user authctx.CurrentUser, sourceArtistID uuid.UUID, targetArtistID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if sourceArtistID == uuid.Nil || targetArtistID == uuid.Nil || sourceArtistID == targetArtistID {
		return apperr.BadRequest("validation.invalid_request", "source_artist_id and target_artist_id must be different valid UUIDs")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var source model.Artist
		if err := tx.Preload("Aliases").First(&source, "id = ?", sourceArtistID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.artist_not_found", "Source artist not found")
			}
			return err
		}

		var target model.Artist
		if err := tx.Preload("Aliases").First(&target, "id = ?", targetArtistID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.artist_not_found", "Target artist not found")
			}
			return err
		}

		if source.EntryStatus == "closed" {
			return apperr.Unprocessable("music.artist_not_open", "Source artist is not available")
		}
		if target.EntryStatus == "closed" {
			return apperr.Unprocessable("music.artist_not_open", "Target artist is not available")
		}

		if err := tx.Exec(`
			INSERT INTO album_artists (album_id, artist_id)
			SELECT aa.album_id, ?
			FROM album_artists aa
			WHERE aa.artist_id = ?
			  AND NOT EXISTS (
				SELECT 1 FROM album_artists existing
				WHERE existing.album_id = aa.album_id AND existing.artist_id = ?
			  )
		`, targetArtistID, sourceArtistID, targetArtistID).Error; err != nil {
			return err
		}
		if err := tx.Where("artist_id = ?", sourceArtistID).Delete(&model.AlbumArtist{}).Error; err != nil {
			return err
		}

		if err := tx.Exec(`
			INSERT INTO song_artists (song_id, artist_id)
			SELECT sa.song_id, ?
			FROM song_artists sa
			WHERE sa.artist_id = ?
			  AND NOT EXISTS (
				SELECT 1 FROM song_artists existing
				WHERE existing.song_id = sa.song_id AND existing.artist_id = ?
			  )
		`, targetArtistID, sourceArtistID, targetArtistID).Error; err != nil {
			return err
		}
		if err := tx.Where("artist_id = ?", sourceArtistID).Delete(&model.SongArtist{}).Error; err != nil {
			return err
		}

		var sourceAliases []model.ArtistAlias
		if err := tx.Where("artist_id = ?", sourceArtistID).Find(&sourceAliases).Error; err != nil {
			return err
		}

		for _, alias := range append([]string{source.Name}, func() []string {
			names := make([]string, 0, len(sourceAliases))
			for _, item := range sourceAliases {
				names = append(names, item.Alias)
			}
			return names
		}()...) {
			alias = strings.TrimSpace(alias)
			if alias == "" || strings.EqualFold(alias, target.Name) {
				continue
			}
			if err := tx.Where("artist_id = ? AND LOWER(alias) = LOWER(?)", targetArtistID, alias).
				FirstOrCreate(&model.ArtistAlias{ArtistID: targetArtistID, Alias: alias, IsMainName: false}).Error; err != nil {
				return err
			}
		}

		if err := tx.Model(&model.Artist{}).Where("id = ?", sourceArtistID).Update("entry_status", "closed").Error; err != nil {
			return err
		}

		mergeRecord := model.ArtistMerge{
			SourceArtistID: sourceArtistID,
			TargetArtistID: targetArtistID,
			MergedBy:       user.ID,
			MergedAt:       time.Now(),
		}
		return tx.Create(&mergeRecord).Error
	})
}

func (s *Service) SubmitEdit(user authctx.CurrentUser, req SubmitEditRequest) (model.MusicEdit, error) {
	if user.ID == uuid.Nil {
		return model.MusicEdit{}, apperr.Unauthorized("Login required")
	}
	if req.Type == "" || req.EntityType == "" || req.Reason == "" {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "type, entity_type and reason are required")
	}

	payloadJSON, err := marshalObject(req.Payload, map[string]any{})
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "payload must be an object")
	}
	changesJSON, err := marshalObject(req.Changes, map[string]any{})
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "changes must be an object")
	}
	sourcesJSON, err := json.Marshal(req.Sources)
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "sources are invalid")
	}

	edit := model.MusicEdit{
		Type:        req.Type,
		EntityType:  req.EntityType,
		EntityID:    req.EntityID,
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      req.Reason,
		PayloadJSON: string(payloadJSON),
		ChangesJSON: string(changesJSON),
		SourcesJSON: string(sourcesJSON),
		Votable:     true,
	}
	autoApplyTypes := map[string]struct{}{
		"create_artist": {},
		"create_album":  {},
		"update_artist": {},
		"update_album":  {},
	}

	if _, shouldAutoApply := autoApplyTypes[req.Type]; !shouldAutoApply {
		if err := s.repo.CreateEdit(&edit); err != nil {
			return model.MusicEdit{}, err
		}
		return edit, nil
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		if err := repo.CreateEdit(&edit); err != nil {
			return err
		}
		if err := applyEdit(tx, &edit); err != nil {
			edit.Status = "failed_prerequisite"
			edit.FailureReason = err.Error()
			return repo.SaveEdit(&edit)
		}
		edit.Status = "applied"
		edit.AutoApplied = true
		edit.Votable = false
		return repo.SaveEdit(&edit)
	})
	if err != nil {
		return model.MusicEdit{}, err
	}
	return edit, nil
}

func (s *Service) Vote(user authctx.CurrentUser, editID uuid.UUID, req VoteRequest) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if req.Vote != "yes" && req.Vote != "no" {
		return apperr.BadRequest("validation.invalid_request", "vote must be yes or no")
	}

	edit, err := s.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.edit_not_found", "Edit not found")
		}
		return err
	}
	if edit.Status != "open" {
		return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
	}

	vote := model.MusicEditVote{EditID: editID, UserID: user.ID, Vote: req.Vote, Comment: req.Comment}
	return s.db.Where("edit_id = ? AND user_id = ?", editID, user.ID).Assign(map[string]any{"vote": req.Vote, "comment": req.Comment}).FirstOrCreate(&vote).Error
}

func (s *Service) ApproveEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Moderator role required")
	}

	var out model.MusicEdit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		claimed, err := repo.ClaimOpenEdit(editID, "applying")
		if err != nil {
			return err
		}
		if !claimed {
			if _, err := repo.GetEdit(editID); errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			} else if err != nil {
				return err
			}
			return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
		}

		edit, err := repo.GetEdit(editID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			}
			return err
		}
		if err := applyEdit(tx, &edit); err != nil {
			return err
		}

		edit.Status = "applied"
		if err := repo.SaveEdit(&edit); err != nil {
			return err
		}
		decision := model.MusicEditDecision{EditID: edit.ID, DeciderID: user.ID, Decision: "approve", Reason: reason}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "music.edit.approve", EntityType: "music_edit", EntityID: &edit.ID, Reason: reason}); err != nil {
			return err
		}
		out = edit
		return nil
	})
	if err != nil {
		failed := model.MusicEdit{}
		if getErr := s.db.First(&failed, "id = ?", editID).Error; getErr == nil && failed.Status == "open" {
			failed.Status = "failed_prerequisite"
			failed.FailureReason = err.Error()
			if saveErr := s.repo.SaveEdit(&failed); saveErr == nil {
				out = failed
			}
		}
		return out, err
	}
	return out, nil
}

func (s *Service) RejectEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Moderator role required")
	}

	var out model.MusicEdit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		claimed, err := repo.ClaimOpenEdit(editID, "rejected")
		if err != nil {
			return err
		}
		if !claimed {
			if _, err := repo.GetEdit(editID); errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			} else if err != nil {
				return err
			}
			return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
		}

		edit, err := repo.GetEdit(editID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			}
			return err
		}

		edit.Status = "rejected"
		if err := repo.SaveEdit(&edit); err != nil {
			return err
		}
		if err := tx.Create(&model.MusicEditDecision{EditID: edit.ID, DeciderID: user.ID, Decision: "reject", Reason: reason}).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "music.edit.reject", EntityType: "music_edit", EntityID: &edit.ID, Reason: reason}); err != nil {
			return err
		}
		out = edit
		return nil
	})
	return out, err
}

func (s *Service) CancelEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	_ = reason
	if user.ID == uuid.Nil {
		return model.MusicEdit{}, apperr.Unauthorized("Login required")
	}

	edit, err := s.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.MusicEdit{}, apperr.NotFound("music.edit_not_found", "Edit not found")
		}
		return model.MusicEdit{}, err
	}
	if edit.Status != "open" {
		return model.MusicEdit{}, apperr.Unprocessable("music.edit_not_open", "Edit is not open")
	}
	if edit.SubmittedBy != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Only submitter or moderator can cancel")
	}

	edit.Status = "cancelled"
	if err := s.repo.SaveEdit(&edit); err != nil {
		return model.MusicEdit{}, err
	}
	return edit, nil
}

func marshalObject(value map[string]any, fallback map[string]any) ([]byte, error) {
	if value == nil {
		value = fallback
	}
	return json.Marshal(value)
}

func (s *Service) ListArtistBookmarks(user authctx.CurrentUser, page int, pageSize int) ([]model.ArtistBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListArtistBookmarks(user.ID, page, pageSize)
}

func (s *Service) BookmarkArtist(user authctx.CurrentUser, artistID uuid.UUID) (model.ArtistBookmark, error) {
	if user.ID == uuid.Nil {
		return model.ArtistBookmark{}, apperr.Unauthorized("Login required")
	}
	if artistID == uuid.Nil {
		return model.ArtistBookmark{}, apperr.BadRequest("validation.invalid_request", "artist_id is required")
	}
	return s.repo.UpsertArtistBookmark(user.ID, artistID)
}

func (s *Service) DeleteArtistBookmark(user authctx.CurrentUser, artistID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteArtistBookmark(user.ID, artistID)
}

func (s *Service) ListAlbumBookmarks(user authctx.CurrentUser, page int, pageSize int) ([]model.AlbumBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListAlbumBookmarks(user.ID, page, pageSize)
}

func (s *Service) BookmarkAlbum(user authctx.CurrentUser, albumID uuid.UUID) (model.AlbumBookmark, error) {
	if user.ID == uuid.Nil {
		return model.AlbumBookmark{}, apperr.Unauthorized("Login required")
	}
	if albumID == uuid.Nil {
		return model.AlbumBookmark{}, apperr.BadRequest("validation.invalid_request", "album_id is required")
	}
	return s.repo.UpsertAlbumBookmark(user.ID, albumID)
}

func (s *Service) DeleteAlbumBookmark(user authctx.CurrentUser, albumID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteAlbumBookmark(user.ID, albumID)
}

func (s *Service) ListSongBookmarks(user authctx.CurrentUser, page int, pageSize int) ([]model.SongBookmark, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListSongBookmarks(user.ID, page, pageSize)
}

func (s *Service) BookmarkSong(user authctx.CurrentUser, songID uuid.UUID) (model.SongBookmark, error) {
	if user.ID == uuid.Nil {
		return model.SongBookmark{}, apperr.Unauthorized("Login required")
	}
	if songID == uuid.Nil {
		return model.SongBookmark{}, apperr.BadRequest("validation.invalid_request", "song_id is required")
	}
	return s.repo.UpsertSongBookmark(user.ID, songID)
}

func (s *Service) DeleteSongBookmark(user authctx.CurrentUser, songID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteSongBookmark(user.ID, songID)
}

func (s *Service) CreatePlaylist(user authctx.CurrentUser, req CreatePlaylistRequest) (model.Playlist, error) {
	if user.ID == uuid.Nil {
		return model.Playlist{}, apperr.Unauthorized("Login required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.Playlist{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}

	playlist := model.Playlist{
		UserID:      user.ID,
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		CoverURL:    strings.TrimSpace(req.CoverURL),
		IsPublic:    req.IsPublic,
	}
	return s.repo.CreatePlaylist(playlist)
}

func (s *Service) ListPlaylists(user authctx.CurrentUser, page int, pageSize int) ([]model.Playlist, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListPlaylists(user.ID, page, pageSize)
}

func (s *Service) DeletePlaylist(user authctx.CurrentUser, playlistID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeletePlaylist(user.ID, playlistID)
}

func (s *Service) GetPlaylist(user authctx.CurrentUser, playlistID uuid.UUID) (model.Playlist, error) {
	playlist, err := s.repo.GetPlaylistByID(playlistID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Playlist{}, apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return model.Playlist{}, err
	}
	if playlist.UserID != user.ID && !playlist.IsPublic {
		return model.Playlist{}, apperr.NotFound("music.playlist_not_found", "Playlist not found")
	}
	return playlist, nil
}

func (s *Service) AddPlaylistSong(user authctx.CurrentUser, playlistID uuid.UUID, songID uuid.UUID) (model.PlaylistSong, error) {
	if user.ID == uuid.Nil {
		return model.PlaylistSong{}, apperr.Unauthorized("Login required")
	}
	if songID == uuid.Nil {
		return model.PlaylistSong{}, apperr.BadRequest("validation.invalid_request", "song_id is required")
	}
	if _, err := s.repo.GetPlaylistForUser(user.ID, playlistID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.PlaylistSong{}, apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return model.PlaylistSong{}, err
	}
	return s.repo.UpsertPlaylistSong(playlistID, songID)
}

func (s *Service) ListPlaylistSongs(user authctx.CurrentUser, playlistID uuid.UUID, page int, pageSize int) ([]model.PlaylistSong, int64, error) {
	playlist, err := s.repo.GetPlaylistByID(playlistID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return nil, 0, err
	}
	if playlist.UserID != user.ID && !playlist.IsPublic {
		return nil, 0, apperr.NotFound("music.playlist_not_found", "Playlist not found")
	}
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	return s.repo.ListPlaylistSongs(playlistID, page, pageSize)
}

func (s *Service) DeletePlaylistSong(user authctx.CurrentUser, playlistID uuid.UUID, songID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if _, err := s.repo.GetPlaylistForUser(user.ID, playlistID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.playlist_not_found", "Playlist not found")
		}
		return err
	}
	return s.repo.DeletePlaylistSong(playlistID, songID)
}

func (s *Service) ListPublicPlaylists(page int, pageSize int) ([]model.Playlist, map[uuid.UUID]int64, int64, error) {
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)

	playlists, total, err := s.repo.ListPublicPlaylists(page, pageSize)
	if err != nil {
		return nil, nil, 0, err
	}

	playlistIDs := make([]uuid.UUID, 0, len(playlists))
	for _, playlist := range playlists {
		playlistIDs = append(playlistIDs, playlist.ID)
	}
	songCounts, err := s.repo.CountPlaylistSongs(playlistIDs)
	if err != nil {
		return nil, nil, 0, err
	}

	return playlists, songCounts, total, nil
}

func (s *Service) Discover(page int, pageSize int) ([]DiscoverItemResponse, int64, error) {
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)
	seedSize := page * pageSize

	albumItems, albumTotal, err := s.RecommendAlbumsByMode(recommendation.ModeDiscover, 1, seedSize)
	if err != nil {
		return nil, 0, err
	}
	artistItems, artistTotal, err := s.RecommendArtistsByMode(recommendation.ModeDiscover, 1, seedSize)
	if err != nil {
		return nil, 0, err
	}
	playlists, playlistTotal, err := s.repo.ListRecentPublicPlaylists(seedSize)
	if err != nil {
		return nil, 0, err
	}
	playlistIDs := make([]uuid.UUID, 0, len(playlists))
	for _, playlist := range playlists {
		playlistIDs = append(playlistIDs, playlist.ID)
	}
	playlistSongCounts, err := s.repo.CountPlaylistSongs(playlistIDs)
	if err != nil {
		return nil, 0, err
	}

	playlistItems := make([]DiscoverItemResponse, 0, len(playlists))
	for _, playlist := range playlists {
		playlistItems = append(playlistItems, DiscoverItemResponse{
			Type:        "playlist",
			ID:          playlist.ID.String(),
			Title:       playlist.Name,
			Summary:     playlist.Description,
			ImageURL:    playlist.CoverURL,
			TargetPath:  "/music/playlist/" + playlist.ID.String(),
			SongCount:   playlistSongCounts[playlist.ID],
			OwnerUserID: playlist.UserID.String(),
		})
	}

	albumDiscoverItems := make([]DiscoverItemResponse, 0, len(albumItems))
	for _, item := range albumItems {
		albumDiscoverItems = append(albumDiscoverItems, DiscoverItemResponse{
			Type:       "album",
			ID:         item.ID,
			Title:      item.Title,
			Summary:    item.Summary,
			ImageURL:   item.ImageURL,
			TargetPath: item.TargetPath,
		})
	}

	artistDiscoverItems := make([]DiscoverItemResponse, 0, len(artistItems))
	for _, item := range artistItems {
		artistDiscoverItems = append(artistDiscoverItems, DiscoverItemResponse{
			Type:       "artist",
			ID:         item.ID,
			Title:      item.Title,
			Summary:    item.Summary,
			ImageURL:   item.ImageURL,
			TargetPath: item.TargetPath,
		})
	}

	mixed := make([]DiscoverItemResponse, 0, len(albumDiscoverItems)+len(artistDiscoverItems)+len(playlistItems))
	for i := 0; i < seedSize; i++ {
		if i < len(albumDiscoverItems) {
			mixed = append(mixed, albumDiscoverItems[i])
		}
		if i < len(artistDiscoverItems) {
			mixed = append(mixed, artistDiscoverItems[i])
		}
		if i < len(playlistItems) {
			mixed = append(mixed, playlistItems[i])
		}
	}

	start := (page - 1) * pageSize
	if start > len(mixed) {
		start = len(mixed)
	}
	end := start + pageSize
	if end > len(mixed) {
		end = len(mixed)
	}

	return mixed[start:end], albumTotal + artistTotal + playlistTotal, nil
}
