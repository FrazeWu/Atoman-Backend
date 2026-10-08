package music

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestImportCreationRegressionDraftOnlyNeverSubmits(t *testing.T) {
	for _, status := range []string{AlbumImportStatusReady, AlbumImportStatusUploading} {
		t.Run(status, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&session).Update("status", status).Error; err != nil {
				t.Fatal(err)
			}
			// 信息完整且音频已就绪，确保失败来自误提交，而非校验未通过。
			seedReadyImportMedia(t, db, session.ID, "https://cdn.test/draft-cover.jpg", "草稿曲目")
			before, beforePayload := loadCreationRegressionPayload(t, db, session.ID)
			input := CommitAlbumImportSessionInput{
				Artist: completeAlbumImportArtistPayload("草稿艺人"),
				Album: AlbumImportAlbumPayload{
					Title: "草稿专辑", CoverURL: "https://cdn.test/draft-cover.jpg", ReleaseDate: "2020-01-01",
					Tracks: []AlbumImportTrackPayload{{Title: "草稿曲目", TrackNumber: 1}},
				},
				ArtistSource: "https://source.test/artist", AlbumSource: "https://source.test/album",
			}
			// 从 API 的 JSON 输入设置新字段，不让字段缺失阻断其他回归测试编译。
			if err := json.Unmarshal([]byte(`{"draft_only":true}`), &input); err != nil {
				t.Fatal(err)
			}
			assertCreationRegressionJSONFlag(t, input, "draft_only")

			saved, err := svc.CommitAlbumImportSession(user, session.ID, input)
			if err != nil {
				t.Fatalf("保存草稿失败：%v", err)
			}
			if saved.Status != before.Status {
				t.Errorf("保存草稿改变了返回状态：%q，期望 %q", saved.Status, before.Status)
			}
			stored, payload := loadCreationRegressionPayload(t, db, session.ID)
			assertCreationRegressionUnsubmitted(t, db, stored, before.Status, payload)
			if !reflect.DeepEqual(payload["derived_tracks"], beforePayload["derived_tracks"]) {
				t.Error("保存草稿改变了已处理音频")
			}
			rawDraft, ok := payload["draft_request"]
			if !ok {
				t.Error("填写内容没有保存到 draft_request")
			} else {
				encoded, err := json.Marshal(rawDraft)
				if err != nil {
					t.Fatal(err)
				}
				var draft CommitAlbumImportSessionInput
				if err := json.Unmarshal(encoded, &draft); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(draft.Artist, input.Artist) || !reflect.DeepEqual(draft.Album, input.Album) ||
					draft.ArtistSource != input.ArtistSource || draft.AlbumSource != input.AlbumSource {
					t.Errorf("draft_request 丢失填写内容：%#v", draft)
				}
			}

			if err := svc.FinalizeSubmittedAlbumImport(session.ID); err != nil {
				t.Fatalf("草稿不应触发后台创建：%v", err)
			}
			finalized, finalizedPayload := loadCreationRegressionPayload(t, db, session.ID)
			assertCreationRegressionUnsubmitted(t, db, finalized, before.Status, finalizedPayload)
			if finalized.PayloadJSON != stored.PayloadJSON {
				t.Error("后台完成处理改变了草稿 payload")
			}
		})
	}
}

func TestImportCreationRegressionFileIdentityWinsOverSharedOrigin(t *testing.T) {
	tracks := []map[string]any{{"file_id": "file-b", "origin": "archive"}, {"file_id": "file-a", "origin": "archive"}}
	mergeImportTrackAudio(tracks, []map[string]any{
		{"file_id": "file-a", "origin": "archive", "audio_key": "a.mp3"},
		{"file_id": "file-b", "origin": "archive", "audio_key": "b.mp3"},
	})
	if tracks[0]["audio_key"] != "b.mp3" || tracks[1]["audio_key"] != "a.mp3" {
		t.Fatalf("同一通用来源覆盖了文件身份：%#v", tracks)
	}
}

func TestImportCreationRegressionMetadataCannotDeclareProcessedAudio(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.WithAlbumImportMetadataEnricher(&creationRegressionMetadataEnricher{})
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, AlbumImportMetadataPreviewInput{
		Artist: "测试艺人", AlbumTitle: "未处理专辑",
		Tracks: []AlbumImportMetadataPreviewTrack{
			{Title: "曲目一", Origin: "01-track.flac", AudioKey: "unprocessed.mp3", AudioURL: "https://cdn.test/unprocessed.mp3"},
			{Title: "曲目二", Origin: "02-track.flac", AudioKey: "unprocessed-2.mp3", AudioURL: "https://cdn.test/unprocessed-2.mp3"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, payload := loadCreationRegressionPayload(t, db, session.ID)
	for _, track := range creationRegressionDerivedTracks(t, payload) {
		if stringValue(track["audio_key"]) != "" || stringValue(track["audio_url"]) != "" {
			t.Fatalf("匹配把客户端音频字段变成了已处理结果：%#v", track)
		}
	}
}

func TestImportCreationRegressionForceMetadataRetryPreservesAudioIdentity(t *testing.T) {
	for _, failed := range []bool{true, false} {
		name := "unmatched"
		if failed {
			name = "lookup_error"
		}
		t.Run(name, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			enricher := &creationRegressionMetadataEnricher{fail: failed}
			svc.WithAlbumImportMetadataEnricher(enricher)
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
			if err != nil {
				t.Fatal(err)
			}
			input := AlbumImportMetadataPreviewInput{
				AlbumTitle: "初次专辑名", Artist: "测试艺人",
				Tracks: []AlbumImportMetadataPreviewTrack{
					{Title: "本地 A", FileID: uuid.NewString(), AudioKey: "audio/a.mp3", AudioURL: "https://cdn.test/audio/a.mp3", Origin: "disc/01-a.flac", DiscNumber: 1, TrackNumber: 1, OriginalTitle: "本地 A", OriginalDisc: 1, OriginalTrack: 1},
					{Title: "本地 B", FileID: uuid.NewString(), AudioKey: "audio/b.mp3", AudioURL: "https://cdn.test/audio/b.mp3", Origin: "disc/02-b.flac", DiscNumber: 1, TrackNumber: 2, OriginalTitle: "本地 B", OriginalDisc: 1, OriginalTrack: 2},
				},
			}
			storedTracks := make([]map[string]any, 0, len(input.Tracks))
			for _, track := range input.Tracks {
				storedTracks = append(storedTracks, map[string]any{"file_id": track.FileID, "audio_key": track.AudioKey, "audio_url": track.AudioURL, "origin": track.Origin})
			}
			encoded, _ := json.Marshal(map[string]any{"derived_tracks": storedTracks})
			if err := db.Model(&session).Update("payload_json", string(encoded)).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, input); err != nil {
				t.Fatalf("首次匹配失败应保存诊断结果：%v", err)
			}
			_, firstPayload := loadCreationRegressionPayload(t, db, session.ID)
			if firstPayload["metadata_match_status"] != model.MusicMatchUnmatched {
				t.Fatalf("首次匹配应为 unmatched：%#v", firstPayload)
			}
			if failed && stringValue(firstPayload["metadata_error"]) == "" {
				t.Error("首次外部匹配失败没有保存诊断信息")
			}

			// 更正输入并显式强制重试，成功结果必须替换首次失败结果。
			enricher.fail, enricher.matched = false, true
			input.AlbumTitle = "更正后的专辑名"
			if err := json.Unmarshal([]byte(`{"force":true}`), &input); err != nil {
				t.Fatal(err)
			}
			assertCreationRegressionJSONFlag(t, input, "force")
			if _, err := svc.MatchAlbumImportMetadata(context.Background(), user, session.ID, input); err != nil {
				t.Fatalf("强制再次匹配失败：%v", err)
			}
			_, payload := loadCreationRegressionPayload(t, db, session.ID)
			if payload["metadata_match_status"] != model.MusicMatchMatched || payload["derived_album_title"] != input.AlbumTitle ||
				payload["metadata_external_id"] != "retry-release" || payload["metadata_match_locked"] != true {
				t.Errorf("强制重试未保存成功结果：%#v", payload)
			}
			tracks := creationRegressionDerivedTracks(t, payload)
			for index, track := range tracks {
				original := input.Tracks[1-index]
				if track["title"] != []string{"标准 B", "标准 A"}[index] || int64Value(track["track_number"]) != int64(index+1) {
					t.Errorf("重试后未保存标准曲名及新曲序：%#v", track)
				}
				for key, want := range map[string]string{"file_id": original.FileID, "audio_key": original.AudioKey, "audio_url": original.AudioURL, "origin": original.Origin} {
					if track[key] != want {
						t.Errorf("重试后第 %d 首 %s = %#v，期望 %q", index+1, key, track[key], want)
					}
				}
			}
		})
	}
}

func TestImportCreationRegressionLockedTracksBindAudioBySource(t *testing.T) {
	for _, identity := range []string{"file_id", "origin", "file_id_and_origin"} {
		t.Run(identity, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
			if err != nil {
				t.Fatal(err)
			}
			files := []model.AlbumImportFile{
				{ImportID: session.ID, FileName: "01-a.flac", RelativePath: "disc/01-a.flac", Title: "本地 A", DiscNumber: 1, TrackNumber: 1, PlaybackKey: "audio/a.mp3"},
				{ImportID: session.ID, FileName: "02-b.flac", RelativePath: "disc/02-b.flac", Title: "本地 B", DiscNumber: 1, TrackNumber: 2, PlaybackKey: "audio/b.mp3"},
			}
			if err := db.Create(&files).Error; err != nil {
				t.Fatal(err)
			}
			localTracks := make([]AlbumImportMetadataTrack, 0, len(files))
			localLyrics := map[string]AlbumImportTrackLyricsPayload{}
			for _, file := range files {
				localTracks = append(localTracks, AlbumImportMetadataTrack{
					Title: file.Title, FileID: file.ID.String(), Origin: file.RelativePath,
					DiscNumber: file.DiscNumber, TrackNumber: file.TrackNumber, AudioKey: file.PlaybackKey,
				})
				// 仅按原始曲序提供歌词，避免新曲序串歌被 origin 查找掩盖。
				localLyrics[lyricSequenceKey(file.DiscNumber, file.TrackNumber)] = AlbumImportTrackLyricsPayload{
					Content: file.Title + " 的本地歌词", Translation: file.Title + " 的翻译", Format: "plain", Language: "zh",
				}
			}
			derived := make([]map[string]any, 0, len(files))
			for index, file := range []model.AlbumImportFile{files[1], files[0]} {
				track := map[string]any{
					"title": []string{"标准 B", "标准 A"}[index], "disc_number": 1, "track_number": index + 1,
					"original_title": file.Title, "original_disc_number": file.DiscNumber, "original_track_number": file.TrackNumber,
					"match_status": model.MusicMatchMatched,
				}
				if identity != "origin" {
					track["file_id"] = file.ID.String()
				}
				if identity != "file_id" {
					track["origin"] = file.RelativePath
				}
				derived = append(derived, track)
			}
			payload := map[string]any{"metadata_match_locked": true, "metadata_match_status": model.MusicMatchMatched, "derived_tracks": derived}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&session).Update("payload_json", string(encoded)).Error; err != nil {
				t.Fatal(err)
			}
			processor := &MediaImportProcessor{db: db, urlPrefix: "https://cdn.test"}
			if err := processor.persistLockedMetadataTracks(context.Background(), &session, files, localTracks, localLyrics, payload); err != nil {
				t.Fatal(err)
			}
			_, persisted := loadCreationRegressionPayload(t, db, session.ID)
			tracks := creationRegressionDerivedTracks(t, persisted)
			for index, track := range tracks {
				file := files[1-index]
				for key, want := range map[string]string{"file_id": file.ID.String(), "audio_key": file.PlaybackKey, "audio_url": "https://cdn.test/" + file.PlaybackKey} {
					if track[key] != want {
						t.Errorf("新曲序第 %d 首 %s = %#v，期望来源文件的 %q", index+1, key, track[key], want)
					}
				}
				if identity != "file_id" && track["origin"] != file.RelativePath {
					t.Errorf("来源路径改变：%#v", track)
				}
				if track["title"] != derived[index]["title"] || int64Value(track["track_number"]) != int64(index+1) {
					t.Errorf("音频回填覆盖了匹配后的曲名或曲序：%#v", track)
				}
				lyricsJSON, err := json.Marshal(track["lyrics"])
				if err != nil {
					t.Fatal(err)
				}
				var lyrics AlbumImportTrackLyricsPayload
				if err := json.Unmarshal(lyricsJSON, &lyrics); err != nil {
					t.Fatal(err)
				}
				wantLyrics := localLyrics[lyricSequenceKey(file.DiscNumber, file.TrackNumber)]
				if lyrics != wantLyrics || track["lyrics_source"] != "local" {
					t.Errorf("新曲序第 %d 首没有保留来源文件的本地歌词：%#v，期望 %#v", index+1, track, wantLyrics)
				}
			}
		})
	}
}

type creationRegressionMetadataEnricher struct {
	fail    bool
	matched bool
}

func (e *creationRegressionMetadataEnricher) Enrich(_ context.Context, input AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
	if e.fail {
		return AlbumImportMetadataResult{}, errors.New("测试外部匹配服务失败")
	}
	result := AlbumImportMetadataResult{AlbumTitle: input.AlbumTitle, MatchStatus: model.MusicMatchUnmatched, Tracks: baseMetadataTracks(input.Tracks)}
	if e.matched {
		result.MatchStatus, result.MetadataSource, result.ExternalID = model.MusicMatchMatched, "musicbrainz", "retry-release"
		result.Tracks[0], result.Tracks[1] = result.Tracks[1], result.Tracks[0]
		for index := range result.Tracks {
			result.Tracks[index].Title = []string{"标准 B", "标准 A"}[index]
			result.Tracks[index].TrackNumber = index + 1
			result.Tracks[index].MatchStatus = model.MusicMatchMatched
		}
	}
	return result, nil
}

func assertCreationRegressionJSONFlag(t *testing.T, input any, key string) {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload[key] != true {
		t.Errorf("请求必须支持 bool 类型的 JSON 字段 %s：%s", key, encoded)
	}
}

func loadCreationRegressionPayload(t *testing.T, db *gorm.DB, id uuid.UUID) (model.AlbumImportSession, map[string]any) {
	t.Helper()
	var session model.AlbumImportSession
	if err := db.First(&session, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	return session, payload
}

func creationRegressionDerivedTracks(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	encoded, err := json.Marshal(payload["derived_tracks"])
	if err != nil {
		t.Fatal(err)
	}
	var tracks []map[string]any
	if err := json.Unmarshal(encoded, &tracks); err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("期望保存两首曲目，实际为 %#v", tracks)
	}
	return tracks
}

func assertCreationRegressionUnsubmitted(t *testing.T, db *gorm.DB, session model.AlbumImportSession, status string, payload map[string]any) {
	t.Helper()
	if session.Status != status || session.TargetAlbumID != nil || session.TargetSongID != nil || session.CommittedAt != nil || session.CommittedBy != nil {
		t.Errorf("草稿被标记为已提交或改变状态：%#v", session)
	}
	if _, ok := payload["commit_request"]; ok {
		t.Error("保存草稿不应写入 commit_request")
	}
	for _, entity := range []any{&model.Artist{}, &model.Album{}} {
		var count int64
		if err := db.Model(entity).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("保存草稿不应创建 %T，实际数量为 %d", entity, count)
		}
	}
}
