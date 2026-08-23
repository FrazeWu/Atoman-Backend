package music

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExternalAlbumMetadataEnricherPrefersLocalLyrics(t *testing.T) {
	var lrcRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/get") {
			lrcRequests.Add(1)
			_, _ = w.Write([]byte(`{"plainLyrics":"remote"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), "", "", server.URL, "Atoman/test")
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Album",
		Tracks:     []AlbumImportMetadataTrack{{Title: "First Song", Artist: "Artist", Origin: "01 - First Song.flac", DurationSeconds: 200}},
		LocalLyrics: map[string]AlbumImportTrackLyricsPayload{
			"first song": {Content: "[00:01.00]local", Format: "lrc"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tracks) != 1 || result.Tracks[0].Lyrics == nil || result.Tracks[0].Lyrics.Content != "[00:01.00]local" || result.Tracks[0].LyricsSource != "local" {
		t.Fatalf("unexpected local lyrics result: %#v", result)
	}
	if lrcRequests.Load() != 0 {
		t.Fatalf("LRCLIB must not be called when local lyrics exist")
	}
}

func TestFindLocalLyricsUsesDiscAndTrackBeforeFileName(t *testing.T) {
	lyrics, ok := findLocalLyrics(map[string]AlbumImportTrackLyricsPayload{
		"01":             {Content: "ambiguous"},
		"disc:2:track:1": {Content: "disc two"},
	}, AlbumImportMetadataTrack{Title: "Song", Origin: "Disc 2/01.flac", DiscNumber: 2, TrackNumber: 1})
	if !ok || lyrics.Content != "disc two" {
		t.Fatalf("unexpected multidisc lyrics match: %#v, %v", lyrics, ok)
	}
}

func TestExternalAlbumMetadataEnricherFallsBackToLRCLIB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeMatchingMusicBrainzRelease(w, r) {
			return
		}
		if r.URL.Path != "/api/get" || r.URL.Query().Get("track_name") != "First Song" || r.URL.Query().Get("duration") != "200" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"duration":200,"plainLyrics":"plain","syncedLyrics":"[00:01.00]synced"}`))
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", server.URL, "Atoman/test")
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Album", Artist: "Artist",
		Tracks: []AlbumImportMetadataTrack{{Title: "First Song", Artist: "Artist", DurationSeconds: 200}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tracks[0].Lyrics == nil || result.Tracks[0].Lyrics.Content != "[00:01.00]synced" || result.Tracks[0].Lyrics.Format != "lrc" || result.Tracks[0].LyricsSource != "lrclib" {
		t.Fatalf("unexpected LRCLIB result: %#v", result.Tracks[0])
	}
}

func TestExternalAlbumMetadataEnricherUsesInputArtistWhenTrackArtistMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeMatchingMusicBrainzRelease(w, r) {
			return
		}
		if r.URL.Path != "/api/get" || r.URL.Query().Get("artist_name") != "Artist" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"plainLyrics":"remote"}`))
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", server.URL, "Atoman/test")
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Album", Artist: "Artist",
		Tracks: []AlbumImportMetadataTrack{{Title: "First Song", DurationSeconds: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tracks[0].Lyrics == nil || result.Tracks[0].Lyrics.Content != "remote" {
		t.Fatalf("expected lyrics from input artist fallback: %#v", result.Tracks[0])
	}
}

func TestExternalAlbumMetadataEnricherUsesMusicBrainzTrackTitleForLRCLIB(t *testing.T) {
	var requestedTitle string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeMatchingMusicBrainzRelease(w, r) {
			return
		}
		if r.URL.Path != "/api/get" {
			http.NotFound(w, r)
			return
		}
		requestedTitle = r.URL.Query().Get("track_name")
		if requestedTitle != "First Song" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"plainLyrics":"remote"}`))
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", server.URL, "Atoman/test")
	enricher.musicBrainzWait = 0
	enricher.lrcLibWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Album", Artist: "Artist",
		Tracks: []AlbumImportMetadataTrack{{Title: "01. Local Name", TrackNumber: 1, DurationSeconds: 200}},
	})
	if err != nil || result.Tracks[0].Lyrics == nil || result.Tracks[0].Lyrics.Content != "remote" {
		t.Fatalf("unexpected strict lyrics result: %#v, err=%v", result, err)
	}
	if requestedTitle != "First Song" {
		t.Fatalf("LRCLIB title = %q, want MusicBrainz title", requestedTitle)
	}
}

func TestExternalAlbumMetadataEnricherFallsBackToLRCLIBSearchAfterMusicBrainzMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeMatchingMusicBrainzRelease(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/get":
			http.NotFound(w, r)
		case "/api/search":
			_, _ = w.Write([]byte(`[{"trackName":"First Song","duration":46,"plainLyrics":"searched"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", server.URL, "Atoman/test")
	enricher.musicBrainzWait = 0
	enricher.lrcLibWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Album", Artist: "Artist",
		Tracks: []AlbumImportMetadataTrack{{Title: "First Song", DurationSeconds: 46}},
	})
	if err != nil || result.Tracks[0].Lyrics == nil || result.Tracks[0].Lyrics.Content != "searched" {
		t.Fatalf("unexpected search fallback result: %#v, err=%v", result, err)
	}
}

func TestNewLRCLyricsPayloadFallsBackToPlainWhenSyncedLyricsAreInvalid(t *testing.T) {
	lyrics, ok := newLRCLyricsPayload("plain lyrics", "[00:01.00]valid\ninvalid", 100, 100)
	if !ok || lyrics.Format != "plain" || lyrics.Content != "plain lyrics" {
		t.Fatalf("lyrics = %#v, ok = %v", lyrics, ok)
	}
}

func TestExternalAlbumMetadataEnricherAppliesMatchingMusicBrainzRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/2/release/":
			if !strings.Contains(r.Header.Get("User-Agent"), "Atoman") {
				t.Fatalf("missing MusicBrainz user agent")
			}
			_, _ = w.Write([]byte(`{"releases":[{"id":"release-id","title":"Canonical Album","track-count":2}]}`))
		case "/ws/2/release/release-id":
			_, _ = w.Write([]byte(`{"id":"release-id","title":"Canonical Album","date":"2020-02-03","release-group":{"primary-type":"Album"},"media":[{"position":1,"tracks":[{"position":1,"title":"First Song","length":200000},{"position":2,"title":"Second Song","length":210000}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "https://cover.example", "", "Atoman/test")
	enricher.musicBrainzWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Album", Artist: "Artist",
		Tracks: []AlbumImportMetadataTrack{
			{Title: "First Song", DiscNumber: 1, TrackNumber: 2, DurationSeconds: 200, AudioKey: "first"},
			{Title: "Second Song", DiscNumber: 1, TrackNumber: 1, DurationSeconds: 210, AudioKey: "second"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AlbumTitle != "Canonical Album" || result.ReleaseDate != "2020-02-03" || result.AlbumType != "album" || result.SourceURL != server.URL+"/release/release-id" || result.MusicBrainzReleaseID != "release-id" || result.CoverURL != "https://cover.example/release/release-id/front-500" {
		t.Fatalf("unexpected release metadata: %#v", result)
	}
	if len(result.Tracks) != 2 || result.Tracks[0].Title != "First Song" || result.Tracks[0].TrackNumber != 1 || result.Tracks[0].AudioKey != "first" {
		t.Fatalf("unexpected enriched tracks: %#v", result.Tracks)
	}
}

func TestExternalAlbumMetadataEnricherReordersShuffledTracks(t *testing.T) {
	server := newMusicBrainzEnricherTestServer(t, `{"id":"release-id","title":"Canonical Album","date":"2020-02-03","release-group":{"primary-type":"Album"},"media":[{"position":1,"tracks":[{"position":1,"title":"Intro","length":75000},{"position":2,"title":"What Would I Do","length":222000},{"position":3,"title":"God’s Gift","length":235000},{"position":4,"title":"Love Song","length":368000}]}]}`)
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", "", "Atoman/test")
	enricher.musicBrainzWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Album", Artist: "Artist",
		Tracks: []AlbumImportMetadataTrack{
			{Title: "Love Song", AudioKey: "love"},
			{Title: "Gods Gift", AudioKey: "gift"},
			{Title: "Intro", AudioKey: "intro"},
			{Title: "What Would I Do", AudioKey: "would"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"intro", "would", "gift", "love"}
	for index, want := range wantKeys {
		if result.Tracks[index].AudioKey != want || result.Tracks[index].TrackNumber != index+1 {
			t.Fatalf("track %d = %#v, want audio key %q", index, result.Tracks[index], want)
		}
	}
}

func TestExternalAlbumMetadataEnricherFindsReleaseThroughGroupAndArtistEntityName(t *testing.T) {
	const releaseID = "group-release-id"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/2/release-group/":
			_, _ = w.Write([]byte(`{"release-groups":[{"id":"group-id","title":"The College Dropout","artist-credit":[{"name":"Old Artist Name","artist":{"name":"Current Artist Name","aliases":[{"name":"Old Artist Name"}]}}]}]}`))
		case "/ws/2/release":
			_, _ = w.Write([]byte(`{"releases":[
				{"id":"wrong-release","title":"The College Dropout","status":"Official","date":"2004-02-09","release-group":{"primary-type":"Album"},"media":[{"tracks":[{"position":1,"title":"Intro","length":19000},{"position":2,"title":"All Falls Down","length":214000}]}]},
				{"id":"` + releaseID + `","title":"The College Dropout","status":"Official","date":"2004-02-10","release-group":{"primary-type":"Album"},"media":[{"tracks":[{"position":1,"title":"Intro","length":19000},{"position":2,"title":"All Falls Down","length":224000}]}]}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", "", "Atoman/test")
	enricher.musicBrainzWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "The College Dropout",
		Artist:     "Current Artist Name",
		Tracks: []AlbumImportMetadataTrack{
			{Title: "All Falls Down (feat. Syleena Johnson)", DurationSeconds: 224, AudioKey: "all-falls-down"},
			{Title: "Intro", DurationSeconds: 19, AudioKey: "intro"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceURL != server.URL+"/release/"+releaseID || result.ReleaseDate != "2004-02-10" {
		t.Fatalf("unexpected release: %#v", result)
	}
	if result.Tracks[0].AudioKey != "intro" || result.Tracks[1].AudioKey != "all-falls-down" {
		t.Fatalf("unexpected track mapping: %#v", result.Tracks)
	}
}

func TestMatchMusicBrainzTracksUsesUniqueDurationFallback(t *testing.T) {
	release := testMusicBrainzRelease(
		flattenedMusicBrainzTrack{Title: "Known One", DurationMS: 200000},
		flattenedMusicBrainzTrack{Title: "Known Two", DurationMS: 210000},
		flattenedMusicBrainzTrack{Title: "Known Three", DurationMS: 215000},
		flattenedMusicBrainzTrack{Title: "Canonical Four", DurationMS: 220000},
	)
	mapping, ok := matchMusicBrainzTracks(release, []AlbumImportMetadataTrack{
		{Title: "Known Two", DurationSeconds: 210},
		{Title: "Different Local Title", DurationSeconds: 220},
		{Title: "Known One", DurationSeconds: 200},
		{Title: "Known Three", DurationSeconds: 215},
	})
	if !ok || len(mapping) != 4 || mapping[0] != 2 || mapping[1] != 0 || mapping[2] != 3 || mapping[3] != 1 {
		t.Fatalf("unexpected mapping: %#v, %v", mapping, ok)
	}
}

func TestMatchMusicBrainzTracksIgnoresFeaturedArtistSuffix(t *testing.T) {
	release := testMusicBrainzRelease(flattenedMusicBrainzTrack{Title: "All Falls Down", DurationMS: 224000})
	mapping, ok := matchMusicBrainzTracks(release, []AlbumImportMetadataTrack{{
		Title: "All Falls Down (feat. Syleena Johnson)", DurationSeconds: 224,
	}})
	if !ok || len(mapping) != 1 || mapping[0] != 0 {
		t.Fatalf("unexpected mapping: %#v, %v", mapping, ok)
	}
}

func TestMatchMusicBrainzTracksIgnoresDuplicateFileSuffix(t *testing.T) {
	release := testMusicBrainzRelease(flattenedMusicBrainzTrack{Title: "Mercy"})
	mapping, ok := matchMusicBrainzTracks(release, []AlbumImportMetadataTrack{{Title: "Mercy.1"}})
	if !ok || len(mapping) != 1 || mapping[0] != 0 {
		t.Fatalf("mapping = %#v, %v", mapping, ok)
	}
}

func TestMatchMusicBrainzTracksMatchesTheCollegeDropout(t *testing.T) {
	remoteTitles := []string{"Intro", "We Don’t Care", "Graduation Day", "All Falls Down", "I’ll Fly Away", "Spaceship", "Jesus Walks", "Never Let Me Down", "Get ’Em High", "Workout Plan", "The New Workout Plan", "Slow Jamz", "Breathe In, Breathe Out", "School Spirit (skit 1)", "School Spirit", "School Spirit (skit 2)", "Lil Jimmy (skit)", "Two Words", "Through the Wire", "Family Business", "Last Call"}
	localTitles := []string{"Intro", "We Don't Care", "Graduation Day", "All Falls Down (feat. Syleena Johnson)", "I'll Fly Away", "Spaceship (feat. GLC & Consequence)", "Jesus Walks", "Never Let Me Down (feat. JAY-Z & J. Ivy)", "Get Em High (feat. Talib Kweli & Common)", "Workout Plan", "The New Workout Plan", "Slow Jamz (feat. Kanye West & Jamie Foxx)", "Breathe In Breathe Out (feat. Ludacris)", "School Spirit Skit 1", "School Spirit", "School Spirit Skit 2", "Lil Jimmy Skit", "Two Words (feat. Mos Def, Freeway & The Boys Choir of Harlem)", "Through The Wire", "Family Business", "Last Call"}
	durations := []int{19, 240, 82, 224, 70, 324, 194, 324, 289, 46, 323, 316, 247, 79, 182, 44, 54, 266, 221, 279, 761}
	remote := make([]flattenedMusicBrainzTrack, len(remoteTitles))
	uploaded := make([]AlbumImportMetadataTrack, len(localTitles))
	for index := range remoteTitles {
		remote[index] = flattenedMusicBrainzTrack{Title: remoteTitles[index], DurationMS: durations[index] * 1000}
		uploaded[index] = AlbumImportMetadataTrack{Title: localTitles[index], DiscNumber: 1, TrackNumber: index + 1, DurationSeconds: float64(durations[index])}
	}
	mapping, ok := matchMusicBrainzTracks(testMusicBrainzRelease(remote...), uploaded)
	if !ok || len(mapping) != len(uploaded) {
		t.Fatalf("The College Dropout did not match: %#v, %v", mapping, ok)
	}
}

func TestMatchMusicBrainzTracksByPositionStillRequiresTitleOrDuration(t *testing.T) {
	release := testMusicBrainzRelease(flattenedMusicBrainzTrack{Title: "Correct", DurationMS: 200000})
	_, ok := matchMusicBrainzTracks(release, []AlbumImportMetadataTrack{{Title: "Wrong", DiscNumber: 1, TrackNumber: 1, DurationSeconds: 300}})
	if ok {
		t.Fatal("track position alone must not accept a release")
	}
}

func TestMatchMusicBrainzTracksMatches808s(t *testing.T) {
	remote := testMusicBrainzRelease(
		flattenedMusicBrainzTrack{Title: "Say You Will"},
		flattenedMusicBrainzTrack{Title: "Welcome to Heartbreak"},
		flattenedMusicBrainzTrack{Title: "Heartless"},
		flattenedMusicBrainzTrack{Title: "Amazing"},
		flattenedMusicBrainzTrack{Title: "Love Lockdown"},
		flattenedMusicBrainzTrack{Title: "Paranoid"},
		flattenedMusicBrainzTrack{Title: "RoboCop"},
		flattenedMusicBrainzTrack{Title: "Street Lights"},
		flattenedMusicBrainzTrack{Title: "Bad News"},
		flattenedMusicBrainzTrack{Title: "See You in My Nightmares"},
		flattenedMusicBrainzTrack{Title: "Coldest Winter"},
		flattenedMusicBrainzTrack{Title: "Pinocchio Story (freestyle live from Singapore)"},
	)
	local := []string{"Street Lights", "Amazing", "Love Lockdown", "Heartless", "See You In My Nightmares", "Say You Will", "Paranoid", "Welcome To Heartbreak", "Bad News", "RoboCop", "Coldest Winter", "Pinocchio Story (Freestyle Live From Singapore)"}
	uploaded := make([]AlbumImportMetadataTrack, len(local))
	for index, title := range local {
		uploaded[index] = AlbumImportMetadataTrack{Title: title, DiscNumber: 1}
	}
	if _, ok := matchMusicBrainzTracks(remote, uploaded); !ok {
		t.Fatal("808s titles should match")
	}
}

func TestMatchMusicBrainzTracksLeavesAmbiguousDurationTracksUnmatched(t *testing.T) {
	release := testMusicBrainzRelease(
		flattenedMusicBrainzTrack{Title: "Known One", DurationMS: 200000},
		flattenedMusicBrainzTrack{Title: "Known Two", DurationMS: 210000},
		flattenedMusicBrainzTrack{Title: "Known Three", DurationMS: 212000},
		flattenedMusicBrainzTrack{Title: "Known Four", DurationMS: 214000},
		flattenedMusicBrainzTrack{Title: "Known Five", DurationMS: 216000},
		flattenedMusicBrainzTrack{Title: "Remote Six", DurationMS: 220000},
		flattenedMusicBrainzTrack{Title: "Remote Seven", DurationMS: 220000},
	)
	mapping, ok := matchMusicBrainzTracks(release, []AlbumImportMetadataTrack{
		{Title: "Known One", DurationSeconds: 200},
		{Title: "Known Two", DurationSeconds: 210},
		{Title: "Known Three", DurationSeconds: 212},
		{Title: "Known Four", DurationSeconds: 214},
		{Title: "Known Five", DurationSeconds: 216},
		{Title: "Local Six", DurationSeconds: 220},
		{Title: "Local Seven", DurationSeconds: 220},
	})
	if !ok || len(mapping) != 7 || mapping[5] != -1 || mapping[6] != -1 {
		t.Fatalf("ambiguous tracks should remain unmatched: %#v, %v", mapping, ok)
	}
}

func TestMatchMusicBrainzTracksAcceptsDurationMajority(t *testing.T) {
	release := testMusicBrainzRelease(
		flattenedMusicBrainzTrack{Title: "Remote One", DurationMS: 100000},
		flattenedMusicBrainzTrack{Title: "Remote Two", DurationMS: 120000},
		flattenedMusicBrainzTrack{Title: "Remote Three", DurationMS: 140000},
		flattenedMusicBrainzTrack{Title: "Same Four"},
	)
	mapping, ok := matchMusicBrainzTracks(release, []AlbumImportMetadataTrack{
		{Title: "Local Two", DurationSeconds: 120},
		{Title: "Same Four"},
		{Title: "Local One", DurationSeconds: 100},
		{Title: "Local Three", DurationSeconds: 140},
	})
	if !ok || len(mapping) != 4 || mapping[0] != 2 || mapping[1] != 0 || mapping[2] != 3 || mapping[3] != 1 {
		t.Fatalf("mapping = %#v, %v", mapping, ok)
	}
}

func TestBestMusicBrainzReleaseAcceptsBootlegButRejectsDifferentTitle(t *testing.T) {
	bootleg := testMusicBrainzRelease(flattenedMusicBrainzTrack{Title: "Song"})
	bootleg.Title, bootleg.Status = "ye", "Bootleg"
	bootleg.ReleaseGroup.Title = "ye"
	variant := testMusicBrainzRelease(flattenedMusicBrainzTrack{Title: "Song"})
	variant.Title, variant.Status = "ye (Slowed)", "Official"
	variant.ReleaseGroup.Title = "ye (Slowed)"
	matched, _, ok := bestMusicBrainzRelease([]musicBrainzRelease{bootleg, variant}, "ye", []AlbumImportMetadataTrack{{Title: "Song"}})
	if !ok || matched.Status != "Bootleg" {
		t.Fatalf("bootleg with matching album and tracks should be accepted: %#v, %v", matched, ok)
	}
}

func TestExternalAlbumMetadataEnricherAcceptsPreferredBootleg(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"bootleg","title":"Yandhi","status":"Bootleg","release-group":{"title":"Yandhi"},"media":[{"tracks":[{"position":1,"title":"Song","length":200000}]}]}`))
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", "", "Atoman/test")
	enricher.musicBrainzWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Yandhi", PreferredReleaseID: "bootleg",
		Tracks: []AlbumImportMetadataTrack{{Title: "Song", DurationSeconds: 200}},
	})
	if err != nil || result.SourceURL != server.URL+"/release/bootleg" {
		t.Fatalf("expected preferred bootleg match, result=%#v err=%v", result, err)
	}
}

func TestBestMusicBrainzReleaseAcceptsOtherNonOfficialStatuses(t *testing.T) {
	for _, status := range []string{"Promotion", "Withdrawn"} {
		release := testMusicBrainzRelease(flattenedMusicBrainzTrack{Title: "Song", DurationMS: 200000})
		release.Title, release.Status, release.ReleaseGroup.Title = "Album", status, "Album"
		matched, _, ok := bestMusicBrainzRelease([]musicBrainzRelease{release}, "Album", []AlbumImportMetadataTrack{{Title: "Song", DurationSeconds: 200}})
		if !ok || matched.Status != status {
			t.Fatalf("status %q should match: %#v, %v", status, matched, ok)
		}
	}
}

func TestMatchMusicBrainzTracksAllowsDifferentTrackCounts(t *testing.T) {
	release := testMusicBrainzRelease(
		flattenedMusicBrainzTrack{Title: "One", DurationMS: 100000},
		flattenedMusicBrainzTrack{Title: "Two", DurationMS: 120000},
		flattenedMusicBrainzTrack{Title: "Three", DurationMS: 140000},
	)
	mapping, ok := matchMusicBrainzTracks(release, []AlbumImportMetadataTrack{
		{Title: "Extra Local", DurationSeconds: 180},
		{Title: "Three", DurationSeconds: 140},
		{Title: "One", DurationSeconds: 100},
		{Title: "Two", DurationSeconds: 120},
	})
	if !ok || len(mapping) != 3 || mapping[0] != 2 || mapping[1] != 3 || mapping[2] != 1 {
		t.Fatalf("mapping = %#v, %v", mapping, ok)
	}
	tracks := applyMusicBrainzTracks([]AlbumImportDTOTrack{
		{Title: "Extra Local", AudioKey: "extra"},
		{Title: "Three", AudioKey: "three"},
		{Title: "One", AudioKey: "one"},
		{Title: "Two", AudioKey: "two"},
	}, release, mapping)
	if len(tracks) != 4 || tracks[0].AudioKey != "one" || tracks[1].AudioKey != "two" || tracks[2].AudioKey != "three" || tracks[3].AudioKey != "extra" {
		t.Fatalf("tracks = %#v", tracks)
	}
}

func TestMatchMusicBrainzTracksRejectsSmallPartialOverlap(t *testing.T) {
	release := testMusicBrainzRelease(
		flattenedMusicBrainzTrack{Title: "One"},
		flattenedMusicBrainzTrack{Title: "Remote Two"},
		flattenedMusicBrainzTrack{Title: "Remote Three"},
	)
	if _, ok := matchMusicBrainzTracks(release, []AlbumImportMetadataTrack{{Title: "One"}, {Title: "Local Two"}, {Title: "Local Three"}, {Title: "Extra"}}); ok {
		t.Fatal("one matching title must not accept the album")
	}
}

func TestBestMusicBrainzReleasePrefersMoreMatchedTracks(t *testing.T) {
	oneTrack := testMusicBrainzRelease(flattenedMusicBrainzTrack{Title: "One", DurationMS: 100000})
	oneTrack.ID, oneTrack.Status = "one", "Official"
	oneTrack.ReleaseGroup.Title = "Album"
	full := testMusicBrainzRelease(
		flattenedMusicBrainzTrack{Title: "One", DurationMS: 100000},
		flattenedMusicBrainzTrack{Title: "Two", DurationMS: 120000},
		flattenedMusicBrainzTrack{Title: "Three", DurationMS: 140000},
	)
	full.ID, full.Status = "full", "Official"
	full.ReleaseGroup.Title = "Album"
	matched, _, ok := bestMusicBrainzRelease([]musicBrainzRelease{oneTrack, full}, "Album", []AlbumImportMetadataTrack{
		{Title: "One", DurationSeconds: 100}, {Title: "Two", DurationSeconds: 120}, {Title: "Three", DurationSeconds: 140},
	})
	if !ok || matched.ID != "full" {
		t.Fatalf("matched = %#v, %v", matched, ok)
	}
}

func TestExternalAlbumMetadataEnricherSearchesGroupWithEachArtist(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/2/release-group/":
			query := r.URL.Query().Get("query")
			queries = append(queries, query)
			if strings.Contains(query, `artist:"Pusha T"`) {
				_, _ = w.Write([]byte(`{"release-groups":[{"id":"daytona","title":"DAYTONA","artist-credit":[{"artist":{"name":"Pusha T"}}]}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"release-groups":[]}`))
		case "/ws/2/release":
			_, _ = w.Write([]byte(`{"releases":[{"id":"release-id","title":"DAYTONA","status":"Official","release-group":{"title":"DAYTONA"},"artist-credit":[{"artist":{"name":"Pusha T"}}],"media":[{"tracks":[{"position":1,"title":"If You Know You Know","length":202000}]}]}]}`))
		case "/ws/2/release/":
			_, _ = w.Write([]byte(`{"releases":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", "", "Atoman/test")
	enricher.musicBrainzWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "DAYTONA", Artist: "Ye", Artists: []string{"Ye", "Pusha T"},
		Tracks: []AlbumImportMetadataTrack{{Title: "If You Know You Know", DurationSeconds: 202}},
	})
	if err != nil || result.SourceURL == "" {
		t.Fatalf("second artist did not match: result=%#v err=%v queries=%#v", result, err, queries)
	}
	if len(queries) < 2 || !strings.Contains(queries[0], `artist:"Ye"`) || !strings.Contains(queries[1], `artist:"Pusha T"`) {
		t.Fatalf("unexpected group queries: %#v", queries)
	}
}

func TestExternalAlbumMetadataEnricherRetriesWithExactArtistID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/2/release-group/":
			if strings.Contains(r.URL.Query().Get("query"), "arid:ye-id") {
				_, _ = w.Write([]byte(`{"release-groups":[{"id":"ye-group","title":"ye","artist-credit":[{"artist":{"name":"Kanye West"}}]}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"release-groups":[]}`))
		case "/ws/2/release/":
			_, _ = w.Write([]byte(`{"releases":[]}`))
		case "/ws/2/artist/":
			_, _ = w.Write([]byte(`{"artists":[{"id":"ye-id","name":"Ye","score":100,"aliases":[{"name":"Kanye West"}]}]}`))
		case "/ws/2/release":
			_, _ = w.Write([]byte(`{"releases":[{"id":"ye-release","title":"ye","status":"Official","release-group":{"title":"ye"},"media":[{"tracks":[{"position":1,"title":"Ghost Town","length":271000}]}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", "", "Atoman/test")
	enricher.musicBrainzWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Ye", Artist: "Ye", Tracks: []AlbumImportMetadataTrack{{Title: "Ghost Town", DurationSeconds: 271}},
	})
	if err != nil || result.SourceURL != server.URL+"/release/ye-release" {
		t.Fatalf("artist ID retry failed: result=%#v err=%v", result, err)
	}
}

func TestMusicBrainzAlbumTitlesMatchDeluxeSuffix(t *testing.T) {
	if !musicBrainzAlbumTitlesMatch("Watch the Throne", "Watch The Throne[Deluxe]") {
		t.Fatal("deluxe suffix should not prevent matching")
	}
	if musicBrainzAlbumTitlesMatch("ye (Slowed)", "ye") {
		t.Fatal("unknown edition suffix must not be ignored")
	}
}

func TestBestMusicBrainzReleaseAllowsExactReleaseTitleWhenGroupTitleIsLonger(t *testing.T) {
	release := testMusicBrainzRelease(flattenedMusicBrainzTrack{Title: "Mercy.1"})
	release.Title = "Cruel Summer"
	release.Status = "Official"
	release.ReleaseGroup.Title = "Kanye West Presents Good Music: Cruel Summer"
	if _, _, ok := bestMusicBrainzRelease([]musicBrainzRelease{release}, "Cruel Summer", []AlbumImportMetadataTrack{{Title: "Mercy.1"}}); !ok {
		t.Fatal("exact release title should allow a longer release-group title")
	}
}

func TestMissingMusicBrainzArtists(t *testing.T) {
	credits := []musicBrainzArtistCredit{{Artist: musicBrainzArtist{Name: "Ye"}}, {Artist: musicBrainzArtist{Name: "Jay-Z"}}}
	missing := missingMusicBrainzArtists(credits, []string{"Ye"})
	if len(missing) != 1 || missing[0] != "Jay-Z" {
		t.Fatalf("missing = %#v", missing)
	}
}

func TestMissingMusicBrainzArtistsUsesAllLocalArtists(t *testing.T) {
	credits := []musicBrainzArtistCredit{{Artist: musicBrainzArtist{Name: "Ye"}}, {Artist: musicBrainzArtist{Name: "Pusha T"}}}
	if missing := missingMusicBrainzArtists(credits, []string{"Ye", "Pusha T"}); len(missing) != 0 {
		t.Fatalf("missing = %#v", missing)
	}
}

func TestExternalAlbumMetadataEnricherFallsBackToReleaseOnlySearch(t *testing.T) {
	var searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/2/release/":
			if searches.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"releases":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"releases":[{"id":"release-id","title":"Album","track-count":2}]}`))
		case "/ws/2/release/release-id":
			_, _ = w.Write([]byte(`{"id":"release-id","title":"Album","media":[{"tracks":[{"position":1,"title":"First"},{"position":2,"title":"Second"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", "", "Atoman/test")
	enricher.musicBrainzWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Album", Artist: "Artist Alias", Tracks: []AlbumImportMetadataTrack{{Title: "First"}, {Title: "Second"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if searches.Load() != 2 || result.SourceURL != server.URL+"/release/release-id" {
		t.Fatalf("unexpected fallback result: searches=%d result=%#v", searches.Load(), result)
	}
}

func TestExternalAlbumMetadataEnricherDoesNotDropArtistForSingle(t *testing.T) {
	var releaseSearches atomic.Int32
	var releaseSearchHadArtist atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/2/release/" {
			releaseSearches.Add(1)
			releaseSearchHadArtist.Store(strings.Contains(r.URL.Query().Get("query"), `artist:"Ye"`))
			_, _ = w.Write([]byte(`{"releases":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"release-groups":[]}`))
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", "", "Atoman/test")
	enricher.musicBrainzWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Love Love Love", Artist: "Ye", Tracks: []AlbumImportMetadataTrack{{Title: "Love Love Love"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if releaseSearches.Load() != 1 || !releaseSearchHadArtist.Load() || result.SourceURL != "" {
		t.Fatalf("single search dropped artist: searches=%d artist=%v result=%#v", releaseSearches.Load(), releaseSearchHadArtist.Load(), result)
	}
}

func TestExternalAlbumMetadataEnricherSkipsLRCLIBWhenMusicBrainzDoesNotMatch(t *testing.T) {
	var lyricsRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			lyricsRequests.Add(1)
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	enricher := NewExternalAlbumMetadataEnricher(server.Client(), server.URL, "", server.URL, "Atoman/test")
	enricher.musicBrainzWait = 0
	enricher.lrcLibWait = 0
	result, err := enricher.Enrich(context.Background(), AlbumImportMetadataInput{
		AlbumTitle: "Unknown Album", Artist: "Unknown Artist",
		Tracks: []AlbumImportMetadataTrack{{Title: "Unknown Song", DurationSeconds: 180}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceURL != "" || result.Tracks[0].Lyrics != nil || result.MetadataError == "" {
		t.Fatalf("unexpected unmatched result: %#v", result)
	}
	if lyricsRequests.Load() != 0 {
		t.Fatalf("LRCLIB must not be called when MusicBrainz does not match")
	}
}

func writeMatchingMusicBrainzRelease(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/ws/2/release/":
		_, _ = w.Write([]byte(`{"releases":[{"id":"release-id","title":"Album"}]}`))
		return true
	case "/ws/2/release/release-id":
		_, _ = w.Write([]byte(`{"id":"release-id","title":"Album","release-group":{"title":"Album","primary-type":"Album"},"media":[{"position":1,"tracks":[{"position":1,"title":"First Song","length":200000}]}]}`))
		return true
	default:
		return false
	}
}

func newMusicBrainzEnricherTestServer(t *testing.T, detail string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/2/release/":
			_, _ = w.Write([]byte(`{"releases":[{"id":"release-id","title":"Canonical Album","track-count":4}]}`))
		case "/ws/2/release/release-id":
			_, _ = w.Write([]byte(detail))
		default:
			http.NotFound(w, r)
		}
	}))
}

func testMusicBrainzRelease(tracks ...flattenedMusicBrainzTrack) musicBrainzRelease {
	var release musicBrainzRelease
	release.Media = make([]struct {
		Position int `json:"position"`
		Tracks   []struct {
			Position  int    `json:"position"`
			Title     string `json:"title"`
			Length    int    `json:"length"`
			Recording struct {
				Title  string `json:"title"`
				Length int    `json:"length"`
			} `json:"recording"`
		} `json:"tracks"`
	}, 1)
	release.Media[0].Position = 1
	for index, track := range tracks {
		var item struct {
			Position  int    `json:"position"`
			Title     string `json:"title"`
			Length    int    `json:"length"`
			Recording struct {
				Title  string `json:"title"`
				Length int    `json:"length"`
			} `json:"recording"`
		}
		item.Position = index + 1
		item.Title = track.Title
		item.Length = track.DurationMS
		release.Media[0].Tracks = append(release.Media[0].Tracks, item)
	}
	return release
}

func TestMatchMusicBrainzTracksMatchesLateRegistrationByDuration(t *testing.T) {
	remoteTitles := []string{
		"Wake Up Mr. West", "Heard ’Em Say", "Touch the Sky", "Gold Digger", "Skit #1",
		"Drive Slow", "My Way Home", "Crack Music", "Roses", "Bring Me Down", "Addiction",
		"Skit #2", "Diamonds From Sierra Leone (remix)", "We Major", "Skit #3", "Hey Mama",
		"Celebration", "Skit #4", "Gone", "Diamonds From Sierra Leone", "Late",
	}
	remoteDurations := []int{41, 204, 237, 208, 34, 273, 104, 271, 246, 199, 267, 31, 233, 449, 24, 305, 198, 79, 363, 239, 230}
	remote := make([]flattenedMusicBrainzTrack, len(remoteTitles))
	for index := range remote {
		remote[index] = flattenedMusicBrainzTrack{Title: remoteTitles[index], Position: index + 1, DurationMS: remoteDurations[index] * 1000}
	}
	uploadedTitles := []string{
		"Addiction", "Celebration", "Crack Music", "Drive Slow", "Gold Digger", "Gone", "Hey Mama", "Late",
		"My Way Home", "Roses", "Skit #1 (Kanye West-Late Registration)", "Skit #2 (Kanye West-Late Registration)",
		"Skit #3 (Kanye West-Late Registration)", "Skit #4 (Kanye West-Late Registration)", "Touch The Sky", "Wake Up Mr. West",
		"We Major", "Diamonds From Sierra Leone (bonus track)", "Heard 'Em Say", "Bring Me Down", "Diamonds From Sierra Leone",
	}
	uploadedDurations := []int{267, 198, 271, 272, 208, 333, 305, 230, 103, 246, 34, 31, 24, 79, 237, 41, 448, 238, 204, 199, 233}
	uploaded := make([]AlbumImportMetadataTrack, len(uploadedTitles))
	for index := range uploaded {
		uploaded[index] = AlbumImportMetadataTrack{Title: uploadedTitles[index], DurationSeconds: float64(uploadedDurations[index]), AudioKey: fmt.Sprintf("song-%d", index+1)}
	}
	signalMapping, signalOK := matchMusicBrainzTracksBySignals(remote, uploaded)
	if !signalOK {
		t.Fatalf("signal matching failed, mapping=%v", signalMapping)
	}
	mapping, ok := matchMusicBrainzTracks(testMusicBrainzRelease(remote...), uploaded)
	if !ok || len(mapping) != len(remote) {
		t.Fatalf("Late Registration should match all tracks, mapping=%v, ok=%v", mapping, ok)
	}
	for remoteIndex, uploadedIndex := range mapping {
		if uploadedIndex < 0 || uploadedIndex >= len(uploaded) {
			t.Fatalf("remote track %d has invalid upload mapping %d; mapping=%v", remoteIndex+1, uploadedIndex, mapping)
		}
		if uploaded[uploadedIndex].Title != uploadedTitles[uploadedIndex] {
			t.Fatalf("remote track %d mapped to unexpected upload %d", remoteIndex+1, uploadedIndex+1)
		}
	}
}
