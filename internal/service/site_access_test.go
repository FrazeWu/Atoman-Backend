package service

import "testing"

func TestSaveInputAcceptsKanboModule(t *testing.T) {
	input := DefaultSiteAccessMatrix().ToInput()
	enabled := true
	input.Modules["kanbo"] = SiteAccessModuleInput{Enabled: &enabled}

	if err := validateSiteAccessInput(input); err != nil {
		t.Fatalf("validate site access input with kanbo: %v", err)
	}
}
