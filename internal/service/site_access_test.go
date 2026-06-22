package service

import "testing"

func TestSaveInputAcceptsMediaModule(t *testing.T) {
	input := DefaultSiteAccessMatrix().ToInput()
	enabled := true
	input.Modules["media"] = SiteAccessModuleInput{Enabled: &enabled}

	if err := validateSiteAccessInput(input); err != nil {
		t.Fatalf("validate site access input with media: %v", err)
	}
}
