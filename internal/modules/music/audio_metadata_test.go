package music

import (
	"encoding/json"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestSongAudioMetadataFromImportFile(t *testing.T) {
	file := model.AlbumImportFile{
		Base:            model.Base{ID: uuid.New()},
		FileName:        "01 - Master.flac",
		DetectedFormat:  "flac",
		Size:            95 * 1024 * 1024,
		DurationSeconds: 248.4,
		MetadataJSON:    `{"container":"flac","codec":"flac","bit_rate":2763000,"sample_rate":96000,"bit_depth":24,"channels":2}`,
	}

	metadata := songAudioMetadataFromImportFile(file)
	song := model.Song{}
	applySongAudioMetadata(&song, metadata)

	if song.SourceFileName != "01 - Master.flac" || song.SourceContainer != "flac" || song.SourceCodec != "flac" {
		t.Fatalf("unexpected source identity: %#v", song)
	}
	if song.SourceBitrateKbps != 2763 || song.SourceSampleRateHz != 96000 || song.SourceBitDepth != 24 || song.SourceChannels != 2 {
		t.Fatalf("unexpected source specification: %#v", song)
	}
	if !song.SourceLossless || song.SourceSizeBytes != file.Size || song.DurationSec != 248 {
		t.Fatalf("unexpected archived source metadata: %#v", song)
	}
	if song.PlaybackContainer != "mp3" || song.PlaybackBitrateKbps != 320 || song.PlaybackChannels != 2 {
		t.Fatalf("unexpected playback metadata: %#v", song)
	}
}

func TestParseAudioProbeReadsTechnicalSpecification(t *testing.T) {
	metadata := parseAudioProbe([]byte(`{
		"format":{"duration":"248.4","format_name":"flac","bit_rate":"2763000","tags":{"title":"Master"}},
		"streams":[{"codec_name":"flac","sample_rate":"96000","bits_per_raw_sample":"24","channels":2}]
	}`))

	if metadata.container != "flac" || metadata.codec != "flac" || metadata.bitRate != 2763000 {
		t.Fatalf("unexpected parsed container metadata: %#v", metadata)
	}
	if metadata.sampleRate != 96000 || metadata.bitDepth != 24 || metadata.channels != 2 {
		t.Fatalf("unexpected parsed stream metadata: %#v", metadata)
	}
}

func TestCommitAlbumImportSessionCopiesAudioMetadataToSong(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	session := model.AlbumImportSession{UserID: &user.ID, Status: AlbumImportStatusReady}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create import session: %v", err)
	}
	file := model.AlbumImportFile{
		ImportID:        session.ID,
		FileName:        "01 - Master.flac",
		RelativePath:    "01 - Master.flac",
		Role:            AlbumImportFileRoleAudio,
		DetectedFormat:  "flac",
		Size:            95 * 1024 * 1024,
		DurationSeconds: 248.4,
		MetadataJSON:    `{"container":"flac","codec":"flac","bit_rate":2763000,"sample_rate":96000,"bit_depth":24,"channels":2}`,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("create import file: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"derived_tracks": []map[string]any{{
			"file_id": file.ID.String(), "title": "Master", "track_number": 1, "audio_url": "https://cdn.test/master.mp3",
		}},
	})
	if err := db.Model(&session).Update("payload_json", string(payload)).Error; err != nil {
		t.Fatalf("store derived tracks: %v", err)
	}

	_, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist:       completeAlbumImportArtistPayload("Archive Artist"),
		Album:        AlbumImportAlbumPayload{Title: "Archive Album", CoverURL: "https://cdn.test/cover.jpg", ReleaseDate: "2020-01-01", Tracks: []AlbumImportTrackPayload{{Title: "Master", TrackNumber: 1}}},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
	})
	if err != nil {
		t.Fatalf("commit import: %v", err)
	}

	var song model.Song
	if err := db.Where("title = ?", "Master").First(&song).Error; err != nil {
		t.Fatalf("load committed song: %v", err)
	}
	if song.SourceContainer != "flac" || song.SourceBitDepth != 24 || song.SourceSampleRateHz != 96000 || song.PlaybackBitrateKbps != 320 {
		t.Fatalf("expected committed archive metadata, got %#v", song)
	}
}
