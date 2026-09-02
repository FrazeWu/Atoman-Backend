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

func TestDefaultSiteAccessDisablesBooksUntilLaunch(t *testing.T) {
	books := DefaultSiteAccessMatrix().Modules["books"]
	if books.Enabled == nil || *books.Enabled {
		t.Fatal("books should be disabled by default")
	}
}

func TestSaveLegacyPayloadKeepsBooksDisabled(t *testing.T) {
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
	if stored.Modules["books"].Enabled == nil || *stored.Modules["books"].Enabled {
		t.Fatal("books should remain disabled after legacy save")
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
