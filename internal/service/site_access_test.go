package service

import (
	"encoding/json"
	"errors"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestSaveInputAcceptsMediaModule(t *testing.T) {
	input := DefaultSiteAccessMatrix().ToInput()
	enabled := true
	input.Modules["media"] = SiteAccessModuleInput{Enabled: &enabled}

	if err := validateSiteAccessInput(input); err != nil {
		t.Fatalf("validate site access input with media: %v", err)
	}
}

func TestDefaultSiteAccessEnablesLaunchedBooks(t *testing.T) {
	books := DefaultSiteAccessMatrix().Modules["books"]
	if books.Enabled == nil || !*books.Enabled {
		t.Fatal("books should be enabled by default after launch")
	}
	for _, feature := range []string{"books.submit", "books.review", "books.publish_asset"} {
		if !books.Features[feature] {
			t.Fatalf("launched books feature %q should be enabled", feature)
		}
	}
}

func TestSaveLegacyPayloadAllowsEnablingBooks(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.SiteSetting{}); err != nil {
		t.Fatalf("migrate site settings: %v", err)
	}

	enabled := true
	payload, err := json.Marshal(legacySiteAccessMatrix{
		Modules: map[string]legacySiteAccessModule{
			"books": {Enabled: &enabled},
		},
	})
	if err != nil {
		t.Fatalf("marshal legacy payload: %v", err)
	}

	svc := NewSiteAccessService(db)
	if err := svc.SaveLegacyPayload(payload); err != nil {
		t.Fatalf("save legacy payload: %v", err)
	}

	var setting model.SiteSetting
	if err := db.First(&setting, "key = ?", SiteAccessSettingKey).Error; err != nil {
		t.Fatalf("load stored site access: %v", err)
	}
	var stored SiteAccessMatrix
	if err := json.Unmarshal([]byte(setting.Value), &stored); err != nil {
		t.Fatalf("decode stored site access: %v", err)
	}
	if stored.Modules["books"].Enabled == nil || !*stored.Modules["books"].Enabled {
		t.Fatal("books should be enabled after legacy save")
	}
}

func TestSaveInputAllowsEnablingBooks(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.SiteSetting{}); err != nil {
		t.Fatalf("migrate site settings: %v", err)
	}

	svc := NewSiteAccessService(db)
	if err := svc.SaveInput(DefaultSiteAccessMatrix().ToInput()); err != nil {
		t.Fatalf("seed site access: %v", err)
	}

	matrix, err := svc.Load()
	if err != nil {
		t.Fatalf("load site access: %v", err)
	}
	enabled := true
	input := matrix.ToInput()
	input.Modules["books"] = SiteAccessModuleInput{Enabled: &enabled}
	if err := svc.SaveInput(input); err != nil {
		t.Fatalf("enable books: %v", err)
	}

	updated, err := svc.Load()
	if err != nil {
		t.Fatalf("reload site access: %v", err)
	}
	if updated.Modules["books"].Enabled == nil || !*updated.Modules["books"].Enabled {
		t.Fatal("books should remain enabled after save")
	}
}

func TestSaveInputRejectsStaleRevision(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.SiteSetting{}); err != nil {
		t.Fatalf("migrate site settings: %v", err)
	}

	svc := NewSiteAccessService(db)
	if err := svc.SaveInput(DefaultSiteAccessMatrix().ToInput()); err != nil {
		t.Fatalf("seed site access: %v", err)
	}

	adminA, err := svc.Load()
	if err != nil {
		t.Fatalf("load admin a: %v", err)
	}
	adminB, err := svc.Load()
	if err != nil {
		t.Fatalf("load admin b: %v", err)
	}
	if adminA.Revision == 0 || adminB.Revision == 0 {
		t.Fatal("load should include revision token")
	}

	inputA := adminA.ToInput()
	feedEnabled := false
	inputA.Modules["feed"] = SiteAccessModuleInput{Enabled: &feedEnabled}
	if err := svc.SaveInput(inputA); err != nil {
		t.Fatalf("save admin a: %v", err)
	}

	inputB := adminB.ToInput()
	blogEnabled := false
	inputB.Modules["blog"] = SiteAccessModuleInput{Enabled: &blogEnabled}
	err = svc.SaveInput(inputB)
	if !errors.Is(err, ErrSiteAccessConflict) {
		t.Fatalf("save admin b error = %v, want ErrSiteAccessConflict", err)
	}
}

func TestSaveInputRejectsMissingRevisionForExistingSetting(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.SiteSetting{}); err != nil {
		t.Fatalf("migrate site settings: %v", err)
	}

	svc := NewSiteAccessService(db)
	if err := svc.SaveInput(DefaultSiteAccessMatrix().ToInput()); err != nil {
		t.Fatalf("seed site access: %v", err)
	}

	input := DefaultSiteAccessMatrix().ToInput()
	feedEnabled := false
	input.Modules["feed"] = SiteAccessModuleInput{Enabled: &feedEnabled}
	err := svc.SaveInput(input)
	if !errors.Is(err, ErrSiteAccessConflict) {
		t.Fatalf("save without revision error = %v, want ErrSiteAccessConflict", err)
	}
}

func testIntPtr(v int) *int { return &v }

func TestDebateSettingsDefaultsPersistenceBoundsAndFallback(t *testing.T) {
	dbNoTable := testdb.Open(t)
	fallback := NewSiteAccessService(dbNoTable).LoadDebateSettings()
	if fallback.ConclusionMinVotes != DefaultDebateConclusionMinVotes ||
		fallback.ConclusionThresholdPercent != DefaultDebateConclusionThresholdPct ||
		fallback.ChallengeMinVotes != DefaultDebateChallengeMinVotes {
		t.Fatalf("unexpected fallback debate settings without table: %+v", fallback)
	}

	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.SiteSetting{}); err != nil {
		t.Fatalf("migrate site settings: %v", err)
	}
	svc := NewSiteAccessService(db)
	matrix, err := svc.Load()
	if err != nil {
		t.Fatalf("load site access: %v", err)
	}
	if matrix.Settings.Debate.ConclusionMinVotes != 5 ||
		matrix.Settings.Debate.ConclusionThresholdPercent != 75 ||
		matrix.Settings.Debate.ChallengeMinVotes != 5 {
		t.Fatalf("unexpected default debate settings: %+v", matrix.Settings.Debate)
	}

	input := matrix.ToInput()
	input.Settings.Debate = &SiteAccessDebateSettingsInput{
		ConclusionMinVotes:         testIntPtr(8),
		ConclusionThresholdPercent: testIntPtr(80),
		ChallengeMinVotes:          testIntPtr(6),
	}
	if err := svc.SaveInput(input); err != nil {
		t.Fatalf("save valid debate settings: %v", err)
	}
	loaded := svc.LoadDebateSettings()
	if loaded.ConclusionMinVotes != 8 || loaded.ConclusionThresholdPercent != 80 || loaded.ChallengeMinVotes != 6 {
		t.Fatalf("unexpected persisted debate settings: %+v", loaded)
	}

	reloaded, err := svc.Load()
	if err != nil {
		t.Fatalf("reload matrix: %v", err)
	}
	invalidCases := []SiteAccessDebateSettingsInput{
		{ConclusionMinVotes: testIntPtr(2)},
		{ConclusionMinVotes: testIntPtr(101)},
		{ConclusionThresholdPercent: testIntPtr(50)},
		{ConclusionThresholdPercent: testIntPtr(96)},
		{ChallengeMinVotes: testIntPtr(2)},
		{ChallengeMinVotes: testIntPtr(101)},
	}
	for _, bad := range invalidCases {
		badInput := reloaded.ToInput()
		badCopy := bad
		badInput.Settings.Debate = &badCopy
		if err := svc.SaveInput(badInput); err == nil {
			t.Fatalf("expected validation error for %+v", bad)
		}
	}
}
