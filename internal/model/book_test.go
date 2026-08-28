package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBookModelsKeepPrivateFieldsOutOfJSON(t *testing.T) {
	ownerID := uuid.New()
	asset := UserBookAsset{
		Base:             Base{ID: uuid.New()},
		UserID:           ownerID,
		OriginalFilename: "private.epub",
		ObjectKey:        "users/owner/private.epub",
		DerivedObjectKey: "users/owner/private/reader",
		SHA256:           "private-hash",
		ProcessingStatus: BookAssetStatusPrivateAvailable,
	}
	state := UserBookReadingState{
		Base:           Base{ID: uuid.New()},
		UserID:         ownerID,
		AssetID:        asset.ID,
		EPUBCFI:        "epubcfi(/6/2[chapter]!/4/2/1:0)",
		PrivateNotes:   "不要公开",
		ReadingPercent: 0.4,
	}

	assetJSON, err := json.Marshal(asset)
	if err != nil {
		t.Fatal(err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{string(assetJSON), string(stateJSON)} {
		for _, privateField := range []string{"object_key", "derived_object_key", "sha256", "epub_cfi", "private_notes"} {
			if containsJSONKey(encoded, privateField) {
				t.Fatalf("private field %q leaked into JSON: %s", privateField, encoded)
			}
		}
	}
}

func containsJSONKey(encoded, key string) bool {
	return strings.Contains(encoded, `"`+key+`"`)
}
