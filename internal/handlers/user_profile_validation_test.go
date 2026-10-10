package handlers

import "testing"

func TestValidateUserProfileInput(t *testing.T) {
	valid := "https://example.com/avatar.png"
	if err := validateUserProfileInput(UserProfileInput{AvatarURL: &valid}); err != nil {
		t.Fatalf("valid profile input rejected: %v", err)
	}

	tooLong := make([]byte, maxProfileBioBytes+1)
	if err := validateUserProfileInput(UserProfileInput{Bio: stringPointer(string(tooLong))}); err == nil {
		t.Fatal("expected oversized bio to be rejected")
	}

	unsafe := "javascript:alert(1)"
	if err := validateUserProfileInput(UserProfileInput{Website: &unsafe}); err == nil {
		t.Fatal("expected unsafe website URL to be rejected")
	}
}

func stringPointer(value string) *string { return &value }
