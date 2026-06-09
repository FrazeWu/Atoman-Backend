package model

import (
	"testing"

	"github.com/google/uuid"

	"atoman/internal/testdb"
)

func TestMusicEditModelsMigrateAndCreate(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&MusicEdit{},
		&MusicEditVote{},
		&MusicEditDecision{},
		&MusicEditChange{},
	)

	submitterID := uuid.Must(uuid.NewV7())
	user := User{
		UUID:     submitterID,
		Username: "editor",
		Email:    "editor@example.com",
		Password: "pw",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create submitter: %v", err)
	}

	entityID := uuid.Must(uuid.NewV7())
	edit := MusicEdit{
		Type:        "update_song",
		EntityType:  "song",
		EntityID:    &entityID,
		SubmittedBy: submitterID,
		Status:      "open",
		Reason:      "补充来源",
		PayloadJSON: `{"title":"New Title"}`,
		ChangesJSON: `{"title":{"to":"New Title"}}`,
		SourcesJSON: `[{"url":"https://example.com/source"}]`,
		AutoApplied: false,
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create music edit: %v", err)
	}

	vote := MusicEditVote{
		EditID:  edit.ID,
		UserID:  submitterID,
		Vote:    "yes",
		Comment: "looks good",
	}
	if err := db.Create(&vote).Error; err != nil {
		t.Fatalf("create music edit vote: %v", err)
	}

	decision := MusicEditDecision{
		EditID:    edit.ID,
		DeciderID: submitterID,
		Decision:  "approve",
		Reason:    "verified",
	}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatalf("create music edit decision: %v", err)
	}

	change := MusicEditChange{
		EditID:     edit.ID,
		EntityType: "song",
		EntityID:   &entityID,
		BeforeJSON: `{"title":"Old Title"}`,
		AfterJSON:  `{"title":"New Title"}`,
	}
	if err := db.Create(&change).Error; err != nil {
		t.Fatalf("create music edit change: %v", err)
	}

	duplicateVote := MusicEditVote{
		EditID:  edit.ID,
		UserID:  submitterID,
		Vote:    "no",
		Comment: "duplicate should fail",
	}
	if err := db.Create(&duplicateVote).Error; err == nil {
		t.Fatal("expected duplicate user vote for same edit to fail")
	}

	if edit.ID == uuid.Nil {
		t.Fatal("expected music edit ID to be generated")
	}
	if vote.ID == uuid.Nil {
		t.Fatal("expected music edit vote ID to be generated")
	}
	if decision.ID == uuid.Nil {
		t.Fatal("expected music edit decision ID to be generated")
	}
	if change.ID == uuid.Nil {
		t.Fatal("expected music edit change ID to be generated")
	}
}
