package music

import (
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"os"
	"path"
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
)

var albumImportCreateAlbumHook func(*gorm.DB, *model.Album) error

func (s *Service) CommitAlbumImportSession(user authctx.CurrentUser, id uuid.UUID, input CommitAlbumImportSessionInput) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}

	var out model.AlbumImportSession
	oldObjectKeys := []string{}
	newObjectKeys := []string{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status == AlbumImportStatusCommitted {
			out = session
			return nil
		}
		if session.Status != AlbumImportStatusReady {
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

		album := model.Album{
			Title:       strings.TrimSpace(payload.Album.Title),
			Description: strings.TrimSpace(payload.Album.Description),
			ReleaseYear: payload.Album.ReleaseYear,
			Year:        payload.Album.ReleaseYear,
			CoverURL:    coverURL,
			CoverSource: coverSourceFromURL(coverURL),
			Status:      "open",
			EntryStatus: "open",
			AlbumType:   strings.TrimSpace(payload.Album.AlbumType),
			UploadedBy:  &user.ID,
			SourcesJSON: albumSourcesJSON,
			Sources:     albumSources,
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
		if err := replaceAlbumArtistCredits(tx, album.ID, credits, true); err != nil {
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
				"entry_status": artistEntryOpen,
				"sources_json": artistSourcesJSON,
			}).Error; err != nil {
				return err
			}
			resolved.Artist.EntryStatus = artistEntryOpen
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
			if err := tx.Where("album_id = ?", album.ID).Order("disc_number ASC, track_number ASC, created_at ASC").Find(&existingSongs).Error; err != nil {
				return err
			}
			for index := range existingSongs {
				existingSongsByID[existingSongs[index].ID] = &existingSongs[index]
			}
		}
		seenSongIDs := map[uuid.UUID]bool{}
		for index, track := range payload.Album.Tracks {
			audioURL := strings.TrimSpace(track.AudioURL)
			derived := derivedTrackAudio{}
			if audioURL == "" {
				derived = matchDerivedTrackAudio(rawDerivedTracks, track, index, usedDerivedTrackIndexes)
				audioURL = derived.AudioURL
			}
			metadata := songAudioMetadataFromImportFile(importFilesByID[derived.FileID])
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
			}
			if existingSong != nil {
				song := *existingSong
				if strings.TrimSpace(song.AudioURL) == "" {
					return apperr.BadRequest("validation.invalid_request", "every track must have processed audio")
				}
				song.Title = strings.TrimSpace(track.Title)
				song.TrackNumber = track.TrackNumber
				song.DiscNumber = normalizedDiscNumber(track.DiscNumber)
				song.Status = "open"
				song.ReleaseDate = album.ReleaseDate
				song.ReleaseDatePrecision = album.ReleaseDatePrecision
				if strings.TrimSpace(audioURL) != "" && audioURL != song.AudioURL {
					promotedAudioURL, oldAudioKey, newAudioKey, err := s.promoteAlbumImportAsset(
						audioURL,
						storage.BuildMusicAlbumTrackVersionKey(album.ID.String(), song.ID.String(), uuid.NewString(), path.Ext(audioURL)),
						id,
					)
					if err != nil {
						return err
					}
					if newAudioKey != "" {
						song.AudioURL = promotedAudioURL
						song.AudioSource = "s3"
						oldObjectKeys = append(oldObjectKeys, oldAudioKey)
						newObjectKeys = append(newObjectKeys, newAudioKey)
					}
				}
				applySongAudioMetadata(&song, metadata)
				if err := tx.Save(&song).Error; err != nil {
					return err
				}
				if err := tx.Model(&song).Association("Artists").Replace(artists); err != nil {
					return err
				}
				if err := persistAlbumImportTrackLyrics(tx, user.ID, song.ID, track.Lyrics); err != nil {
					return err
				}
				continue
			}
			if strings.TrimSpace(audioURL) == "" {
				return apperr.BadRequest("validation.invalid_request", "every track must have processed audio")
			}

			song := model.Song{
				Title:                strings.TrimSpace(track.Title),
				TrackNumber:          track.TrackNumber,
				DiscNumber:           normalizedDiscNumber(track.DiscNumber),
				ReleaseDate:          album.ReleaseDate,
				ReleaseDatePrecision: album.ReleaseDatePrecision,
				AlbumID:              &album.ID,
				Status:               "open",
				AudioURL:             audioURL,
				AudioSource:          coverSourceFromURL(audioURL),
				UploadedBy:           &user.ID,
			}
			applySongAudioMetadata(&song, metadata)
			if err := tx.Create(&song).Error; err != nil {
				return err
			}
			promotedAudioURL, oldAudioKey, newAudioKey, err := s.promoteAlbumImportAsset(
				song.AudioURL,
				storage.BuildMusicAlbumTrackVersionKey(album.ID.String(), song.ID.String(), uuid.NewString(), path.Ext(song.AudioURL)),
				id,
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
			if err := persistAlbumImportTrackLyrics(tx, user.ID, song.ID, track.Lyrics); err != nil {
				return err
			}
		}
		if isRepair {
			for _, song := range existingSongs {
				if seenSongIDs[song.ID] {
					continue
				}
				if err := tx.Model(&song).Update("status", "closed").Error; err != nil {
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
		s.notifyIndexNowAlbum(out.TargetAlbumID)
	}
	return out, nil
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

func (s *Service) markAlbumImportNeedsAttention(userID, importID uuid.UUID, message string) (model.AlbumImportSession, error) {
	if err := s.db.Model(&model.AlbumImportSession{}).
		Where("id = ? AND user_id = ? AND status = ?", importID, userID, AlbumImportStatusReady).
		Updates(map[string]any{
			"status":        AlbumImportStatusNeedsAttention,
			"stage":         AlbumImportStageReady,
			"error_message": strings.TrimSpace(message),
		}).Error; err != nil {
		return model.AlbumImportSession{}, err
	}
	return loadAlbumImportSession(s.db, importID, &userID)
}

func persistAlbumImportTrackLyrics(tx *gorm.DB, actorID, songID uuid.UUID, payload *AlbumImportTrackLyricsPayload) error {
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
	return persistSongLyrics(tx, actorID, songID, input, lines, false)
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
	_, err = s.CommitAlbumImportSession(authctx.CurrentUser{ID: *session.UserID, Role: authctx.RoleUser}, id, input)
	return err
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
	AudioURL string
	FileID   string
}

func matchDerivedTrackAudio(rawDerivedTracks []any, track AlbumImportTrackPayload, index int, used map[int]bool) derivedTrackAudio {
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
			return derivedTrackAudio{AudioURL: stringValue(trackMap["audio_url"]), FileID: stringValue(trackMap["file_id"])}
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

	title := strings.TrimSpace(track.Title)
	if track.TrackNumber > 0 {
		if audio := tryMatch(func(trackMap map[string]any) bool {
			return strings.TrimSpace(stringValue(trackMap["title"])) == title &&
				normalizedDiscNumber(int(int64Value(trackMap["disc_number"]))) == normalizedDiscNumber(track.DiscNumber) &&
				int(int64Value(trackMap["track_number"])) == track.TrackNumber
		}); audio.AudioURL != "" {
			return audio
		}
	}
	if audio := tryMatch(func(trackMap map[string]any) bool {
		return strings.TrimSpace(stringValue(trackMap["title"])) == title
	}); audio.AudioURL != "" {
		return audio
	}
	if index >= 0 && index < len(rawDerivedTracks) && !used[index] {
		if trackMap, ok := rawDerivedTracks[index].(map[string]any); ok {
			used[index] = true
			return derivedTrackAudio{AudioURL: stringValue(trackMap["audio_url"]), FileID: stringValue(trackMap["file_id"])}
		}
	}
	return derivedTrackAudio{}
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
	tracks := make([]AlbumImportTrackPayload, 0, len(rawTracks))
	for index, raw := range rawTracks {
		trackMap, ok := raw.(map[string]any)
		if !ok {
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
			SongID: stringValue(trackMap["song_id"]), Title: title,
			DiscNumber: normalizedDiscNumber(int(int64Value(trackMap["disc_number"]))), TrackNumber: trackNumber,
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
		Name:           strings.TrimSpace(input.Name),
		Disambiguation: strings.TrimSpace(input.Disambiguation),
		LegalName:      strings.TrimSpace(input.LegalName),
		Bio:            strings.TrimSpace(input.Bio),
		ImageURL:       strings.TrimSpace(input.ImageURL),
		Nationality:    strings.TrimSpace(input.Nationality),
		StageNamesJSON: mustMarshalStageNames(input.StageNames),
		BirthPlace:     strings.TrimSpace(input.BirthPlace),
		ArtistForm:     normalizeArtistForm(input.ArtistForm),
		EntryStatus:    artistEntryDraft,
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
	if err := ensureArtistDisplayNameAvailable(tx, artist.Name, artist.Disambiguation, nil); err != nil {
		return err
	}
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
