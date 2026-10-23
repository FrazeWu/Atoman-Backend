package music

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"gorm.io/gorm"
	"strings"
)

func validateImportedAlbumEdition(tx *gorm.DB, artists []resolvedCommitAlbumImportArtist, album *model.Album, input AlbumImportAlbumPayload) error {
	// 同一作品的重复判断和创建共用事务锁，避免并发上传产生两个同版本。
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(2026102302)).Error; err != nil {
		return err
	}
	edition := strings.TrimSpace(input.EditionType)
	if edition == "" {
		edition = "original"
	}
	switch edition {
	case "original", "reissue", "remaster", "other":
	default:
		return apperr.BadRequest("music.invalid_edition", "Unknown edition type")
	}
	album.EditionType = edition
	if input.CanonicalAlbumID == nil {
		return nil
	}
	var canonical model.Album
	if err := tx.First(&canonical, "id=? AND lifecycle_status=?", input.CanonicalAlbumID, model.MusicLifecycleActive).Error; err != nil {
		return apperr.NotFound("music.album_not_found", "Original album not found")
	}
	// 只关联到版本根，避免链和循环；不自动把不同版本的歌曲合并。
	if canonical.CanonicalAlbumID != nil || canonical.ID == album.ID {
		return apperr.Conflict("music.edition_conflict", "请选择该作品的原始版本")
	}
	var shared int64
	artistIDs := []any{}
	for _, artist := range artists {
		if artist.Artist != nil && hasAlbumArtistRole(artist.Roles, "primary") {
			artistIDs = append(artistIDs, artist.Artist.ID)
		}
	}
	if err := tx.Model(&model.AlbumArtist{}).Where("album_id=? AND artist_id IN ?", canonical.ID, artistIDs).Count(&shared).Error; err != nil {
		return err
	}
	if shared == 0 {
		return apperr.Conflict("music.edition_conflict", "版本必须属于同一主艺术家")
	}
	if normalizedMusicText(canonical.Title) != normalizedMusicText(album.Title) {
		return apperr.Conflict("music.edition_conflict", "请选择同一作品的版本")
	}
	if album.MusicBrainzReleaseID != "" {
		var count int64
		if err := tx.Model(&model.Album{}).Where("musicbrainz_release_id=? AND (id=? OR canonical_album_id=?)", album.MusicBrainzReleaseID, canonical.ID, canonical.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return apperr.Conflict("music.album_duplicate", "该发行版本已存在")
		}
	} else {
		var versions []model.Album
		if err := tx.Where("id=? OR canonical_album_id=?", canonical.ID, canonical.ID).Find(&versions).Error; err != nil {
			return err
		}
		for _, version := range versions {
			if version.EditionType == edition && version.ReleaseDate.Equal(album.ReleaseDate) {
				return apperr.Conflict("music.edition_conflict", "缺少足够信息区分该版本，请确认发行日期或匹配具体发行版本")
			}
		}
	}
	album.CanonicalAlbumID = &canonical.ID
	return nil
}
