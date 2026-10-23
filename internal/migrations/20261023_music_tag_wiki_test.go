package migrations

import (
	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
	"testing"
)

func TestMusicTagWikiMigrationRepeatPreservesHistory(t *testing.T) {
	db := testdb.OpenPostgres(t, "music_tag_wiki_migration")
	if err := RunMusicTagsMigration(db); err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "migration-user", Email: "migration@test.invalid", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	tag := model.MusicTag{Name: "Folk", NormalizedName: "folk", Kind: "type", CreatedBy: user.UUID}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	assignment := model.MusicTagAssignment{EntityType: "song", EntityID: uuid.New(), TagID: tag.ID, CreatedBy: tag.CreatedBy}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	vote := model.MusicTagVote{AssignmentID: assignment.ID, UserID: tag.CreatedBy, Vote: "down"}
	if err := db.Create(&vote).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RunMusicTagWikiMigration(db); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&model.MusicTagVote{}, "id=?", vote.ID).Error; err != nil {
		t.Fatal("migration removed votes", err)
	}
	if err := db.First(&model.MusicTagAssignment{}, "id=?", assignment.ID).Error; err != nil {
		t.Fatal("migration removed assignments", err)
	}
	if !db.Migrator().HasTable(&model.MusicTagRevision{}) {
		t.Fatal("missing revision table")
	}
}
