package music

import (
	"testing"

	"atoman/internal/model"
)

func TestParsePartialDate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantDate  string
		precision string
	}{
		{name: "day", input: "1990-05-20", wantDate: "1990-05-20", precision: "day"},
		{name: "month", input: "1990/05/--", wantDate: "1990-05-01", precision: "month"},
		{name: "year", input: "1990/--/--", wantDate: "1990-01-01", precision: "year"},
		{name: "legacy year", input: "1990", wantDate: "1990-01-01", precision: "year"},
		{name: "legacy zero year", input: "1990-00-00", wantDate: "1990-01-01", precision: "year"},
		{name: "legacy zero month", input: "1990-05-00", wantDate: "1990-05-01", precision: "month"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			date, precision, err := parsePartialDate(tt.input, "date")
			if err != nil {
				t.Fatalf("parse date: %v", err)
			}
			if got := date.Format("2006-01-02"); got != tt.wantDate || precision != tt.precision {
				t.Fatalf("got %s/%s, want %s/%s", got, precision, tt.wantDate, tt.precision)
			}
		})
	}
}

func TestParsePartialDateRejectsDayWithoutMonth(t *testing.T) {
	if _, _, err := parsePartialDate("1990/--/20", "date"); err == nil {
		t.Fatal("expected invalid partial date")
	}
}

func TestCreateArtistStoresPartialDatePrecision(t *testing.T) {
	service, _, user := newMusicTestService(t)
	artist, err := service.CreateArtist(user, CreateArtistRequest{
		Name:            "Partial Date Artist",
		LegalName:       "Partial Date Artist",
		ImageURL:        "/artist.jpg",
		Nationality:     "CN",
		BirthDate:       "1990/05/--",
		ActiveStartDate: "2010/--/--",
		Sources:         []Source{{Title: "artist source"}},
	})
	if err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if artist.BirthDate == nil || artist.BirthDatePrecision != "month" {
		t.Fatalf("unexpected birth date precision: %#v", artist)
	}
	if artist.ActiveStartDatePrecision != "year" {
		t.Fatalf("unexpected active start precision: %q", artist.ActiveStartDatePrecision)
	}
}

func TestReplaceArtistMembersRequiresExistingArtistID(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	group := model.Artist{Name: "Named Member Group", ArtistForm: "group", EntryStatus: "open"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := replaceArtistMembers(db, group.ID, []ArtistMemberPayload{{Name: "New Named Member", JoinDate: "2002-00-00"}}); err == nil {
		t.Fatal("expected member without artist_id to be rejected")
	}
}
