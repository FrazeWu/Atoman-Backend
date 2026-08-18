package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type AlbumImportMetadataTrack struct {
	Title           string
	Artist          string
	Album           string
	DiscNumber      int
	TrackNumber     int
	DurationSeconds float64
	Origin          string
	AudioKey        string
	AudioURL        string
}

type AlbumImportMetadataInput struct {
	AlbumTitle         string
	Artist             string
	Artists            []string
	PreferredReleaseID string
	Tracks             []AlbumImportMetadataTrack
	LocalLyrics        map[string]AlbumImportTrackLyricsPayload
}

type AlbumImportMetadataResult struct {
	AlbumTitle     string
	ReleaseDate    string
	AlbumType      string
	CoverURL       string
	SourceURL      string
	MissingArtists []string
	MetadataError  string
	Tracks         []AlbumImportDTOTrack
}

type AlbumImportMetadataEnricher interface {
	Enrich(context.Context, AlbumImportMetadataInput) (AlbumImportMetadataResult, error)
}

type ExternalAlbumMetadataEnricher struct {
	httpClient      *http.Client
	musicBrainzBase string
	coverArtBase    string
	lrcLibBase      string
	userAgent       string
	requestMu       sync.Mutex
	lastMBRequest   time.Time
	musicBrainzWait time.Duration
	lrcLibMu        sync.Mutex
	lastLRCRequest  time.Time
	lrcLibWait      time.Duration
}

func NewExternalAlbumMetadataEnricher(httpClient *http.Client, musicBrainzBase, coverArtBase, lrcLibBase, userAgent string) *ExternalAlbumMetadataEnricher {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &ExternalAlbumMetadataEnricher{
		httpClient: httpClient, musicBrainzBase: strings.TrimRight(musicBrainzBase, "/"),
		coverArtBase: strings.TrimRight(coverArtBase, "/"), lrcLibBase: strings.TrimRight(lrcLibBase, "/"),
		userAgent: strings.TrimSpace(userAgent), musicBrainzWait: time.Second, lrcLibWait: time.Second,
	}
}

type musicBrainzRelease struct {
	ID           string                    `json:"id"`
	Title        string                    `json:"title"`
	Date         string                    `json:"date"`
	Status       string                    `json:"status"`
	TrackCount   int                       `json:"track-count"`
	ArtistCredit []musicBrainzArtistCredit `json:"artist-credit"`
	ReleaseGroup struct {
		ID          string `json:"id"`
		PrimaryType string `json:"primary-type"`
		Title       string `json:"title"`
	} `json:"release-group"`
	Media []struct {
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
	} `json:"media"`
}

type musicBrainzArtist struct {
	Name    string `json:"name"`
	Aliases []struct {
		Name string `json:"name"`
	} `json:"aliases"`
}

type musicBrainzArtistSearchResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Score   int    `json:"score"`
	Aliases []struct {
		Name string `json:"name"`
	} `json:"aliases"`
}

type musicBrainzArtistCredit struct {
	Name   string            `json:"name"`
	Artist musicBrainzArtist `json:"artist"`
}

type musicBrainzReleaseGroup struct {
	ID           string                    `json:"id"`
	Title        string                    `json:"title"`
	ArtistCredit []musicBrainzArtistCredit `json:"artist-credit"`
}

func (e *ExternalAlbumMetadataEnricher) Enrich(ctx context.Context, input AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
	result := AlbumImportMetadataResult{AlbumTitle: input.AlbumTitle, Tracks: baseMetadataTracks(input.Tracks)}
	if e == nil {
		return result, nil
	}

	release, trackMapping, err := e.findRelease(ctx, input)
	if err != nil && input.PreferredReleaseID != "" {
		return result, err
	}
	releaseMatched := err == nil && release.ID != ""
	if err != nil {
		result.MetadataError = err.Error()
	}
	if releaseMatched {
		result.AlbumTitle = release.Title
		result.ReleaseDate = release.Date
		result.AlbumType = normalizeMusicBrainzAlbumType(release.ReleaseGroup.PrimaryType)
		result.SourceURL = e.musicBrainzBase + "/release/" + release.ID
		result.MissingArtists = missingMusicBrainzArtists(release.ArtistCredit, uniqueMusicArtists(append([]string{input.Artist}, input.Artists...)))
		if e.coverArtBase != "" {
			result.CoverURL = e.coverArtBase + "/release/" + release.ID + "/front-500"
		}
		result.Tracks = applyMusicBrainzTracks(result.Tracks, release, trackMapping)
	}

	missingLyrics := make([]int, 0, len(result.Tracks))
	for index := range result.Tracks {
		track := &result.Tracks[index]
		original := metadataTrackForResult(input.Tracks, *track)
		if lyrics, ok := findLocalLyrics(input.LocalLyrics, original); ok {
			track.Lyrics = &lyrics
			track.LyricsSource = "local"
			continue
		}
		missingLyrics = append(missingLyrics, index)
	}
	if !releaseMatched {
		log.Printf("WARN: skipping LRCLIB lookup because MusicBrainz did not safely match album=%q error=%v", input.AlbumTitle, result.MetadataError)
		return result, nil
	}
	var lyricsWG sync.WaitGroup
	lyricsSlots := make(chan struct{}, 4)
	for _, index := range missingLyrics {
		index := index
		lyricsWG.Add(1)
		go func() {
			defer lyricsWG.Done()
			lyricsSlots <- struct{}{}
			defer func() { <-lyricsSlots }()
			original := metadataTrackForResult(input.Tracks, result.Tracks[index])
			artistCandidates := musicBrainzReleaseArtistNames(release)
			if len(artistCandidates) == 0 {
				artistCandidates = uniqueMusicArtists(append([]string{original.Artist, input.Artist}, input.Artists...))
			}
			lyrics, matchedArtist, lookupErr := e.findLRCLyricsForMusicBrainzTrack(ctx, result.AlbumTitle, artistCandidates, result.Tracks[index], original.DurationSeconds)
			if lookupErr == nil && lyrics.Content != "" {
				result.Tracks[index].Lyrics = &lyrics
				result.Tracks[index].LyricsSource = "lrclib"
				return
			}
			if lookupErr != nil {
				log.Printf("WARN: LRCLIB lyric lookup failed: title=%q artists=%q matched_artist=%q error=%v", result.Tracks[index].Title, strings.Join(artistCandidates, ","), matchedArtist, lookupErr)
			}
		}()
	}
	lyricsWG.Wait()
	return result, nil
}

func metadataTrackForResult(tracks []AlbumImportMetadataTrack, result AlbumImportDTOTrack) AlbumImportMetadataTrack {
	for _, track := range tracks {
		if result.AudioKey != "" && track.AudioKey == result.AudioKey {
			return track
		}
		if result.Origin != "" && track.Origin == result.Origin {
			return track
		}
	}
	for _, track := range tracks {
		if normalizedMusicText(track.Title) == normalizedMusicText(result.Title) {
			return track
		}
	}
	return AlbumImportMetadataTrack{Title: result.Title, DiscNumber: result.DiscNumber, TrackNumber: result.TrackNumber}
}

func baseMetadataTracks(tracks []AlbumImportMetadataTrack) []AlbumImportDTOTrack {
	result := make([]AlbumImportDTOTrack, 0, len(tracks))
	for _, track := range tracks {
		result = append(result, AlbumImportDTOTrack{
			Title: track.Title, AudioKey: track.AudioKey, AudioURL: track.AudioURL, Origin: track.Origin,
			DiscNumber: track.DiscNumber, TrackNumber: track.TrackNumber,
		})
	}
	return result
}

func (e *ExternalAlbumMetadataEnricher) findRelease(ctx context.Context, input AlbumImportMetadataInput) (musicBrainzRelease, []int, error) {
	if e.musicBrainzBase == "" || input.AlbumTitle == "" || len(input.Tracks) == 0 {
		return musicBrainzRelease{}, nil, errors.New("not enough metadata for MusicBrainz lookup")
	}
	if input.PreferredReleaseID != "" {
		var release musicBrainzRelease
		lookupURL := e.musicBrainzBase + "/ws/2/release/" + url.PathEscape(input.PreferredReleaseID) + "?fmt=json&inc=recordings+release-groups+artist-credits"
		if err := e.musicBrainzJSON(ctx, lookupURL, &release); err != nil {
			return musicBrainzRelease{}, nil, err
		}
		if matched, mapping, ok := bestMusicBrainzRelease([]musicBrainzRelease{release}, input.AlbumTitle, input.Tracks); ok {
			return matched, mapping, nil
		}
		return musicBrainzRelease{}, nil, errors.New("preferred MusicBrainz release did not safely match uploaded tracks")
	}
	artists := uniqueMusicArtists(append([]string{input.Artist}, input.Artists...))
	var lastErr error
	for _, artist := range artists {
		release, mapping, err := e.findReleaseWithArtist(ctx, input, artist)
		if err == nil {
			return release, mapping, nil
		}
		lastErr = err
		if artistID, resolveErr := e.findExactArtistID(ctx, artist); resolveErr == nil && artistID != "" {
			release, mapping, err = e.findReleaseWithArtistID(ctx, input, artistID)
			if err == nil {
				return release, mapping, nil
			}
			lastErr = err
		}
	}
	if len(input.Tracks) > 1 {
		release, mapping, err := e.findReleaseWithArtist(ctx, input, "")
		if err == nil {
			return release, mapping, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("MusicBrainz has no safe matching release")
	}
	return musicBrainzRelease{}, nil, lastErr
}

func (e *ExternalAlbumMetadataEnricher) findReleaseWithArtist(ctx context.Context, input AlbumImportMetadataInput, artist string) (musicBrainzRelease, []int, error) {
	return e.findReleaseWithArtistQuery(ctx, input, artist, "")
}

func (e *ExternalAlbumMetadataEnricher) findReleaseWithArtistID(ctx context.Context, input AlbumImportMetadataInput, artistID string) (musicBrainzRelease, []int, error) {
	return e.findReleaseWithArtistQuery(ctx, input, "", artistID)
}

func (e *ExternalAlbumMetadataEnricher) findReleaseWithArtistQuery(ctx context.Context, input AlbumImportMetadataInput, artist, artistID string) (musicBrainzRelease, []int, error) {
	release, mapping, groupErr := e.findReleaseFromGroups(ctx, input, artist, artistID)
	if groupErr == nil {
		return release, mapping, nil
	}
	release, mapping, directErr := e.findReleaseDirectly(ctx, input, artist, artistID)
	if directErr == nil {
		return release, mapping, nil
	}
	return musicBrainzRelease{}, nil, fmt.Errorf("release-group search: %v; release search: %w", groupErr, directErr)
}

func (e *ExternalAlbumMetadataEnricher) findReleaseFromGroups(ctx context.Context, input AlbumImportMetadataInput, artist, artistID string) (musicBrainzRelease, []int, error) {
	lookupTitle := musicBrainzLookupAlbumTitle(input.AlbumTitle)
	query := `releasegroup:"` + escapeMusicBrainzQuery(lookupTitle) + `"`
	if artist != "" {
		query += ` AND artist:"` + escapeMusicBrainzQuery(artist) + `"`
	} else if artistID != "" {
		query += ` AND arid:` + artistID
	}
	var search struct {
		ReleaseGroups []musicBrainzReleaseGroup `json:"release-groups"`
	}
	searchURL := e.musicBrainzBase + "/ws/2/release-group/?fmt=json&limit=10&query=" + url.QueryEscape(query)
	if err := e.musicBrainzJSON(ctx, searchURL, &search); err != nil {
		return musicBrainzRelease{}, nil, err
	}
	for _, group := range search.ReleaseGroups {
		if !musicBrainzAlbumTitlesMatch(group.Title, input.AlbumTitle) || (artistID == "" && !musicBrainzArtistMatches(group.ArtistCredit, artist)) {
			continue
		}
		var releases struct {
			Releases []musicBrainzRelease `json:"releases"`
		}
		lookupURL := e.musicBrainzBase + "/ws/2/release?fmt=json&limit=100&release-group=" + url.QueryEscape(group.ID) + "&inc=recordings+release-groups+artist-credits"
		if err := e.musicBrainzJSON(ctx, lookupURL, &releases); err != nil {
			continue
		}
		if release, mapping, ok := bestMusicBrainzRelease(releases.Releases, input.AlbumTitle, input.Tracks); ok {
			return release, mapping, nil
		}
	}
	return musicBrainzRelease{}, nil, errors.New("MusicBrainz release groups did not match uploaded tracks")
}

func (e *ExternalAlbumMetadataEnricher) findReleaseDirectly(ctx context.Context, input AlbumImportMetadataInput, artist, artistID string) (musicBrainzRelease, []int, error) {
	query := `release:"` + escapeMusicBrainzQuery(musicBrainzLookupAlbumTitle(input.AlbumTitle)) + `"`
	if artist != "" {
		query += ` AND artist:"` + escapeMusicBrainzQuery(artist) + `"`
	} else if artistID != "" {
		query += ` AND arid:` + artistID
	}
	var search struct {
		Releases []musicBrainzRelease `json:"releases"`
	}
	searchURL := e.musicBrainzBase + "/ws/2/release/?fmt=json&limit=10&query=" + url.QueryEscape(query)
	if err := e.musicBrainzJSON(ctx, searchURL, &search); err != nil {
		return musicBrainzRelease{}, nil, err
	}
	if len(search.Releases) == 0 {
		return musicBrainzRelease{}, nil, errors.New("MusicBrainz has no release candidates")
	}
	detailedCandidates := make([]musicBrainzRelease, 0, len(search.Releases))
	for _, candidate := range search.Releases {
		var detailed musicBrainzRelease
		lookupURL := e.musicBrainzBase + "/ws/2/release/" + url.PathEscape(candidate.ID) + "?fmt=json&inc=recordings+release-groups+artist-credits"
		if err := e.musicBrainzJSON(ctx, lookupURL, &detailed); err != nil {
			continue
		}
		detailedCandidates = append(detailedCandidates, detailed)
	}
	if release, mapping, ok := bestMusicBrainzRelease(detailedCandidates, input.AlbumTitle, input.Tracks); ok {
		return release, mapping, nil
	}
	return musicBrainzRelease{}, nil, errors.New("MusicBrainz candidates did not match uploaded tracks")
}

func (e *ExternalAlbumMetadataEnricher) findExactArtistID(ctx context.Context, artist string) (string, error) {
	query := `artist:"` + escapeMusicBrainzQuery(artist) + `"`
	var search struct {
		Artists []musicBrainzArtistSearchResult `json:"artists"`
	}
	searchURL := e.musicBrainzBase + "/ws/2/artist/?fmt=json&limit=10&query=" + url.QueryEscape(query)
	if err := e.musicBrainzJSON(ctx, searchURL, &search); err != nil {
		return "", err
	}
	want := compactMusicText(artist)
	for _, candidate := range search.Artists {
		if candidate.Score < 90 {
			continue
		}
		if compactMusicText(candidate.Name) == want {
			return candidate.ID, nil
		}
		for _, alias := range candidate.Aliases {
			if compactMusicText(alias.Name) == want {
				return candidate.ID, nil
			}
		}
	}
	return "", errors.New("MusicBrainz has no exact artist match")
}

func musicBrainzArtistMatches(credits []musicBrainzArtistCredit, artist string) bool {
	if artist == "" {
		return true
	}
	want := compactMusicText(artist)
	for _, credit := range credits {
		if compactMusicText(credit.Name) == want || compactMusicText(credit.Artist.Name) == want {
			return true
		}
		for _, alias := range credit.Artist.Aliases {
			if compactMusicText(alias.Name) == want {
				return true
			}
		}
	}
	return false
}

func bestMusicBrainzRelease(candidates []musicBrainzRelease, albumTitle string, uploaded []AlbumImportMetadataTrack) (musicBrainzRelease, []int, bool) {
	bestIndex := -1
	bestMatchedTracks := 0
	bestDurationDifference := 0.0
	var bestMapping []int
	for index, candidate := range candidates {
		if candidate.ReleaseGroup.Title != "" && !musicBrainzAlbumTitlesMatch(candidate.ReleaseGroup.Title, albumTitle) && !musicBrainzAlbumTitlesMatch(candidate.Title, albumTitle) {
			continue
		}
		mapping, ok := matchMusicBrainzTracks(candidate, uploaded)
		if !ok {
			continue
		}
		matchedTracks := musicBrainzMatchedTrackCount(mapping)
		difference := musicBrainzDurationDifference(candidate, uploaded, mapping)
		if bestIndex < 0 || matchedTracks > bestMatchedTracks ||
			(matchedTracks == bestMatchedTracks && musicBrainzReleaseStatusRank(candidate.Status) > musicBrainzReleaseStatusRank(candidates[bestIndex].Status)) ||
			(matchedTracks == bestMatchedTracks && musicBrainzReleaseStatusRank(candidate.Status) == musicBrainzReleaseStatusRank(candidates[bestIndex].Status) && difference < bestDurationDifference) {
			bestIndex = index
			bestMatchedTracks = matchedTracks
			bestDurationDifference = difference
			bestMapping = mapping
		}
	}
	if bestIndex < 0 {
		return musicBrainzRelease{}, nil, false
	}
	return candidates[bestIndex], bestMapping, true
}

func musicBrainzMatchedTrackCount(mapping []int) int {
	count := 0
	for _, uploadedIndex := range mapping {
		if uploadedIndex >= 0 {
			count++
		}
	}
	return count
}

func musicBrainzDurationDifference(release musicBrainzRelease, uploaded []AlbumImportMetadataTrack, mapping []int) float64 {
	difference := 0.0
	for index, remote := range flattenMusicBrainzTracks(release) {
		if index < len(mapping) && mapping[index] >= 0 && remote.DurationMS > 0 && uploaded[mapping[index]].DurationSeconds > 0 {
			difference += absFloat(float64(remote.DurationMS)/1000 - uploaded[mapping[index]].DurationSeconds)
		}
	}
	return difference
}

// matchMusicBrainzTracks maps safely identifiable remote tracks to uploaded audio.
// A release is accepted when at least 70% of the smaller track list is matched.
func matchMusicBrainzTracks(release musicBrainzRelease, uploaded []AlbumImportMetadataTrack) ([]int, bool) {
	remote := flattenMusicBrainzTracks(release)
	if len(remote) == 0 || len(uploaded) == 0 {
		return nil, false
	}
	if len(remote) == len(uploaded) {
		if mapping, ok := matchMusicBrainzTracksByPosition(remote, uploaded); ok {
			return mapping, true
		}
		if mapping, ok := matchMusicBrainzTracksByDurationMajority(remote, uploaded); ok {
			return mapping, true
		}
	}
	mapping := make([]int, len(remote))
	for index := range mapping {
		mapping[index] = -1
	}
	used := make([]bool, len(uploaded))

	// Titles are the strongest signal. Compact normalization handles punctuation
	// and spacing variants such as "God's Gift" and "Gods Gift".
	for remoteIndex := range remote {
		candidates := unmatchedTrackCandidates(uploaded, used, func(track AlbumImportMetadataTrack) bool {
			return comparableMusicTitle(remote[remoteIndex].Title) == comparableMusicTitle(track.Title)
		})
		if len(candidates) == 1 {
			mapping[remoteIndex] = candidates[0]
			used[candidates[0]] = true
		}
	}

	// Repeated titles may still be resolved safely when only one remaining
	// duration fits in both directions.
	for {
		matched := false
		for remoteIndex := range remote {
			if mapping[remoteIndex] >= 0 {
				continue
			}
			candidates := unmatchedTrackCandidates(uploaded, used, func(track AlbumImportMetadataTrack) bool {
				return trackDurationsMatch(remote[remoteIndex], track)
			})
			if len(candidates) != 1 || unmatchedRemoteDurationCandidates(remote, mapping, uploaded[candidates[0]]) != 1 {
				continue
			}
			mapping[remoteIndex] = candidates[0]
			used[candidates[0]] = true
			matched = true
		}
		if !matched {
			break
		}
	}

	matched := 0
	for _, uploadedIndex := range mapping {
		if uploadedIndex >= 0 {
			matched++
		}
	}
	requiredBase := minInt(len(remote), len(uploaded))
	if matched*100/requiredBase < 70 {
		return nil, false
	}
	return mapping, true
}

func matchMusicBrainzTracksByDurationMajority(remote []flattenedMusicBrainzTrack, uploaded []AlbumImportMetadataTrack) ([]int, bool) {
	if len(remote) == 0 {
		return nil, false
	}
	mapping := make([]int, len(remote))
	for index := range mapping {
		mapping[index] = -1
	}
	used := make([]bool, len(uploaded))
	matched := 0
	for remoteIndex := range remote {
		candidates := unmatchedTrackCandidates(uploaded, used, func(track AlbumImportMetadataTrack) bool {
			return trackDurationsMatch(remote[remoteIndex], track)
		})
		if len(candidates) == 1 && unmatchedRemoteDurationCandidates(remote, mapping, uploaded[candidates[0]]) == 1 {
			mapping[remoteIndex] = candidates[0]
			used[candidates[0]] = true
			matched++
		}
	}
	if matched*100/len(remote) < 70 {
		return nil, false
	}
	for remoteIndex := range remote {
		if mapping[remoteIndex] >= 0 {
			continue
		}
		candidates := unmatchedTrackCandidates(uploaded, used, func(track AlbumImportMetadataTrack) bool {
			return comparableMusicTitle(remote[remoteIndex].Title) == comparableMusicTitle(track.Title)
		})
		if len(candidates) != 1 {
			return nil, false
		}
		mapping[remoteIndex] = candidates[0]
		used[candidates[0]] = true
	}
	return mapping, true
}

func missingMusicBrainzArtists(credits []musicBrainzArtistCredit, localArtists []string) []string {
	missing := []string{}
	local := map[string]bool{}
	for _, name := range localArtists {
		local[compactMusicText(name)] = true
	}
	for _, credit := range credits {
		name := strings.TrimSpace(credit.Artist.Name)
		if name == "" {
			name = strings.TrimSpace(credit.Name)
		}
		if name != "" && !local[compactMusicText(name)] {
			missing = append(missing, name)
		}
	}
	return missing
}

func uniqueMusicArtists(values []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := compactMusicText(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

func musicBrainzLookupAlbumTitle(value string) string {
	value = strings.TrimSpace(value)
	for {
		trimmed := strings.TrimSpace(strings.TrimRightFunc(value, func(r rune) bool { return r == ')' || r == ']' }))
		lower := strings.ToLower(trimmed)
		removed := false
		for _, suffix := range []string{"(deluxe", "[deluxe", "(deluxe edition", "[deluxe edition"} {
			if index := strings.LastIndex(lower, suffix); index > 0 {
				value = strings.TrimSpace(trimmed[:index])
				removed = true
				break
			}
		}
		if !removed {
			return value
		}
	}
}

func musicBrainzAlbumTitlesMatch(left, right string) bool {
	return compactMusicText(musicBrainzLookupAlbumTitle(left)) == compactMusicText(musicBrainzLookupAlbumTitle(right))
}

func matchMusicBrainzTracksByPosition(remote []flattenedMusicBrainzTrack, uploaded []AlbumImportMetadataTrack) ([]int, bool) {
	mapping := make([]int, len(remote))
	used := make([]bool, len(uploaded))
	for remoteIndex, remoteTrack := range remote {
		candidate := -1
		for uploadedIndex, uploadedTrack := range uploaded {
			if used[uploadedIndex] || normalizedDiscNumber(uploadedTrack.DiscNumber) != remoteTrack.Disc || uploadedTrack.TrackNumber != remoteTrack.Position {
				continue
			}
			if candidate >= 0 {
				return nil, false
			}
			candidate = uploadedIndex
		}
		if candidate < 0 || (comparableMusicTitle(remoteTrack.Title) != comparableMusicTitle(uploaded[candidate].Title) && !trackDurationsMatch(remoteTrack, uploaded[candidate])) {
			return nil, false
		}
		mapping[remoteIndex] = candidate
		used[candidate] = true
	}
	return mapping, true
}

func unmatchedTrackCandidates(tracks []AlbumImportMetadataTrack, used []bool, matches func(AlbumImportMetadataTrack) bool) []int {
	candidates := []int{}
	for index, track := range tracks {
		if !used[index] && matches(track) {
			candidates = append(candidates, index)
		}
	}
	return candidates
}

func unmatchedRemoteDurationCandidates(remote []flattenedMusicBrainzTrack, mapping []int, uploaded AlbumImportMetadataTrack) int {
	count := 0
	for index := range remote {
		if mapping[index] < 0 && trackDurationsMatch(remote[index], uploaded) {
			count++
		}
	}
	return count
}

func trackDurationsMatch(remote flattenedMusicBrainzTrack, uploaded AlbumImportMetadataTrack) bool {
	remoteDuration := float64(remote.DurationMS) / 1000
	return remoteDuration > 0 && uploaded.DurationSeconds > 0 && absFloat(remoteDuration-uploaded.DurationSeconds) <= 4
}

type flattenedMusicBrainzTrack struct {
	Title      string
	Disc       int
	Position   int
	DurationMS int
}

func flattenMusicBrainzTracks(release musicBrainzRelease) []flattenedMusicBrainzTrack {
	tracks := []flattenedMusicBrainzTrack{}
	for mediaIndex, medium := range release.Media {
		disc := medium.Position
		if disc <= 0 {
			disc = mediaIndex + 1
		}
		for trackIndex, track := range medium.Tracks {
			position := track.Position
			if position <= 0 {
				position = trackIndex + 1
			}
			title := strings.TrimSpace(track.Title)
			if title == "" {
				title = strings.TrimSpace(track.Recording.Title)
			}
			length := track.Length
			if length <= 0 {
				length = track.Recording.Length
			}
			tracks = append(tracks, flattenedMusicBrainzTrack{Title: title, Disc: disc, Position: position, DurationMS: length})
		}
	}
	return tracks
}

func applyMusicBrainzTracks(tracks []AlbumImportDTOTrack, release musicBrainzRelease, mapping []int) []AlbumImportDTOTrack {
	remote := flattenMusicBrainzTracks(release)
	if len(mapping) != len(remote) {
		return tracks
	}
	result := make([]AlbumImportDTOTrack, 0, len(tracks))
	used := make([]bool, len(tracks))
	for remoteIndex, uploadedIndex := range mapping {
		if uploadedIndex < 0 {
			continue
		}
		if uploadedIndex >= len(tracks) || used[uploadedIndex] {
			return tracks
		}
		used[uploadedIndex] = true
		track := tracks[uploadedIndex]
		track.Title = remote[remoteIndex].Title
		track.DiscNumber = remote[remoteIndex].Disc
		track.TrackNumber = remote[remoteIndex].Position
		result = append(result, track)
	}
	for index, track := range tracks {
		if !used[index] {
			result = append(result, track)
		}
	}
	return result
}

func musicBrainzReleaseStatusRank(status string) int {
	if strings.EqualFold(strings.TrimSpace(status), "Official") {
		return 2
	}
	return 1
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (e *ExternalAlbumMetadataEnricher) musicBrainzJSON(ctx context.Context, endpoint string, target any) error {
	e.requestMu.Lock()
	defer e.requestMu.Unlock()
	if wait := e.musicBrainzWait - time.Since(e.lastMBRequest); wait > 0 && !e.lastMBRequest.IsZero() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	err := e.getJSON(ctx, endpoint, target, e.userAgent)
	e.lastMBRequest = time.Now()
	return err
}

func musicBrainzReleaseArtistNames(release musicBrainzRelease) []string {
	names := make([]string, 0, len(release.ArtistCredit)*3)
	for _, credit := range release.ArtistCredit {
		names = append(names, credit.Name, credit.Artist.Name)
		for _, alias := range credit.Artist.Aliases {
			names = append(names, alias.Name)
		}
	}
	return uniqueMusicArtists(names)
}

func (e *ExternalAlbumMetadataEnricher) findLRCLyricsForMusicBrainzTrack(ctx context.Context, album string, artists []string, track AlbumImportDTOTrack, duration float64) (AlbumImportTrackLyricsPayload, string, error) {
	if e.lrcLibBase == "" {
		return AlbumImportTrackLyricsPayload{}, "", errors.New("LRCLIB is not configured")
	}
	var lastErr error
	for _, artist := range uniqueMusicArtists(artists) {
		lyrics, err := e.findLRCLyricsForTitle(ctx, album, artist, track, duration)
		if err == nil {
			return lyrics, artist, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("not enough metadata for LRCLIB lookup")
	}
	return AlbumImportTrackLyricsPayload{}, "", lastErr
}

func (e *ExternalAlbumMetadataEnricher) findLRCLyricsForTitle(ctx context.Context, album, artist string, track AlbumImportDTOTrack, duration float64) (AlbumImportTrackLyricsPayload, error) {
	if track.Title == "" {
		return AlbumImportTrackLyricsPayload{}, errors.New("not enough metadata for LRCLIB lookup")
	}
	params := url.Values{"track_name": {track.Title}, "artist_name": {artist}}
	if album != "" {
		params.Set("album_name", album)
	}
	if duration > 0 {
		params.Set("duration", fmt.Sprintf("%.0f", duration))
	}
	var response struct {
		PlainLyrics  string  `json:"plainLyrics"`
		SyncedLyrics string  `json:"syncedLyrics"`
		Duration     float64 `json:"duration"`
	}
	if err := e.lrcLibJSON(ctx, e.lrcLibBase+"/api/get?"+params.Encode(), &response); err != nil {
		if !isMetadataServiceStatus(err, http.StatusNotFound) {
			return AlbumImportTrackLyricsPayload{}, err
		}
		return e.searchLRCLyrics(ctx, artist, track, duration)
	}
	if lyrics, ok := newLRCLyricsPayload(response.PlainLyrics, response.SyncedLyrics, response.Duration, duration); ok {
		return lyrics, nil
	}
	return e.searchLRCLyrics(ctx, artist, track, duration)
}

func (e *ExternalAlbumMetadataEnricher) searchLRCLyrics(ctx context.Context, artist string, track AlbumImportDTOTrack, duration float64) (AlbumImportTrackLyricsPayload, error) {
	params := url.Values{"track_name": {track.Title}, "artist_name": {artist}}
	var responses []struct {
		TrackName    string  `json:"trackName"`
		PlainLyrics  string  `json:"plainLyrics"`
		SyncedLyrics string  `json:"syncedLyrics"`
		Duration     float64 `json:"duration"`
	}
	if err := e.lrcLibJSON(ctx, e.lrcLibBase+"/api/search?"+params.Encode(), &responses); err != nil {
		return AlbumImportTrackLyricsPayload{}, err
	}
	for _, response := range responses {
		if response.TrackName != "" && comparableMusicTitle(response.TrackName) != comparableMusicTitle(track.Title) {
			continue
		}
		if lyrics, ok := newLRCLyricsPayload(response.PlainLyrics, response.SyncedLyrics, response.Duration, duration); ok {
			return lyrics, nil
		}
	}
	return AlbumImportTrackLyricsPayload{}, errors.New("LRCLIB returned no lyrics")
}

func newLRCLyricsPayload(plainLyrics, syncedLyrics string, responseDuration, requestedDuration float64) (AlbumImportTrackLyricsPayload, bool) {
	if requestedDuration > 0 && responseDuration > 0 && absFloat(requestedDuration-responseDuration) > 4 {
		return AlbumImportTrackLyricsPayload{}, false
	}
	plain := strings.TrimSpace(plainLyrics)
	synced := strings.TrimSpace(syncedLyrics)
	if synced != "" {
		if _, err := ParseLyricLines(synced, "", "lrc"); err == nil {
			return AlbumImportTrackLyricsPayload{Content: synced, Format: "lrc", EditSummary: "自动匹配歌词"}, true
		}
	}
	if plain != "" {
		return AlbumImportTrackLyricsPayload{Content: plain, Format: "plain", EditSummary: "自动匹配歌词"}, true
	}
	return AlbumImportTrackLyricsPayload{}, false
}

type metadataServiceHTTPError struct {
	statusCode int
	status     string
	retryAfter time.Duration
}

func (e metadataServiceHTTPError) Error() string {
	return fmt.Sprintf("metadata service returned %s", e.status)
}

func isMetadataServiceStatus(err error, statusCode int) bool {
	var statusErr metadataServiceHTTPError
	return errors.As(err, &statusErr) && statusErr.statusCode == statusCode
}

func (e *ExternalAlbumMetadataEnricher) lrcLibJSON(ctx context.Context, endpoint string, target any) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := e.waitForLRCLIBRequest(ctx); err != nil {
			return err
		}
		err := e.getJSON(ctx, endpoint, target, e.userAgent)
		var statusErr metadataServiceHTTPError
		if err == nil || !errors.As(err, &statusErr) || statusErr.statusCode != http.StatusTooManyRequests || attempt == 2 {
			return err
		}
		wait := statusErr.retryAfter
		if wait <= 0 {
			wait = time.Duration(2<<attempt) * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("LRCLIB request retries exhausted")
}

func (e *ExternalAlbumMetadataEnricher) waitForLRCLIBRequest(ctx context.Context) error {
	e.lrcLibMu.Lock()
	defer e.lrcLibMu.Unlock()
	if wait := e.lrcLibWait - time.Since(e.lastLRCRequest); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	e.lastLRCRequest = time.Now()
	return nil
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		if wait := time.Until(retryAt); wait > 0 {
			return wait
		}
	}
	return 0
}

func (e *ExternalAlbumMetadataEnricher) getJSON(ctx context.Context, endpoint string, target any, userAgent string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	response, err := e.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return metadataServiceHTTPError{statusCode: response.StatusCode, status: response.Status, retryAfter: parseRetryAfter(response.Header.Get("Retry-After"))}
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(target)
}

func findLocalLyrics(lyrics map[string]AlbumImportTrackLyricsPayload, track AlbumImportMetadataTrack) (AlbumImportTrackLyricsPayload, bool) {
	keys := []string{}
	if track.TrackNumber > 0 {
		keys = append(keys, lyricSequenceKey(track.DiscNumber, track.TrackNumber))
	}
	keys = append(keys, normalizedLyricName(track.Origin), normalizedMusicText(track.Title))
	if track.TrackNumber > 0 {
		keys = append(keys, fmt.Sprintf("%02d", track.TrackNumber), fmt.Sprintf("%d", track.TrackNumber))
	}
	for _, key := range keys {
		if value, ok := lyrics[key]; ok && strings.TrimSpace(value.Content) != "" {
			return value, true
		}
	}
	return AlbumImportTrackLyricsPayload{}, false
}

func normalizedLyricName(value string) string {
	value = strings.ReplaceAll(value, `\`, "/")
	base := value
	if slash := strings.LastIndex(base, "/"); slash >= 0 {
		base = base[slash+1:]
	}
	name, _, track := albumImportTrackInfoFromFileName(base)
	if name != "" {
		return normalizedMusicText(name)
	}
	if track > 0 {
		return fmt.Sprintf("%02d", track)
	}
	return normalizedMusicText(base)
}

func lyricSequenceKey(discNumber, trackNumber int) string {
	return fmt.Sprintf("disc:%d:track:%d", normalizedDiscNumber(discNumber), trackNumber)
}

func normalizedMusicText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer("-", " ", "_", " ", ".", " ").Replace(value)), " "))
}

func compactMusicText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func comparableMusicTitle(value string) string {
	value = regexp.MustCompile(`(?i)(?:[._ -](?:copy)?\d+)$`).ReplaceAllString(strings.TrimSpace(value), "")
	lower := strings.ToLower(value)
	for _, marker := range []string{" (feat.", " (feat ", " (featuring ", " [feat.", " [feat ", " [featuring "} {
		if index := strings.Index(lower, marker); index >= 0 {
			value = value[:index]
			break
		}
	}
	return compactMusicText(value)
}

func normalizeMusicBrainzAlbumType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ep":
		return "ep"
	case "single":
		return "single"
	case "broadcast", "other":
		return "custom"
	default:
		return "album"
	}
}

func escapeMusicBrainzQuery(value string) string { return strings.ReplaceAll(value, `"`, `\"`) }
func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
