package service

import (
	"errors"
	"testing"

	"atoman/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSaveInputAcceptsMediaModule(t *testing.T) {
	input := DefaultSiteAccessMatrix().ToInput()
	enabled := true
	input.Modules["media"] = SiteAccessModuleInput{Enabled: &enabled}

	if err := validateSiteAccessInput(input); err != nil {
		t.Fatalf("validate site access input with media: %v", err)
	}
}

func TestSaveInputRejectsStaleRevision(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/site-access.sqlite"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/site-access.sqlite"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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
	err = svc.SaveInput(input)
	if !errors.Is(err, ErrSiteAccessConflict) {
		t.Fatalf("save without revision error = %v, want ErrSiteAccessConflict", err)
	}
}
