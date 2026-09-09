package music

import (
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/indexnow"
	revisionservice "atoman/internal/service"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var albumImportCreateAlbumHook func(*gorm.DB, *model.Album) error

func (s *Service) CommitAlbumImportSession(user authctx.CurrentUser, id uuid.UUID, input CommitAlbumImportSessionInput) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}

	var out model.AlbumImportSession
	oldObjectKeys := []string{}
	newObjectKeys := []string{}
	consumedAudioAssetIDs := []uuid.UUID{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status == AlbumImportStatusCommitted {
			out = session
			return nil
		}
		if session.Status != AlbumImportStatusReady && !isAlbumImportValidationRetry(session) {
			if !isAlbumImportActiveStatus(session.Status) {
				return apperr.Unprocessable("music.import_invalid_status", "Import session cannot be submitted")
			}
			if strings.TrimSpace(input.Album.Title) == "" {
				return apperr.BadRequest("validation.invalid_request", "album title is required")
			}
			if strings.TrimSpace(input.Artist.Name) == "" && len(input.Artists) == 0 && strings.TrimSpace(input.ArtistID) == "" {
				return apperr.BadRequest("validation.invalid_request", "at least one artist is required")
			}
			payload := map[string]any{}
			if strings.TrimSpace(session.PayloadJSON) != "" {
				_ = json.Unmarshal([]byte(session.PayloadJSON), &payload)
			}
			payload["commit_request"] = input
			encoded, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			session.PayloadJSON = string(encoded)
			if err := tx.Save(&session).Error; err != nil {
				return err
			}
			if err := queueSubmittedAlbumImportWhenUploadsComplete(tx, &session); err != nil {
				return err
			}
			out = session
			return nil
		}

		payload := AlbumImportPayload{
			Artist:  input.Artist,
			Artists: nil,
			Album:   input.Album,
		}
		if strings.TrimSpace(payload.Album.Title) == "" {
			return apperr.BadRequest("validation.invalid_request", "album title is required")
		}
		resolvedArtists, err := resolveCommitAlbumImportArtists(tx, user, input)
		if err != nil {
			return err
		}
		if len(resolvedArtists) == 0 {
			return apperr.BadRequest("validation.invalid_request", "at least one artist is required")
		}
		albumSources, albumSourcesJSON, err := normalizeMusicSources(input.AlbumSources, input.AlbumSource)
		if err != nil {
			return err
		}
		artists := make([]*model.Artist, 0, len(resolvedArtists))
		credits := make([]AlbumArtistCreditInput, 0, len(resolvedArtists))
		for index, resolved := range resolvedArtists {
			artists = append(artists, resolved.Artist)
			credits = append(credits, AlbumArtistCreditInput{
				ArtistID: resolved.Artist.ID.String(),
				Roles:    resolved.Roles,
				Position: index + 1,
			})
			asset, err := storage.PromoteMusicUploadAsset(
				s.s3, resolved.Artist.ImageURL,
				storage.BuildMusicArtistImageVersionKey(resolved.Artist.ID.String(), uuid.NewString(), path.Ext(resolved.Artist.ImageURL)),
			)
			if err != nil {
				return err
			}
			if asset.DestinationKey != "" {
				resolved.Artist.ImageURL = asset.URL
				oldObjectKeys = append(oldObjectKeys, asset.SourceKey)
				newObjectKeys = append(newObjectKeys, asset.DestinationKey)
				if err := tx.Model(resolved.Artist).Update("image_url", asset.URL).Error; err != nil {
					return err
				}
			}
		}

		var sessionPayload map[string]any
		if strings.TrimSpace(session.PayloadJSON) != "" {
			_ = json.Unmarshal([]byte(session.PayloadJSON), &sessionPayload)
		}
		delete(sessionPayload, "commit_validation_failed")
		if len(payload.Album.Tracks) == 0 {
			payload.Album.Tracks = albumImportTracksFromDerived(sessionPayload)
		}

		coverURL := strings.TrimSpace(input.Album.CoverURL)
		if coverURL == "" && sessionPayload != nil {
			coverURL = resolveAlbumImportCoverURL(sessionPayload)
		}
		if coverURL == "" || strings.TrimSpace(payload.Album.ReleaseDate) == "" || len(payload.Album.Tracks) == 0 {
			return apperr.BadRequest("validation.invalid_request", "album cover, release date and at least one track are required")
		}
		musicBrainzReleaseID := strings.TrimSpace(stringValue(sessionPayload["musicbrainz_release_id"]))
		metadataProvider := strings.ToLower(strings.TrimSpace(stringValue(sessionPayload["metadata_source"])))
		metadataExternalID := strings.TrimSpace(stringValue(sessionPayload["metadata_external_id"]))
		if metadataExternalID == "" {
			metadataExternalID = musicBrainzReleaseID
		}
		metadataMatchStatus := strings.TrimSpace(stringValue(sessionPayload["metadata_match_status"]))
		if metadataMatchStatus == "" {
			if metadataProvider != "" {
				metadataMatchStatus = model.MusicMatchMatched
			} else {
				metadataMatchStatus = model.MusicMatchUnmatched
			}
		}
		metadataMatchConfidence := floatValue(sessionPayload["metadata_match_confidence"])
		if metadataMatchStatus == model.MusicMatchMatched && metadataMatchConfidence == 0 {
			metadataMatchConfidence = 1
		}
		albumMetadataManualOverride := strings.TrimSpace(stringValue(sessionPayload["derived_album_title"])) != "" &&
			!strings.EqualFold(strings.TrimSpace(stringValue(sessionPayload["derived_album_title"])), strings.TrimSpace(payload.Album.Title))
		if albumMetadataManualOverride && metadataMatchStatus == model.MusicMatchMatched {
			metadataMatchStatus = model.MusicMatchManual
		}
		var musicBrainzMatchedAt *time.Time
		if musicBrainzReleaseID != "" {
			matchedAt := time.Now().UTC()
			musicBrainzMatchedAt = &matchedAt
		}
		albumType := strings.ToLower(strings.TrimSpace(payload.Album.AlbumType))
		isStandaloneSong := albumType == "single" || albumType == "leak"
		if isStandaloneSong && len(payload.Album.Tracks) != 1 {
			return apperr.BadRequest("validation.invalid_request", "single and leak releases must contain exactly one track")
		}
		if isStandaloneSong && session.TargetAlbumID != nil {
			return apperr.BadRequest("validation.invalid_request", "convert the album from its editor before repairing this import")
		}
		if !isStandaloneSong && session.TargetSongID != nil {
			return apperr.BadRequest("validation.invalid_request", "convert the song from its editor before repairing this import")
		}

		album := model.Album{
			Title:                  strings.TrimSpace(payload.Album.Title),
			Description:            strings.TrimSpace(payload.Album.Description),
			ReleaseYear:            payload.Album.ReleaseYear,
			Year:                   payload.Album.ReleaseYear,
			CoverURL:               coverURL,
			CoverSource:            coverSourceFromURL(coverURL),
			Status:                 "open",
			EntryStatus:            "open",
			LifecycleStatus:        model.MusicLifecycleActive,
			EditStatus:             model.MusicEditDevelopment,
			AlbumType:              strings.TrimSpace(payload.Album.AlbumType),
			UploadedBy:             &user.ID,
			SourcesJSON:            albumSourcesJSON,
			Sources:                albumSources,
			MusicBrainzMatched:     musicBrainzReleaseID != "",
			MusicBrainzReleaseID:   musicBrainzReleaseID,
			MusicBrainzMatchedAt:   musicBrainzMatchedAt,
			MetadataManualOverride: albumMetadataManualOverride,
		}
		if album.AlbumType == "" {
			album.AlbumType = "album"
		}
		if strings.TrimSpace(payload.Album.ReleaseDate) != "" {
			releaseDate, precision, err := parseOptionalReleaseDate(payload.Album.ReleaseDate)
			if err != nil {
				return err
			}
			album.ReleaseDatePrecision = precision
			if releaseDate != nil {
				album.ReleaseDate = *releaseDate
				if album.ReleaseYear == 0 {
					album.ReleaseYear = releaseDate.Year()
				}
				album.Year = album.ReleaseYear
			}
		}
		if isStandaloneSong {
			standalone, migratedOldKeys, migratedNewKeys, err := s.commitStandaloneSongImport(
				tx, user, &session, sessionPayload, input, payload, album,
				albumSources, albumSourcesJSON, resolvedArtists, credits, id,
			)
			oldObjectKeys = append(oldObjectKeys, migratedOldKeys...)
			newObjectKeys = append(newObjectKeys, migratedNewKeys...)
			if err != nil {
				return err
			}
			out = standalone
			return nil
		}

		isRepair := session.TargetAlbumID != nil
		revisions := revisionservice.NewRevisionService(tx)
		if isRepair {
			var existing model.Album
			if err := tx.First(&existing, "id = ?", *session.TargetAlbumID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return apperr.NotFound("music.album_not_found", "Target album not found")
				}
				return err
			}
			if _, err := revisions.EnsureInitialRevision("album", existing.ID, user.ID); err != nil {
				return err
			}
			if err := revisionservice.ValidateMusicEntryEdit(tx, "album", existing.ID, user.ID); err != nil {
				return err
			}
			existing.Title = album.Title
			existing.Description = album.Description
			existing.ReleaseYear = album.ReleaseYear
			existing.Year = album.Year
			existing.ReleaseDate = album.ReleaseDate
			existing.ReleaseDatePrecision = album.ReleaseDatePrecision
			existing.CoverURL = album.CoverURL
			existing.CoverSource = album.CoverSource
			existing.AlbumType = album.AlbumType
			existing.SourcesJSON = album.SourcesJSON
			existing.Sources = album.Sources
			existing.MusicBrainzMatched = album.MusicBrainzMatched
			existing.MusicBrainzReleaseID = album.MusicBrainzReleaseID
			existing.MusicBrainzMatchedAt = album.MusicBrainzMatchedAt
			existing.MetadataManualOverride = existing.MetadataManualOverride || album.MetadataManualOverride
			album = existing
			if err := tx.Save(&album).Error; err != nil {
				return err
			}
		} else {
			if err := createAlbumImportAlbum(tx, &album); err != nil {
				return err
			}
			session.TargetAlbumID = &album.ID
		}
		promotedCoverURL, oldCoverKey, newCoverKey, err := s.promoteAlbumImportAsset(
			album.CoverURL,
			storage.BuildMusicAlbumCoverVersionKey(album.ID.String(), uuid.NewString(), path.Ext(album.CoverURL)),
			id,
		)
		if err != nil {
			return err
		}
		if newCoverKey != "" {
			album.CoverURL = promotedCoverURL
			album.CoverSource = "s3"
			oldObjectKeys = append(oldObjectKeys, oldCoverKey)
			newObjectKeys = append(newObjectKeys, newCoverKey)
			if err := tx.Model(&album).Updates(map[string]any{
				"cover_url":    album.CoverURL,
				"cover_source": album.CoverSource,
			}).Error; err != nil {
				return err
			}
		}
		if err := upsertMusicMatchRecord(tx, "album", album.ID, metadataProvider, metadataExternalID, strings.TrimSpace(stringValue(sessionPayload["metadata_source_url"])), metadataMatchStatus, metadataMatchConfidence, album.MetadataManualOverride, map[string]any{
			"album_title": payload.Album.Title,
			"source":      metadataProvider,
		}); err != nil {
			return err
		}
		if err := replaceAlbumArtistCredits(tx, album.ID, credits, true, user.ID); err != nil {
			return err
		}
		for _, resolved := range resolvedArtists {
			if resolved.Artist.EntryStatus != artistEntryDraft || !hasAlbumArtistRole(resolved.Roles, "primary") {
				continue
			}
			if resolved.Artist.CreatedBy == nil || *resolved.Artist.CreatedBy != user.ID {
				return apperr.NotFound("music.artist_not_found", "Artist not found")
			}
			artistSources, artistSourcesJSON, err := normalizeMusicSources(input.ArtistSources, input.ArtistSource)
			if err != nil {
				return err
			}
			if err := tx.Model(resolved.Artist).Updates(map[string]any{
				"entry_status":     artistEntryOpen,
				"lifecycle_status": model.MusicLifecycleActive,
				"sources_json":     artistSourcesJSON,
			}).Error; err != nil {
				return err
			}
			resolved.Artist.EntryStatus = artistEntryOpen
			resolved.Artist.LifecycleStatus = model.MusicLifecycleActive
			resolved.Artist.Sources = artistSources
			resolved.Artist.SourcesJSON = artistSourcesJSON
		}

		usedDerivedTrackIndexes := map[int]bool{}
		rawDerivedTracks := []any(nil)
		if sessionPayload != nil {
			if derivedTracks, ok := sessionPayload["derived_tracks"].([]any); ok {
				rawDerivedTracks = derivedTracks
			}
		}
		var importFiles []model.AlbumImportFile
		if err := tx.Where("import_id = ?", session.ID).Find(&importFiles).Error; err != nil {
			return err
		}
		importFilesByID := make(map[string]model.AlbumImportFile, len(importFiles))
		for _, file := range importFiles {
			importFilesByID[file.ID.String()] = file
		}
		var existingSongs []model.Song
		existingSongsByID := map[uuid.UUID]*model.Song{}
		if isRepair {
			if err := tx.Unscoped().Where("album_id = ?", album.ID).Order("disc_number ASC, track_number ASC, created_at ASC").Find(&existingSongs).Error; err != nil {
				return err
			}
			for index := range existingSongs {
				existingSongsByID[existingSongs[index].ID] = &existingSongs[index]
			}
		}
		seenSongIDs := map[uuid.UUID]bool{}
		seenAudioAssetIDs := map[uuid.UUID]bool{}
		for _, track := range payload.Album.Tracks {
			derived := matchDerivedTrackAudio(rawDerivedTracks, track, usedDerivedTrackIndexes)
			audioURL := strings.TrimSpace(derived.AudioURL)
			var uploadedAudioAsset *model.MediaAsset
			audioAssetID := strings.TrimSpace(track.AudioAssetID)
			if audioAssetID != "" {
				assetID, err := uuid.Parse(audioAssetID)
				if err != nil {
					return apperr.BadRequest("validation.invalid_request", "invalid audio asset id")
				}
				if seenAudioAssetIDs[assetID] {
					return apperr.BadRequest("validation.invalid_request", "audio asset cannot be reused")
				}
				var asset model.MediaAsset
				if err := tx.First(&asset, "id = ? AND user_id = ? AND purpose = ?", assetID, user.ID, "music.audio").Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return apperr.NotFound("music.audio_asset_not_found", "Completed audio upload not found")
					}
					return err
				}
				if !isMusicAudioContentType(asset.ContentType) || strings.TrimSpace(asset.URL) == "" || strings.TrimSpace(asset.Key) == "" {
					return apperr.Unprocessable("music.audio_asset_invalid", "Uploaded asset is not valid audio")
				}
				uploadedAudioAsset = &asset
				audioURL = strings.TrimSpace(asset.URL)
				seenAudioAssetIDs[asset.ID] = true
			}
			metadata := songAudioMetadataFromImportFile(importFilesByID[derived.FileID])
			matchStatus, matchProvider, matchExternalID, matchSourceURL, matchConfidence, matchManualOverride := importTrackMatchState(track, derived)
			var existingSong *model.Song
			if strings.TrimSpace(track.SongID) != "" {
				songID, err := uuid.Parse(strings.TrimSpace(track.SongID))
				if err != nil {
					return apperr.BadRequest("validation.invalid_request", "invalid song id")
				}
				existingSong = existingSongsByID[songID]
				if existingSong == nil {
					return apperr.BadRequest("validation.invalid_request", "song does not belong to target album")
				}
				if seenSongIDs[songID] {
					return apperr.BadRequest("validation.invalid_request", "duplicate song id")
				}
				seenSongIDs[songID] = true
			} else if isRepair {
				existingSong = findRepairSong(existingSongs, seenSongIDs, track)
				if existingSong != nil {
					seenSongIDs[existingSong.ID] = true
				}
			}
			if existingSong != nil {
				song := *existingSong
				if strings.TrimSpace(song.AudioURL) == "" && audioURL == "" {
					return apperr.BadRequest("validation.invalid_request", "every track must have processed audio")
				}
				song.DeletedAt = gorm.DeletedAt{}
				song.Title = strings.TrimSpace(track.Title)
				song.TrackNumber = track.TrackNumber
				song.DiscNumber = normalizedDiscNumber(track.DiscNumber)
				song.Status = "open"
				song.LifecycleStatus = model.MusicLifecycleActive
				song.ReleaseDate = album.ReleaseDate
				song.ReleaseDatePrecision = album.ReleaseDatePrecision
				song.MetadataManualOverride = song.MetadataManualOverride || matchManualOverride
				song.AudioStatus = "ready"
				audioCheckedAt := time.Now().UTC()
				song.AudioCheckedAt = &audioCheckedAt
				if strings.TrimSpace(audioURL) != "" && audioURL != song.AudioURL {
					promotedAudioURL, oldAudioKey, newAudioKey, err := s.promoteImportedTrackAsset(
						audioURL,
						storage.BuildMusicAlbumTrackVersionKey(album.ID.String(), song.ID.String(), uuid.NewString(), path.Ext(audioURL)),
						id,
						uploadedAudioAsset != nil,
					)
					if err != nil {
						return err
					}
					if newAudioKey != "" || uploadedAudioAsset != nil {
						song.AudioURL = promotedAudioURL
						song.AudioSource = coverSourceFromURL(promotedAudioURL)
						if newAudioKey != "" {
							oldObjectKeys = append(oldObjectKeys, oldAudioKey)
							newObjectKeys = append(newObjectKeys, newAudioKey)
						}
					}
				}
				if uploadedAudioAsset != nil {
					consumedAudioAssetIDs = append(consumedAudioAssetIDs, uploadedAudioAsset.ID)
				}
				applySongAudioMetadata(&song, metadata)
				if err := tx.Unscoped().Save(&song).Error; err != nil {
					return err
				}
				if err := upsertMusicMatchRecord(tx, "song", song.ID, matchProvider, matchExternalID, matchSourceURL, matchStatus, matchConfidence, song.MetadataManualOverride, map[string]any{
					"title": track.Title, "track_number": track.TrackNumber, "disc_number": track.DiscNumber,
				}); err != nil {
					return err
				}
				if err := tx.Model(&song).Association("Artists").Replace(artists); err != nil {
					return err
				}
				if err := persistAlbumImportTrackLyrics(tx, user.ID, song.ID, track.Lyrics, track.LyricsSource); err != nil {
					return err
				}
				continue
			}
			if strings.TrimSpace(audioURL) == "" {
				return apperr.BadRequest("validation.invalid_request", "every track must have processed audio")
			}

			song := model.Song{
				Title:                  strings.TrimSpace(track.Title),
				TrackNumber:            track.TrackNumber,
				DiscNumber:             normalizedDiscNumber(track.DiscNumber),
				ReleaseDate:            album.ReleaseDate,
				ReleaseDatePrecision:   album.ReleaseDatePrecision,
				AlbumID:                &album.ID,
				Status:                 "open",
				LifecycleStatus:        model.MusicLifecycleActive,
				EditStatus:             model.MusicEditDevelopment,
				AudioURL:               audioURL,
				AudioSource:            coverSourceFromURL(audioURL),
				AudioStatus:            "ready",
				MetadataManualOverride: matchManualOverride,
				UploadedBy:             &user.ID,
			}
			audioCheckedAt := time.Now().UTC()
			song.AudioCheckedAt = &audioCheckedAt
			applySongAudioMetadata(&song, metadata)
			if err := tx.Create(&song).Error; err != nil {
				return err
			}
			if err := upsertMusicMatchRecord(tx, "song", song.ID, matchProvider, matchExternalID, matchSourceURL, matchStatus, matchConfidence, song.MetadataManualOverride, map[string]any{
				"title": track.Title, "track_number": track.TrackNumber, "disc_number": track.DiscNumber,
			}); err != nil {
				return err
			}
			promotedAudioURL, oldAudioKey, newAudioKey, err := s.promoteImportedTrackAsset(
				song.AudioURL,
				storage.BuildMusicAlbumTrackVersionKey(album.ID.String(), song.ID.String(), uuid.NewString(), path.Ext(song.AudioURL)),
				id,
				uploadedAudioAsset != nil,
			)
			if err != nil {
				return err
			}
			if newAudioKey != "" {
				song.AudioURL = promotedAudioURL
				song.AudioSource = "s3"
				oldObjectKeys = append(oldObjectKeys, oldAudioKey)
				newObjectKeys = append(newObjectKeys, newAudioKey)
				if err := tx.Model(&song).Updates(map[string]any{
					"audio_url":    song.AudioURL,
					"audio_source": song.AudioSource,
				}).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&song).Association("Artists").Append(artists); err != nil {
				return err
			}
			if err := persistAlbumImportTrackLyrics(tx, user.ID, song.ID, track.Lyrics, track.LyricsSource); err != nil {
				return err
			}
			if uploadedAudioAsset != nil {
				consumedAudioAssetIDs = append(consumedAudioAssetIDs, uploadedAudioAsset.ID)
			}
		}
		for _, assetID := range consumedAudioAssetIDs {
			if err := tx.Where("id = ? AND user_id = ? AND purpose = ?", assetID, user.ID, "music.audio").Delete(&model.MediaAsset{}).Error; err != nil {
				return err
			}
		}
		if isRepair {
			for _, song := range existingSongs {
				if seenSongIDs[song.ID] {
					continue
				}
				if err := tx.Model(&song).Updates(map[string]any{"status": "closed", "lifecycle_status": model.MusicLifecycleRetired}).Error; err != nil {
					return err
				}
			}
		}

		for _, resolved := range resolvedArtists {
			if _, err := revisions.EnsureInitialRevision("artist", resolved.Artist.ID, user.ID); err != nil {
				return err
			}
		}
		if _, err := revisions.EnsureInitialRevision("album", album.ID, user.ID); err != nil {
			return err
		}
		var albumSongs []model.Song
		if err := tx.Where("album_id = ?", album.ID).Find(&albumSongs).Error; err != nil {
			return err
		}
		for _, song := range albumSongs {
			if _, err := revisions.EnsureInitialRevision("song", song.ID, user.ID); err != nil {
				return err
			}
		}
		if isRepair {
			if _, err := revisions.CreateCurrentSnapshotRevision("album", album.ID, user.ID, "修复专辑资料"); err != nil {
				return err
			}
		}

		now := time.Now()
		if sessionPayload == nil {
			sessionPayload = map[string]any{}
		}
		sessionPayload["commit_request"] = input
		sessionPayload["artist_source"] = strings.TrimSpace(input.ArtistSource)
		sessionPayload["album_source"] = strings.TrimSpace(input.AlbumSource)
		applyAlbumImportSessionState(&session, AlbumImportStatusCommitted, sessionPayload)
		payloadJSON, err := json.Marshal(sessionPayload)
		if err != nil {
			return err
		}
		session.PayloadJSON = string(payloadJSON)
		session.CommittedAt = &now
		session.CommittedBy = &user.ID
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		return nil
	})
	if err != nil {
		s.deleteAlbumImportObjects(newObjectKeys)
		var appErr *apperr.AppError
		if errors.As(err, &appErr) && appErr.HTTPStatus >= 400 && appErr.HTTPStatus < 500 {
			failed, markErr := s.markAlbumImportNeedsAttention(user.ID, id, appErr.Message)
			if markErr == nil {
				s.updateAlbumImportNotification(failed)
				return failed, nil
			}
		}
		return model.AlbumImportSession{}, err
	}
	s.deleteAlbumImportObjects(oldObjectKeys)
	s.updateAlbumImportNotification(out)
	if out.Status == AlbumImportStatusCommitted {
		if out.TargetSongID != nil {
			s.notifyIndexNowSong(out.TargetSongID)
		} else {
			s.notifyIndexNowAlbum(out.TargetAlbumID)
		}
	}
	return out, nil
}

func (s *Service) commitStandaloneSongImport(
	tx *gorm.DB,
	user authctx.CurrentUser,
	session *model.AlbumImportSession,
	sessionPayload map[string]any,
	input CommitAlbumImportSessionInput,
	payload AlbumImportPayload,
	release model.Album,
	sources []Source,
	sourcesJSON string,
	resolvedArtists []resolvedCommitAlbumImportArtist,
	credits []AlbumArtistCreditInput,
	importID uuid.UUID,
) (model.AlbumImportSession, []string, []string, error) {
	if len(payload.Album.Tracks) != 1 {
		return model.AlbumImportSession{}, nil, nil, apperr.BadRequest("validation.invalid_request", "single and leak releases must contain exactly one track")
	}
	if len(sources) == 0 {
		return model.AlbumImportSession{}, nil, nil, apperr.BadRequest("validation.invalid_request", "standalone songs require at least one source")
	}
	track := payload.Album.Tracks[0]
	isRepair := session.TargetSongID != nil
	var song model.Song
	if isRepair {
		if err := tx.First(&song, "id = ?", *session.TargetSongID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.AlbumImportSession{}, nil, nil, apperr.NotFound("music.song_not_found", "Target song not found")
			}
			return model.AlbumImportSession{}, nil, nil, err
		}
		if song.AlbumID != nil || song.ReleaseType == nil {
			return model.AlbumImportSession{}, nil, nil, apperr.BadRequest("validation.invalid_request", "target song is not standalone")
		}
		if err := revisionservice.ValidateMusicEntryEdit(tx, "song", song.ID, user.ID); err != nil {
			return model.AlbumImportSession{}, nil, nil, err
		}
	} else {
		song = model.Song{Base: model.Base{ID: uuid.New()}, UploadedBy: &user.ID}
	}

	if sessionPayload == nil {
		sessionPayload = map[string]any{}
	}
	rawDerivedTracks, _ := sessionPayload["derived_tracks"].([]any)
	derived := matchDerivedTrackAudio(rawDerivedTracks, track, map[int]bool{})
	matchStatus, matchProvider, matchExternalID, matchSourceURL, matchConfidence, matchManualOverride := importTrackMatchState(track, derived)
	var importFile model.AlbumImportFile
	if derived.FileID != "" {
		fileID, err := uuid.Parse(derived.FileID)
		if err != nil {
			return model.AlbumImportSession{}, nil, nil, apperr.BadRequest("validation.invalid_request", "derived audio file id is invalid")
		}
		if err := tx.First(&importFile, "id = ? AND import_id = ?", fileID, session.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.AlbumImportSession{}, nil, nil, apperr.BadRequest("validation.invalid_request", "derived audio file was not found")
			}
			return model.AlbumImportSession{}, nil, nil, err
		}
	}
	audioURL := strings.TrimSpace(derived.AudioURL)
	if audioURL == "" {
		audioURL = strings.TrimSpace(song.AudioURL)
	}
	if audioURL == "" {
		return model.AlbumImportSession{}, nil, nil, apperr.BadRequest("validation.invalid_request", "standalone songs require processed audio")
	}

	oldObjectKeys := []string{}
	newObjectKeys := []string{}
	promotedCoverURL, oldCoverKey, newCoverKey, err := s.promoteAlbumImportAsset(
		release.CoverURL,
		storage.BuildMusicSongCoverVersionKey(song.ID.String(), uuid.NewString(), path.Ext(release.CoverURL)),
		importID,
	)
	if err != nil {
		return model.AlbumImportSession{}, nil, nil, err
	}
	if oldCoverKey != "" {
		oldObjectKeys = append(oldObjectKeys, oldCoverKey)
	}
	if newCoverKey != "" {
		newObjectKeys = append(newObjectKeys, newCoverKey)
	}
	promotedAudioURL, oldAudioKey, newAudioKey, err := s.promoteAlbumImportAsset(
		audioURL,
		storage.BuildMusicSongAudioVersionKey(song.ID.String(), uuid.NewString(), path.Ext(audioURL)),
		importID,
	)
	if err != nil {
		return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
	}
	if oldAudioKey != "" {
		oldObjectKeys = append(oldObjectKeys, oldAudioKey)
	}
	if newAudioKey != "" {
		newObjectKeys = append(newObjectKeys, newAudioKey)
	}

	releaseType := strings.ToLower(strings.TrimSpace(release.AlbumType))
	song.Title = strings.TrimSpace(release.Title)
	song.Description = strings.TrimSpace(release.Description)
	song.ReleaseType = &releaseType
	song.ReleaseDate = release.ReleaseDate
	song.ReleaseDatePrecision = release.ReleaseDatePrecision
	song.TrackNumber = 1
	song.DiscNumber = 1
	song.CoverURL = promotedCoverURL
	song.CoverSource = coverSourceFromURL(promotedCoverURL)
	song.SourcesJSON = sourcesJSON
	song.Sources = sources
	song.AlbumID = nil
	song.AudioURL = promotedAudioURL
	song.AudioSource = coverSourceFromURL(promotedAudioURL)
	song.AudioStatus = "ready"
	audioCheckedAt := time.Now().UTC()
	song.AudioCheckedAt = &audioCheckedAt
	song.MetadataManualOverride = song.MetadataManualOverride || matchManualOverride
	song.Status = "open"
	song.LifecycleStatus = model.MusicLifecycleActive
	song.EditStatus = model.MusicEditDevelopment
	applySongAudioMetadata(&song, songAudioMetadataFromImportFile(importFile))
	if isRepair {
		if err := tx.Save(&song).Error; err != nil {
			return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
		}
	} else if err := tx.Create(&song).Error; err != nil {
		return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
	}
	if err := upsertMusicMatchRecord(tx, "song", song.ID, matchProvider, matchExternalID, matchSourceURL, matchStatus, matchConfidence, song.MetadataManualOverride, map[string]any{
		"title": track.Title, "track_number": track.TrackNumber, "disc_number": track.DiscNumber,
	}); err != nil {
		return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
	}
	if err := replaceStandaloneSongArtistCredits(tx, song.ID, credits, user.ID); err != nil {
		return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
	}
	if err := persistAlbumImportTrackLyrics(tx, user.ID, song.ID, track.Lyrics, track.LyricsSource); err != nil {
		return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
	}
	for _, resolved := range resolvedArtists {
		if resolved.Artist.EntryStatus == artistEntryDraft && hasAlbumArtistRole(resolved.Roles, "primary") {
			if resolved.Artist.CreatedBy == nil || *resolved.Artist.CreatedBy != user.ID {
				return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, apperr.NotFound("music.artist_not_found", "Artist not found")
			}
			artistSources, artistSourcesJSON, err := normalizeMusicSources(input.ArtistSources, input.ArtistSource)
			if err != nil {
				return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
			}
			if err := tx.Model(resolved.Artist).Updates(map[string]any{
				"entry_status": artistEntryOpen, "lifecycle_status": model.MusicLifecycleActive, "sources_json": artistSourcesJSON,
			}).Error; err != nil {
				return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
			}
			resolved.Artist.Sources = artistSources
		}
	}

	revisions := revisionservice.NewRevisionService(tx)
	for _, resolved := range resolvedArtists {
		if _, err := revisions.EnsureInitialRevision("artist", resolved.Artist.ID, user.ID); err != nil {
			return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
		}
	}
	if _, err := revisions.EnsureInitialRevision("song", song.ID, user.ID); err != nil {
		return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
	}
	if isRepair {
		if _, err := revisions.CreateCurrentSnapshotRevision("song", song.ID, user.ID, "修复歌曲资料"); err != nil {
			return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
		}
	}

	now := time.Now()
	session.TargetAlbumID = nil
	session.TargetSongID = &song.ID
	sessionPayload["artist_source"] = strings.TrimSpace(input.ArtistSource)
	sessionPayload["album_source"] = strings.TrimSpace(input.AlbumSource)
	applyAlbumImportSessionState(session, AlbumImportStatusCommitted, sessionPayload)
	payloadJSON, err := json.Marshal(sessionPayload)
	if err != nil {
		return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
	}
	session.PayloadJSON = string(payloadJSON)
	session.CommittedAt = &now
	session.CommittedBy = &user.ID
	if err := tx.Save(session).Error; err != nil {
		return model.AlbumImportSession{}, oldObjectKeys, newObjectKeys, err
	}
	return *session, oldObjectKeys, newObjectKeys, nil
}

func replaceStandaloneSongArtistCredits(tx *gorm.DB, songID uuid.UUID, credits []AlbumArtistCreditInput, actorID uuid.UUID) error {
	albumRows, err := normalizeAlbumArtistCredits(tx, uuid.Nil, credits, true, actorID)
	if err != nil {
		return err
	}
	rows := make([]model.SongArtist, 0, len(albumRows))
	for _, row := range albumRows {
		rows = append(rows, model.SongArtist{
			SongID: songID, ArtistID: row.ArtistID, Role: row.Role,
			CustomRole: row.CustomRole, Position: row.Position,
		})
	}
	if err := tx.Where("song_id = ?", songID).Delete(&model.SongArtist{}).Error; err != nil {
		return err
	}
	return tx.Create(&rows).Error
}

func (s *Service) notifyIndexNowSong(songID *uuid.UUID) {
	if songID == nil || *songID == uuid.Nil {
		return
	}
	paths := []string{"/music/song/" + songID.String()}
	var credits []model.SongArtist
	if err := s.db.Where("song_id = ?", *songID).Find(&credits).Error; err == nil {
		for _, credit := range credits {
			paths = append(paths, "/music/artist/"+credit.ArtistID.String())
		}
	}
	indexnow.NotifyPaths(paths...)
}

func (s *Service) notifyIndexNowAlbum(albumID *uuid.UUID) {
	if albumID == nil || *albumID == uuid.Nil {
		return
	}
	paths := []string{"/music/album/" + albumID.String()}
	var credits []model.AlbumArtist
	if err := s.db.Where("album_id = ?", *albumID).Find(&credits).Error; err == nil {
		for _, credit := range credits {
			paths = append(paths, "/music/artist/"+credit.ArtistID.String())
		}
	}
	var songs []model.Song
	if err := s.db.Where("album_id = ? AND status = ?", *albumID, "open").Find(&songs).Error; err == nil {
		for _, song := range songs {
			paths = append(paths, "/music/song/"+song.ID.String())
		}
	}
	indexnow.NotifyPaths(paths...)
}

func isAlbumImportValidationRetry(session model.AlbumImportSession) bool {
	if session.Status != AlbumImportStatusNeedsAttention {
		return false
	}
	payload := map[string]any{}
	return json.Unmarshal([]byte(session.PayloadJSON), &payload) == nil && payload["commit_validation_failed"] == true
}

func (s *Service) markAlbumImportNeedsAttention(userID, importID uuid.UUID, message string) (model.AlbumImportSession, error) {
	var out model.AlbumImportSession
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND status IN ?", importID, userID, []string{AlbumImportStatusReady, AlbumImportStatusNeedsAttention}).
			First(&session).Error; err != nil {
			return err
		}
		payload := map[string]any{}
		if strings.TrimSpace(session.PayloadJSON) != "" {
			if err := json.Unmarshal([]byte(session.PayloadJSON), &payload); err != nil {
				return err
			}
		}
		payload["commit_validation_failed"] = true
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.Status = AlbumImportStatusNeedsAttention
		session.Stage = AlbumImportStageReady
		session.ErrorMessage = strings.TrimSpace(message)
		session.PayloadJSON = string(encoded)
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		return nil
	}); err != nil {
		return model.AlbumImportSession{}, err
	}
	return out, nil
}

func persistAlbumImportTrackLyrics(tx *gorm.DB, actorID, songID uuid.UUID, payload *AlbumImportTrackLyricsPayload, source string) error {
	if payload == nil {
		return nil
	}
	var current model.MusicSongLyric
	currentVersion := 0
	err := tx.First(&current, "song_id = ?", songID).Error
	if err == nil {
		currentVersion = current.Version
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	input := SaveLyricsInput{
		Target: "all", BaseVersion: &currentVersion,
		Content: payload.Content, Translation: payload.Translation,
		Format: payload.Format, Language: payload.Language,
		EditSummary: strings.TrimSpace(payload.EditSummary),
	}
	if input.EditSummary == "" {
		input.EditSummary = "添加歌词"
	}
	lines, err := prepareLyricsSave(tx, songID, &input)
	if err != nil {
		return err
	}
	if err := persistSongLyrics(tx, actorID, songID, input, lines, false); err != nil {
		return err
	}
	if strings.TrimSpace(source) == "" {
		return nil
	}
	return tx.Model(&model.MusicSongLyric{}).Where("song_id = ?", songID).Update("source", strings.TrimSpace(source)).Error
}

// FinalizeSubmittedAlbumImport creates a submitted album after media processing reaches ready.
func (s *Service) FinalizeSubmittedAlbumImport(id uuid.UUID) error {
	session, err := loadAlbumImportSession(s.db, id, nil)
	if err != nil {
		return err
	}
	if session.Status != AlbumImportStatusReady || session.UserID == nil {
		return nil
	}
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(session.PayloadJSON), &payload); err != nil {
		return err
	}
	rawRequest, ok := payload["commit_request"]
	if !ok {
		return nil
	}
	raw, err := json.Marshal(rawRequest)
	if err != nil {
		return err
	}
	var input CommitAlbumImportSessionInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	committed, err := s.CommitAlbumImportSession(authctx.CurrentUser{ID: *session.UserID, Role: authctx.RoleUser}, id, input)
	if err != nil {
		return err
	}
	return s.syncExternalImportTarget(committed)
}

func (s *Service) syncExternalImportTarget(session model.AlbumImportSession) error {
	if !s.db.Migrator().HasTable(&model.MusicExternalImport{}) {
		return nil
	}
	updates := map[string]any{"album_id": session.TargetAlbumID, "song_id": session.TargetSongID}
	return s.db.Model(&model.MusicExternalImport{}).
		Where("import_session_id = ?", session.ID).
		Updates(updates).Error
}

func findRepairSong(existingSongs []model.Song, seenSongIDs map[uuid.UUID]bool, track AlbumImportTrackPayload) *model.Song {
	discNumber := normalizedDiscNumber(track.DiscNumber)
	if track.TrackNumber > 0 {
		var positionMatches []*model.Song
		for index := range existingSongs {
			song := &existingSongs[index]
			if seenSongIDs[song.ID] || song.DeletedAt.Valid || song.Status == "closed" || song.LifecycleStatus == model.MusicLifecycleRetired {
				continue
			}
			if normalizedDiscNumber(song.DiscNumber) == discNumber && song.TrackNumber == track.TrackNumber {
				positionMatches = append(positionMatches, song)
			}
		}
		if len(positionMatches) == 1 {
			return positionMatches[0]
		}
	}

	title := strings.TrimSpace(track.Title)
	if title == "" {
		return nil
	}
	var titleMatches []*model.Song
	for index := range existingSongs {
		song := &existingSongs[index]
		if seenSongIDs[song.ID] || song.DeletedAt.Valid || song.Status == "closed" || song.LifecycleStatus == model.MusicLifecycleRetired {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(song.Title), title) {
			titleMatches = append(titleMatches, song)
		}
	}
	if len(titleMatches) == 1 {
		return titleMatches[0]
	}
	return nil
}

func (s *Service) promoteAlbumImportAsset(rawURL, destinationKey string, importID uuid.UUID) (string, string, string, error) {
	if s.s3 == nil || !strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		return rawURL, "", "", nil
	}
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	urlPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	sourceKey, ok := musicAlbumImportObjectKey(rawURL, urlPrefix)
	if bucket == "" || urlPrefix == "" || !ok || !isPromotableAlbumImportKey(sourceKey, importID) {
		return rawURL, "", "", nil
	}
	escapedSource := strings.ReplaceAll(url.PathEscape(bucket+"/"+sourceKey), "%2F", "/")
	if _, err := s.s3.CopyObject(&s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		CopySource: aws.String(escapedSource),
		Key:        aws.String(destinationKey),
	}); err != nil {
		return "", "", "", err
	}
	return urlPrefix + "/" + destinationKey, sourceKey, destinationKey, nil
}

func (s *Service) promoteImportedTrackAsset(rawURL, destinationKey string, importID uuid.UUID, uploadedAsset bool) (string, string, string, error) {
	if uploadedAsset {
		asset, err := storage.PromoteMusicUploadAsset(s.s3, rawURL, destinationKey)
		return asset.URL, asset.SourceKey, asset.DestinationKey, err
	}
	return s.promoteAlbumImportAsset(rawURL, destinationKey, importID)
}

func isPromotableAlbumImportKey(key string, importID uuid.UUID) bool {
	playbackPrefix := "music/album-imports/playback/sessions/" + importID.String() + "/"
	return strings.Contains(key, "/uploads/") || strings.HasPrefix(key, playbackPrefix)
}

func musicAlbumImportObjectKey(rawURL, urlPrefix string) (string, bool) {
	prefix := strings.TrimRight(strings.TrimSpace(urlPrefix), "/")
	if prefix == "" || !strings.HasPrefix(strings.TrimSpace(rawURL), prefix+"/") {
		return "", false
	}
	key, err := url.PathUnescape(strings.TrimPrefix(strings.TrimSpace(rawURL), prefix+"/"))
	return strings.TrimLeft(key, "/"), err == nil && key != ""
}

func (s *Service) deleteAlbumImportObjects(keys []string) {
	if s.s3 == nil || len(keys) == 0 {
		return
	}
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, err := s.s3.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
			log.Printf("delete album import object %s: %v", key, err)
		}
	}
}

type derivedTrackAudio struct {
	AudioURL        string
	FileID          string
	MatchStatus     string
	MatchProvider   string
	MatchExternalID string
	MatchSourceURL  string
	MatchConfidence float64
}

func matchDerivedTrackAudio(rawDerivedTracks []any, track AlbumImportTrackPayload, used map[int]bool) derivedTrackAudio {
	tryMatch := func(predicate func(map[string]any) bool) derivedTrackAudio {
		for i, rawTrack := range rawDerivedTracks {
			if used[i] {
				continue
			}
			trackMap, ok := rawTrack.(map[string]any)
			if !ok || !predicate(trackMap) {
				continue
			}
			used[i] = true
			return derivedTrackAudio{
				AudioURL: stringValue(trackMap["audio_url"]), FileID: stringValue(trackMap["file_id"]),
				MatchStatus: stringValue(trackMap["match_status"]), MatchProvider: stringValue(trackMap["match_provider"]),
				MatchExternalID: stringValue(trackMap["match_external_id"]), MatchSourceURL: stringValue(trackMap["match_source_url"]),
				MatchConfidence: floatValue(trackMap["match_confidence"]),
			}
		}
		return derivedTrackAudio{}
	}

	songID := strings.TrimSpace(track.SongID)
	if songID != "" {
		if audio := tryMatch(func(trackMap map[string]any) bool {
			return stringValue(trackMap["song_id"]) == songID
		}); audio.AudioURL != "" {
			return audio
		}
	}

	fileID := strings.TrimSpace(track.FileID)
	if fileID != "" {
		if audio := tryMatch(func(trackMap map[string]any) bool {
			return strings.TrimSpace(stringValue(trackMap["file_id"])) == fileID
		}); audio.AudioURL != "" {
			return audio
		}
	}

	audioKey := strings.TrimSpace(track.AudioKey)
	if audioKey != "" {
		if audio := tryMatch(func(trackMap map[string]any) bool {
			return strings.TrimSpace(stringValue(trackMap["audio_key"])) == audioKey
		}); audio.AudioURL != "" {
			return audio
		}
	}
	if songID != "" || fileID != "" || audioKey != "" {
		return derivedTrackAudio{}
	}

	title := strings.TrimSpace(track.Title)
	if track.TrackNumber > 0 {
		if audio := tryMatch(func(trackMap map[string]any) bool {
			return sameImportedTrackTitle(stringValue(trackMap["title"]), title) &&
				normalizedDiscNumber(int(int64Value(trackMap["disc_number"]))) == normalizedDiscNumber(track.DiscNumber) &&
				int(int64Value(trackMap["track_number"])) == track.TrackNumber
		}); audio.AudioURL != "" {
			return audio
		}
	}
	if audio := tryMatch(func(trackMap map[string]any) bool {
		return sameImportedTrackTitle(stringValue(trackMap["title"]), title)
	}); audio.AudioURL != "" {
		return audio
	}
	if track.AudioURL != "" {
		return derivedTrackAudio{AudioURL: strings.TrimSpace(track.AudioURL)}
	}
	return derivedTrackAudio{}
}

func sameImportedTrackTitle(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	if normalizedMusicText(left) == normalizedMusicText(right) {
		return true
	}
	return compactMusicText(left) == compactMusicText(right)
}

func importTrackMatchState(track AlbumImportTrackPayload, derived derivedTrackAudio) (string, string, string, string, float64, bool) {
	status := strings.TrimSpace(track.MatchStatus)
	provider := strings.ToLower(strings.TrimSpace(track.MatchProvider))
	externalID := strings.TrimSpace(track.MatchExternalID)
	sourceURL := strings.TrimSpace(track.MatchSourceURL)
	confidence := track.MatchConfidence
	if status == "" {
		status = strings.TrimSpace(derived.MatchStatus)
	}
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(derived.MatchProvider))
	}
	if externalID == "" {
		externalID = strings.TrimSpace(derived.MatchExternalID)
	}
	if sourceURL == "" {
		sourceURL = strings.TrimSpace(derived.MatchSourceURL)
	}
	if confidence == 0 {
		confidence = derived.MatchConfidence
	}
	if status == "" {
		status = model.MusicMatchUnmatched
	}
	if status == model.MusicMatchMatched && confidence == 0 {
		confidence = 1
	}
	return status, provider, externalID, sourceURL, confidence, status == model.MusicMatchManual
}

func upsertMusicMatchRecord(tx *gorm.DB, entityType string, entityID uuid.UUID, provider, externalID, sourceURL, status string, confidence float64, userOverridden bool, metadata any) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "metadata"
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = model.MusicMatchUnmatched
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var record model.MusicMatchRecord
	queryErr := tx.Where("entity_type = ? AND entity_id = ? AND provider = ?", entityType, entityID, provider).First(&record).Error
	matchedAt := (*time.Time)(nil)
	if status == model.MusicMatchMatched || status == model.MusicMatchManual {
		now := time.Now().UTC()
		matchedAt = &now
	}
	if errors.Is(queryErr, gorm.ErrRecordNotFound) {
		record = model.MusicMatchRecord{
			Base: model.Base{ID: uuid.New()}, EntityType: entityType, EntityID: entityID, Provider: provider,
		}
	} else if queryErr != nil {
		return queryErr
	}
	record.ExternalID = strings.TrimSpace(externalID)
	record.SourceURL = strings.TrimSpace(sourceURL)
	record.Status = status
	record.Confidence = confidence
	record.MatchedAt = matchedAt
	record.UserOverridden = userOverridden
	record.MetadataJSON = string(metadataJSON)
	return tx.Save(&record).Error
}

type songAudioMetadata struct {
	fileName      string
	container     string
	codec         string
	bitrateKbps   int
	sampleRateHz  int
	bitDepth      int
	channels      int
	sizeBytes     int64
	lossless      bool
	durationSec   int
	waveformPeaks json.RawMessage
}

func songAudioMetadataFromImportFile(file model.AlbumImportFile) songAudioMetadata {
	if file.ID == uuid.Nil {
		return songAudioMetadata{}
	}
	values := map[string]any{}
	_ = json.Unmarshal([]byte(file.MetadataJSON), &values)
	bitRate := int(int64Value(values["bit_rate"]))
	container := strings.TrimSpace(stringValue(values["container"]))
	if container == "" {
		container = strings.TrimSpace(file.DetectedFormat)
	}
	codec := strings.TrimSpace(stringValue(values["codec"]))
	waveformPeaks, _ := json.Marshal(values["waveform_peaks"])
	return songAudioMetadata{
		fileName: file.FileName, container: container, codec: codec, bitrateKbps: bitRate / 1000,
		sampleRateHz: int(int64Value(values["sample_rate"])), bitDepth: int(int64Value(values["bit_depth"])),
		channels: int(int64Value(values["channels"])), sizeBytes: file.Size,
		lossless: isLosslessAudio(container, codec), durationSec: int(file.DurationSeconds + 0.5), waveformPeaks: waveformPeaks,
	}
}

func applySongAudioMetadata(song *model.Song, metadata songAudioMetadata) {
	if metadata.fileName == "" {
		return
	}
	song.SourceFileName = metadata.fileName
	song.SourceContainer = metadata.container
	song.SourceCodec = metadata.codec
	song.SourceBitrateKbps = metadata.bitrateKbps
	song.SourceSampleRateHz = metadata.sampleRateHz
	song.SourceBitDepth = metadata.bitDepth
	song.SourceChannels = metadata.channels
	song.SourceSizeBytes = metadata.sizeBytes
	song.SourceLossless = metadata.lossless
	song.PlaybackContainer = "mp3"
	song.PlaybackCodec = "mp3"
	song.PlaybackBitrateKbps = 320
	song.PlaybackChannels = metadata.channels
	if len(metadata.waveformPeaks) > 0 && string(metadata.waveformPeaks) != "null" {
		song.WaveformPeaks = metadata.waveformPeaks
	}
	if metadata.durationSec > 0 {
		song.DurationSec = metadata.durationSec
	}
}

func isLosslessAudio(container, codec string) bool {
	value := strings.ToLower(container + " " + codec)
	return strings.Contains(value, "flac") || strings.Contains(value, "alac") || strings.Contains(value, "wav") || strings.Contains(value, "aiff")
}

type resolvedCommitAlbumImportArtist struct {
	Artist *model.Artist
	Roles  []AlbumArtistRoleInput
}

func resolveCommitAlbumImportArtists(tx *gorm.DB, user authctx.CurrentUser, input CommitAlbumImportSessionInput) ([]resolvedCommitAlbumImportArtist, error) {
	entries := make([]CommitAlbumImportArtistInput, 0, len(input.Artists))
	if len(input.Artists) > 0 {
		entries = append(entries, input.Artists...)
	} else if strings.TrimSpace(input.ArtistID) != "" || strings.TrimSpace(input.Artist.Name) != "" {
		entries = append(entries, CommitAlbumImportArtistInput{
			ArtistID:        input.ArtistID,
			Name:            input.Artist.Name,
			Disambiguation:  input.Artist.Disambiguation,
			LegalName:       input.Artist.LegalName,
			Bio:             input.Artist.Bio,
			ImageURL:        input.Artist.ImageURL,
			Nationality:     input.Artist.Nationality,
			BirthDate:       input.Artist.BirthDate,
			StageNames:      input.Artist.StageNames,
			BirthPlace:      input.Artist.BirthPlace,
			ArtistForm:      input.Artist.ArtistForm,
			ActiveStartDate: input.Artist.ActiveStartDate,
			ActiveEndDate:   input.Artist.ActiveEndDate,
			Members:         input.Artist.Members,
		})
	}

	out := make([]resolvedCommitAlbumImportArtist, 0, len(entries))
	for _, entry := range entries {
		artistID := strings.TrimSpace(entry.ArtistID)
		if artistID != "" {
			parsedArtistID, err := uuid.Parse(artistID)
			if err != nil {
				return nil, apperr.BadRequest("validation.invalid_request", "artist_id must be a valid UUID")
			}
			var artist model.Artist
			if err := tx.First(&artist, "id = ?", parsedArtistID).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return nil, apperr.NotFound("music.artist_not_found", "Artist not found")
				}
				return nil, err
			}
			if !canUseArtistDraft(artist, user) {
				return nil, apperr.NotFound("music.artist_not_found", "Artist not found")
			}
			out = append(out, resolvedCommitAlbumImportArtist{Artist: &artist, Roles: entry.Roles})
			continue
		}

		if strings.TrimSpace(entry.Name) == "" {
			return nil, apperr.BadRequest("validation.invalid_request", "artist name is required")
		}
		artist, err := buildArtistFromImportInput(entry)
		if err != nil {
			return nil, err
		}
		if hasAlbumArtistRole(entry.Roles, "primary") {
			if err := validateArtistPublicationFields(*artist, entry.Members); err != nil {
				return nil, err
			}
		}
		artist.CreatedBy = &user.ID
		if err := createAlbumImportArtist(tx, artist); err != nil {
			return nil, err
		}
		if err := validateArtistMemberReferences(tx, user, entry.Members); err != nil {
			return nil, err
		}
		if err := replaceArtistMembers(tx, artist.ID, entry.Members); err != nil {
			return nil, err
		}
		out = append(out, resolvedCommitAlbumImportArtist{Artist: artist, Roles: entry.Roles})
	}
	return out, nil
}

func albumImportTracksFromDerived(payload map[string]any) []AlbumImportTrackPayload {
	if payload == nil {
		return nil
	}
	rawTracks, ok := payload["derived_tracks"].([]any)
	if !ok {
		return nil
	}
	deleted := albumImportDeletedTrackKeys(payload)
	tracks := make([]AlbumImportTrackPayload, 0, len(rawTracks))
	for index, raw := range rawTracks {
		trackMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if importedTrackDeleted(trackMap, deleted) {
			continue
		}
		title := strings.TrimSpace(stringValue(trackMap["title"]))
		if title == "" {
			continue
		}
		trackNumber := int(int64Value(trackMap["track_number"]))
		if trackNumber <= 0 {
			trackNumber = index + 1
		}
		track := AlbumImportTrackPayload{
			SongID: stringValue(trackMap["song_id"]), FileID: stringValue(trackMap["file_id"]),
			AudioKey: stringValue(trackMap["audio_key"]), Title: title,
			DiscNumber: normalizedDiscNumber(int(int64Value(trackMap["disc_number"]))), TrackNumber: trackNumber,
			OriginalTitle: stringValue(trackMap["original_title"]),
			OriginalDisc:  int(int64Value(trackMap["original_disc_number"])),
			OriginalTrack: int(int64Value(trackMap["original_track_number"])),
			MatchStatus:   stringValue(trackMap["match_status"]), MatchProvider: stringValue(trackMap["match_provider"]),
			MatchExternalID: stringValue(trackMap["match_external_id"]), MatchSourceURL: stringValue(trackMap["match_source_url"]),
			MatchConfidence: floatValue(trackMap["match_confidence"]),
			LyricsSource:    stringValue(trackMap["lyrics_source"]),
		}
		if lyricsMap, ok := trackMap["lyrics"].(map[string]any); ok {
			track.Lyrics = &AlbumImportTrackLyricsPayload{
				Content: stringValue(lyricsMap["content"]), Translation: stringValue(lyricsMap["translation"]),
				Format: stringValue(lyricsMap["format"]), Language: stringValue(lyricsMap["language"]),
				EditSummary: stringValue(lyricsMap["edit_summary"]),
			}
		}
		tracks = append(tracks, track)
	}
	return tracks
}

func albumImportDeletedTrackKeys(payload map[string]any) map[string]bool {
	deleted := map[string]bool{}
	read := func(raw any) {
		values, ok := raw.([]any)
		if !ok {
			return
		}
		for _, value := range values {
			if key := strings.TrimSpace(stringValue(value)); key != "" {
				deleted[key] = true
			}
		}
	}
	read(payload["deleted_import_track_keys"])
	if request, ok := payload["commit_request"].(map[string]any); ok {
		read(request["deleted_import_track_keys"])
	}
	return deleted
}

func importedTrackDeleted(track map[string]any, deleted map[string]bool) bool {
	if len(deleted) == 0 {
		return false
	}
	disc := normalizedDiscNumber(int(int64Value(track["disc_number"])))
	trackNumber := int(int64Value(track["track_number"]))
	originalDisc := int(int64Value(track["original_disc_number"]))
	originalTrack := int(int64Value(track["original_track_number"]))
	keys := []string{
		"file:" + strings.TrimSpace(stringValue(track["file_id"])),
		"audio:" + strings.TrimSpace(stringValue(track["audio_key"])),
		"position:" + strconv.Itoa(disc) + ":" + strconv.Itoa(trackNumber),
		"position:" + strconv.Itoa(originalDisc) + ":" + strconv.Itoa(originalTrack),
	}
	for _, key := range keys {
		if deleted[key] {
			return true
		}
	}
	return false
}

func normalizedDiscNumber(value int) int {
	if value > 0 {
		return value
	}
	return 1
}

func buildArtistFromImportInput(input CommitAlbumImportArtistInput) (*model.Artist, error) {
	activeStartDate, activeStartDatePrecision, err := parseOptionalDate(input.ActiveStartDate, "active_start_date")
	if err != nil {
		return nil, err
	}
	activeEndDate, activeEndDatePrecision, err := parseOptionalDate(input.ActiveEndDate, "active_end_date")
	if err != nil {
		return nil, err
	}
	artist := &model.Artist{
		Name:            strings.TrimSpace(input.Name),
		Disambiguation:  strings.TrimSpace(input.Disambiguation),
		LegalName:       strings.TrimSpace(input.LegalName),
		Bio:             strings.TrimSpace(input.Bio),
		ImageURL:        strings.TrimSpace(input.ImageURL),
		Nationality:     strings.TrimSpace(input.Nationality),
		StageNamesJSON:  mustMarshalStageNames(input.StageNames),
		BirthPlace:      strings.TrimSpace(input.BirthPlace),
		ArtistForm:      normalizeArtistForm(input.ArtistForm),
		EntryStatus:     artistEntryDraft,
		LifecycleStatus: model.MusicLifecycleDraft,
		EditStatus:      model.MusicEditDevelopment,
	}
	birthDate, birthDatePrecision, err := parseOptionalDate(strings.TrimSpace(input.BirthDate), "birth_date")
	if err != nil {
		return nil, err
	}
	if birthDate != nil {
		artist.BirthDate = birthDate
		artist.BirthYear = birthDate.Year()
	}
	artist.BirthDatePrecision = birthDatePrecision
	if activeStartDate != nil {
		artist.ActiveStartDate = *activeStartDate
	}
	artist.ActiveStartDatePrecision = activeStartDatePrecision
	if activeEndDate != nil {
		artist.ActiveEndDate = *activeEndDate
	}
	artist.ActiveEndDatePrecision = activeEndDatePrecision
	return artist, nil
}

func createAlbumImportArtist(tx *gorm.DB, artist *model.Artist) error {
	return tx.Create(artist).Error
}

func hasAlbumArtistRole(roles []AlbumArtistRoleInput, wanted string) bool {
	if len(roles) == 0 {
		return wanted == "primary"
	}
	for _, role := range roles {
		if strings.EqualFold(strings.TrimSpace(role.Role), wanted) {
			return true
		}
	}
	return false
}

func createAlbumImportAlbum(tx *gorm.DB, album *model.Album) error {
	if albumImportCreateAlbumHook != nil {
		if err := albumImportCreateAlbumHook(tx, album); err != nil {
			return err
		}
	}
	return tx.Create(album).Error
}
