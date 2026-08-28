package books

import (
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestBookAuthorizationSeparatesPrivateReadersAndReviewers(t *testing.T) {
	ownerID := uuid.New()
	otherID := uuid.New()

	if !CanReadPrivateResource(BookViewer{UserID: ownerID}, ownerID) {
		t.Fatal("owner should read private resources")
	}
	if CanReadPrivateResource(BookViewer{UserID: otherID}, ownerID) {
		t.Fatal("other users must not read private resources")
	}
	if CanReadPrivateResource(BookViewer{}, ownerID) {
		t.Fatal("anonymous viewers must not read private resources")
	}

	if !CanReviewSubmission(BookViewer{UserID: uuid.New(), Role: "moderator"}, ownerID) {
		t.Fatal("moderators should review another user's submission")
	}
	if !CanReviewSubmission(BookViewer{UserID: uuid.New(), Role: "admin"}, ownerID) {
		t.Fatal("admins should review another user's submission")
	}
	if CanReviewSubmission(BookViewer{UserID: ownerID, Role: "admin"}, ownerID) {
		t.Fatal("submitters must not review their own submission")
	}
	if CanReviewSubmission(BookViewer{UserID: uuid.New(), Role: "user"}, ownerID) {
		t.Fatal("ordinary users must not review submissions")
	}
}

func TestBookAuthorizationRequiresPublishedPublicStates(t *testing.T) {
	if !IsPublicBookLifecycle(model.BookLifecycleStatusActive) {
		t.Fatal("active works should be public")
	}
	for _, status := range []string{model.BookLifecycleStatusDraft, model.BookLifecycleStatusRetired, model.BookLifecycleStatusMerged} {
		if IsPublicBookLifecycle(status) {
			t.Fatalf("lifecycle %q should not be publicly active", status)
		}
	}
	if !IsPublicBookAssetStatus(model.BookPublicationStatusPublished) {
		t.Fatal("published assets should be public")
	}
	for _, status := range []string{model.BookPublicationStatusPendingReview, model.BookPublicationStatusRejected, model.BookPublicationStatusQuarantined, model.BookPublicationStatusRemoved} {
		if IsPublicBookAssetStatus(status) {
			t.Fatalf("publication status %q should not be public", status)
		}
	}
}
