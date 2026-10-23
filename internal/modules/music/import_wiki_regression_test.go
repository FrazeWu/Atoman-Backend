package music

import (
	"atoman/internal/model"
	"encoding/json"
	"github.com/google/uuid"
	"testing"
)

func TestImportMinimalArtistPublicAndSeparateEditions(t *testing.T) {
	s, db, user := newMusicTestService(t)
	commit := func(input CommitAlbumImportSessionInput) model.AlbumImportSession {
		t.Helper()
		session, err := s.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
		if err != nil {
			t.Fatal(err)
		}
		seedReadyImportMedia(t, db, session.ID, "", "Track")
		result, err := s.CommitAlbumImportSession(user, session.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	input := CommitAlbumImportSessionInput{Artist: AlbumImportArtistPayload{Name: "Name only"}, Album: AlbumImportAlbumPayload{Title: "Album", Tracks: []AlbumImportTrackPayload{{Title: "Track", TrackNumber: 1}}, Tags: []AlbumImportTagPayload{{Kind: "type", Name: "External suggestion"}}}}
	first := commit(input)
	if first.TargetAlbumID == nil {
		t.Fatalf("minimal create failed: %s", first.ErrorMessage)
	}
	var artist model.Artist
	if err := db.First(&artist, "name=?", "Name only").Error; err != nil {
		t.Fatal(err)
	}
	if artist.LifecycleStatus != model.MusicLifecycleActive {
		t.Fatal("minimal artist stayed private")
	}
	response := performMusicJSONRequest(t, newMusicHTTPRouter(s, nil), "GET", "/api/v1/music/artists/"+artist.ID.String(), "")
	if response.Code != 200 {
		t.Fatalf("public artist inaccessible: %s", response.Body.String())
	}
	var count int64
	db.Model(&model.MusicTagAssignment{}).Where("entity_id=?", *first.TargetAlbumID).Count(&count)
	if count != 0 {
		t.Fatal("import created community assignments")
	}
	community, err := s.AddMusicTag(user, "album", *first.TargetAlbumID, "mood", "Community", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.VoteMusicTag(user, "album", *first.TargetAlbumID, community.ID, "down"); err != nil {
		t.Fatal(err)
	}
	repair, err := s.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&repair).Update("target_album_id", first.TargetAlbumID).Error; err != nil {
		t.Fatal(err)
	}
	seedReadyImportMedia(t, db, repair.ID, "", "Track")
	input.ArtistID = artist.ID.String()
	if result, err := s.CommitAlbumImportSession(user, repair.ID, input); err != nil || result.TargetAlbumID == nil {
		t.Fatalf("repair failed: %+v %v", result, err)
	}
	communityTags, err := s.ListMusicTags(&user, "album", *first.TargetAlbumID)
	if err != nil || len(communityTags) != 1 || communityTags[0].AssignmentID != community.AssignmentID || communityTags[0].Downvotes != 1 {
		t.Fatal("repair replaced community tags or votes", err)
	}
	input.ArtistID = artist.ID.String()
	input.Album.CanonicalAlbumID = first.TargetAlbumID
	input.Album.EditionType = "remaster"
	input.Album.ReleaseDate = "2026"
	second := commit(input)
	if second.TargetAlbumID == nil || *second.TargetAlbumID == *first.TargetAlbumID {
		t.Fatalf("edition failed: %s", second.ErrorMessage)
	}
	var edition model.Album
	if err := db.First(&edition, "id=?", second.TargetAlbumID).Error; err != nil {
		t.Fatal(err)
	}
	if edition.CanonicalAlbumID == nil || *edition.CanonicalAlbumID != *first.TargetAlbumID || edition.EditionType != "remaster" {
		t.Fatal("lost explicit edition relation")
	}
	var songs []model.Song
	if err := db.Where("album_id IN ?", []uuid.UUID{*first.TargetAlbumID, *second.TargetAlbumID}).Find(&songs).Error; err != nil {
		t.Fatal(err)
	}
	if len(songs) != 2 || songs[0].ID == songs[1].ID {
		t.Fatal("editions share song identity")
	}
	duplicate := commit(input)
	if duplicate.TargetAlbumID != nil || duplicate.Status != AlbumImportStatusNeedsAttention {
		t.Fatal("same edition imported twice")
	}
}

func TestImportRejectsClientOnlyAudioURL(t *testing.T) {
	s, db, user := newMusicTestService(t)
	session, err := s.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{Artist: AlbumImportArtistPayload{Name: "Forged"}, Album: AlbumImportAlbumPayload{Title: "Forged", Tracks: []AlbumImportTrackPayload{{Title: "Track", AudioURL: "https://untrusted.test/audio.mp3"}}}})
	if err == nil && result.Status == AlbumImportStatusCommitted {
		t.Fatal("client URL bypassed asset validation")
	}
	var count int64
	db.Model(&model.Artist{}).Where("name=?", "Forged").Count(&count)
	if count != 0 {
		t.Fatal("failed import left an orphan artist")
	}
}

func TestImportAmbiguousRequiresExplicitConfirmation(t *testing.T) {
	s, db, user := newMusicTestService(t)
	session, err := s.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatal(err)
	}
	seedReadyImportMedia(t, db, session.ID, "", "Track")
	if err = db.First(&session, "id=?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	json.Unmarshal([]byte(session.PayloadJSON), &payload)
	payload["metadata_match_status"] = "ambiguous"
	raw, _ := json.Marshal(payload)
	if err = db.Model(&session).Update("payload_json", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	input := CommitAlbumImportSessionInput{Artist: AlbumImportArtistPayload{Name: "Confirm artist"}, Album: AlbumImportAlbumPayload{Title: "Confirm album", Tracks: []AlbumImportTrackPayload{{Title: "Track"}}}}
	result, err := s.CommitAlbumImportSession(user, session.ID, input)
	if err == nil && result.Status == AlbumImportStatusCommitted {
		t.Fatal("ambiguous candidate silently published")
	}
	input.MetadataConfirmed = true
	result, err = s.CommitAlbumImportSession(user, session.ID, input)
	if err != nil || result.TargetAlbumID == nil {
		t.Fatalf("confirmed candidate failed: %s %v", result.ErrorMessage, err)
	}
}
