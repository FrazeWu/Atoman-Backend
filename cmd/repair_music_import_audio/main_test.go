package main

import (
	"testing"

	"atoman/internal/model"
)

func TestCommitRequestReadsStoredPayload(t *testing.T) {
	session := model.AlbumImportSession{PayloadJSON: `{"commit_request":{"album":{"title":"菊花夜行军","tracks":[{"title":"两代人","track_number":1}]}}}`}
	input, err := commitRequest(session)
	if err != nil {
		t.Fatal(err)
	}
	if input.Album.Title != "菊花夜行军" || len(input.Album.Tracks) != 1 || input.Album.Tracks[0].Title != "两代人" {
		t.Fatalf("unexpected commit request: %#v", input)
	}
}

func TestParseIDs(t *testing.T) {
	ids, err := parseIDs("01a10a74-2950-71ce-9844-b3a355d25f77, 01a10a2c-ff6a-7daa-85c6-80c568eb1fea")
	if err != nil || len(ids) != 2 {
		t.Fatalf("parse IDs = %#v, error = %v", ids, err)
	}
}
