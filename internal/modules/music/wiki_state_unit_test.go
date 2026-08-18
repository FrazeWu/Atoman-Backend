package music

import (
	"testing"

	"atoman/internal/model"
)

func TestValidateMusicStateActionTransitions(t *testing.T) {
	tests := []struct {
		current string
		action  string
		valid   bool
	}{
		{current: model.MusicEditDevelopment, action: model.MusicStateActionClose, valid: true},
		{current: model.MusicEditLocked, action: model.MusicStateActionUnlock, valid: true},
		{current: model.MusicEditClosed, action: model.MusicStateActionReopen, valid: true},
		{current: model.MusicEditLocked, action: model.MusicStateActionClose, valid: false},
		{current: model.MusicEditClosed, action: model.MusicStateActionUnlock, valid: false},
		{current: model.MusicEditDevelopment, action: model.MusicStateActionReopen, valid: false},
	}
	for _, test := range tests {
		err := validateMusicStateAction(test.current, test.action)
		if (err == nil) != test.valid {
			t.Fatalf("transition %s/%s valid=%v, got %v", test.current, test.action, test.valid, err)
		}
	}
}

func TestValidateAlbumImportFilePartsRequiresExactRangeAndSizes(t *testing.T) {
	file := model.AlbumImportFile{Size: 25, PartSize: 10}
	valid := []AlbumImportMultipartPartDTO{
		{PartNumber: 1, ETag: "one", Size: 10},
		{PartNumber: 2, ETag: "two", Size: 10},
		{PartNumber: 3, ETag: "three", Size: 5},
	}
	if err := validateAlbumImportFileParts(file, valid); err != nil {
		t.Fatalf("valid parts rejected: %v", err)
	}
	invalidNumber := append([]AlbumImportMultipartPartDTO(nil), valid...)
	invalidNumber[2].PartNumber = 4
	if err := validateAlbumImportFileParts(file, invalidNumber); err == nil {
		t.Fatal("out-of-range part number was accepted")
	}
	invalidSize := append([]AlbumImportMultipartPartDTO(nil), valid...)
	invalidSize[2].Size = 10
	if err := validateAlbumImportFileParts(file, invalidSize); err == nil {
		t.Fatal("invalid final part size was accepted")
	}
}
