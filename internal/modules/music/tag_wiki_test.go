package music

import (
	"fmt"
	"sync"
	"testing"

	"atoman/internal/model"
	"github.com/google/uuid"
)

func TestMusicTagWikiMergePreviewAndSafeRevert(t *testing.T) {
	s, db, user := newMusicHTTPTestService(t)
	source, _, err := s.CreateMusicTag(user, "type", "Folk rock", nil)
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := s.CreateMusicTag(user, "type", "Folk", nil)
	if err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Song", LifecycleStatus: model.MusicLifecycleActive, Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	a, err := s.AddMusicTag(user, "song", song.ID, "type", source.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddMusicTag(user, "song", song.ID, "type", target.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{source.ID, target.ID} {
		if _, err := s.VoteMusicTag(user, "song", song.ID, id, "down"); err != nil {
			t.Fatal(err)
		}
	}
	input := MusicTagWikiInput{BaseVersion: source.Version, TargetID: &target.ID, TargetVersion: target.Version}
	if _, err := s.EditMusicTag(user, source.ID, "merge", input, true); err != nil {
		t.Fatal(err)
	}
	tags, err := s.ListMusicTags(&user, "song", song.ID)
	if err != nil || len(tags) != 2 {
		t.Fatalf("preview changed data: %+v %v", tags, err)
	}
	if _, err := s.EditMusicTag(user, source.ID, "merge", input, false); err != nil {
		t.Fatal(err)
	}
	tags, err = s.ListMusicTags(&user, "song", song.ID)
	if err != nil || len(tags) != 1 || tags[0].Downvotes != 1 || tags[0].AssignmentID != b.AssignmentID {
		t.Fatalf("merge lost identity or duplicated vote: %+v %v", tags, err)
	}
	old, err := s.GetMusicTag(source.ID)
	if err != nil || old.CanonicalTagID == nil || *old.CanonicalTagID != target.ID || old.SongCount != 1 {
		t.Fatalf("old ID cannot resolve: %+v %v", old, err)
	}
	history, err := s.MusicTagHistory(&user, "tag", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision := history.Revisions[0].ID
	if _, err := s.EditMusicTag(user, source.ID, "revert", MusicTagWikiInput{BaseVersion: 2, RevisionID: &revision}, false); err != nil {
		t.Fatal(err)
	}
	tags, err = s.ListMusicTags(&user, "song", song.ID)
	if err != nil || len(tags) != 2 {
		t.Fatalf("revert failed: %+v %v", tags, err)
	}
	var restored model.MusicTagAssignment
	if err := db.First(&restored, "id=?", a.AssignmentID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMusicTag(source.ID)
	if err != nil || got.Version != 3 {
		t.Fatalf("version did not advance: %+v %v", got, err)
	}
}

func TestMusicTagAssignmentAtomicReplaceRestoreAndConflict(t *testing.T) {
	s, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Song", LifecycleStatus: model.MusicLifecycleActive, Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for i := 0; i < 13; i++ {
		tag, _, err := s.CreateMusicTag(user, "mood", fmt.Sprintf("Mood %d", i), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tag.ID)
	}
	h, err := s.EditMusicTagAssignments(user, "song", song.ID, MusicTagAssignmentEdit{Add: ids[:12]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EditMusicTagAssignments(user, "song", song.ID, MusicTagAssignmentEdit{BaseVersion: 0, Remove: ids[:1]}); err == nil {
		t.Fatal("stale version accepted")
	}
	if _, err = s.EditMusicTagAssignments(user, "song", song.ID, MusicTagAssignmentEdit{BaseVersion: h.Version, Add: ids[12:]}); err == nil {
		t.Fatal("13th tag accepted")
	}
	tags, _ := s.ListMusicTags(&user, "song", song.ID)
	var original uuid.UUID
	for _, tag := range tags {
		if tag.ID == ids[0] {
			original = tag.AssignmentID
		}
	}
	if _, err = s.VoteMusicTag(user, "song", song.ID, ids[0], "down"); err != nil {
		t.Fatal(err)
	}
	h, err = s.EditMusicTagAssignments(user, "song", song.ID, MusicTagAssignmentEdit{BaseVersion: h.Version, Remove: ids[:1], Add: ids[12:]})
	if err != nil {
		t.Fatal(err)
	}
	h, err = s.EditMusicTagAssignments(user, "song", song.ID, MusicTagAssignmentEdit{BaseVersion: h.Version, Remove: ids[12:], Add: ids[:1]})
	if err != nil {
		t.Fatal(err)
	}
	tags, _ = s.ListMusicTags(&user, "song", song.ID)
	for _, tag := range tags {
		if tag.ID == ids[0] && (tag.AssignmentID != original || tag.Downvotes != 1) {
			t.Fatal("remove/readd reset identity or votes")
		}
	}
	// 后续投票变化后不能用旧快照覆盖。
	rev := h.Revisions[0].ID
	if _, err = s.VoteMusicTag(user, "song", song.ID, ids[0], "up"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EditMusicTagAssignments(user, "song", song.ID, MusicTagAssignmentEdit{BaseVersion: h.Version, RevisionID: &rev}); err == nil {
		t.Fatal("revert overwrote a later vote")
	}
}

func TestMusicTagConfidenceConservativeThreshold(t *testing.T) {
	for _, tc := range []struct {
		up, down int64
		want     string
	}{{0, 0, "pending"}, {0, 9, "pending"}, {2, 8, "disputed"}, {3, 9, "pending"}, {8, 2, "supported"}} {
		if got := musicTagConfidence(tc.up, tc.down); got != tc.want {
			t.Fatalf("%+v: got %s", tc, got)
		}
	}
}

func TestMusicTagWikiTreeConstraintsRollback(t *testing.T) {
	s, db, user := newMusicHTTPTestService(t)
	root, _, err := s.CreateMusicTag(user, "type", "Electronic", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := s.CreateMusicTag(user, "type", "House", &root.ID)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _, err := s.CreateMusicTag(user, "type", "Deep House", &child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CreateMusicTag(user, "type", "Too deep", &leaf.ID); err == nil {
		t.Fatal("fourth level accepted")
	}
	if _, _, err = s.CreateMusicTag(user, "mood", "Flat", &root.ID); err == nil {
		t.Fatal("mood has parent")
	}
	if _, err = s.EditMusicTag(user, root.ID, "move", MusicTagWikiInput{BaseVersion: 1, ParentID: &leaf.ID}, false); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err = s.EditMusicTag(user, root.ID, "merge", MusicTagWikiInput{BaseVersion: 1, TargetID: &leaf.ID, TargetVersion: 1}, false); err == nil {
		t.Fatal("merged into own descendant")
	}
	song := model.Song{Title: "Tagged", LifecycleStatus: model.MusicLifecycleActive, Status: "open"}
	if err = db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddMusicTag(user, "song", song.ID, "type", root.Name, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddMusicTag(user, "song", song.ID, "type", leaf.Name, &child.ID); err == nil {
		t.Fatal("redundant ancestor/leaf accepted")
	}
	var count int64
	db.Model(&model.MusicTagAssignment{}).Where("entity_id=?", song.ID).Count(&count)
	if count != 1 {
		t.Fatal("failed branch edit was not rolled back")
	}
	if _, err = s.EditMusicTag(user, root.ID, "rename", MusicTagWikiInput{BaseVersion: 1, Name: "New Electronic"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EditMusicTag(user, root.ID, "rename", MusicTagWikiInput{BaseVersion: 1, Name: "Stale"}, false); err == nil {
		t.Fatal("stale edit accepted")
	}
}

func TestMusicTagConcurrentLimit(t *testing.T) {
	s, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Concurrent", LifecycleStatus: model.MusicLifecycleActive, Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for i := 0; i < 11; i++ {
		tag, _, err := s.CreateMusicTag(user, "mood", fmt.Sprintf("Seed %d", i), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tag.ID)
	}
	if _, err := s.EditMusicTagAssignments(user, "song", song.ID, MusicTagAssignmentEdit{Add: ids}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"Last A", "Last B"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := s.AddMusicTag(user, "song", song.ID, "mood", name, nil)
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	var count int64
	db.Model(&model.MusicTagAssignment{}).Where("entity_id=?", song.ID).Count(&count)
	if success != 1 || count != 12 {
		t.Fatalf("concurrent limit bypassed: success=%d count=%d", success, count)
	}
}

func TestMusicTagDisputeSearchAndRecovery(t *testing.T) {
	s, db, user := newMusicHTTPTestService(t)
	tag, _, err := s.CreateMusicTag(user, "type", "Contested", nil)
	if err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Song", LifecycleStatus: model.MusicLifecycleActive, Status: "open"}
	if err = db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	a, err := s.AddMusicTag(user, "song", song.ID, "type", tag.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		voter := model.User{Username: fmt.Sprintf("voter%d", i), Email: fmt.Sprintf("voter%d@test.invalid", i), Password: "hash", Role: "user", IsActive: true}
		if err = db.Create(&voter).Error; err != nil {
			t.Fatal(err)
		}
		vote := "down"
		if i < 2 {
			vote = "up"
		}
		if err = db.Create(&model.MusicTagVote{AssignmentID: a.AssignmentID, UserID: voter.UUID, Vote: vote}).Error; err != nil {
			t.Fatal(err)
		}
	}
	counts, err := s.musicTagContentCounts([]uuid.UUID{tag.ID}, nil)
	if err != nil || counts[tag.ID].SongCount != 0 {
		t.Fatalf("disputed tag stayed searchable: %+v %v", counts, err)
	}
	rows, err := s.ListMusicTags(&user, "song", song.ID)
	if err != nil || len(rows) != 1 || rows[0].Confidence != "disputed" {
		t.Fatal("wiki hid contested relation")
	}
	if err = db.Where("assignment_id=? AND vote=?", a.AssignmentID, "down").Limit(1).Delete(&model.MusicTagVote{}).Error; err != nil {
		t.Fatal(err)
	}
	counts, err = s.musicTagContentCounts([]uuid.UUID{tag.ID}, nil)
	if err != nil || counts[tag.ID].SongCount != 1 {
		t.Fatalf("did not restore search: %+v %v", counts, err)
	}
}
