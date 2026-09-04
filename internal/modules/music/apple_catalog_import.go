package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const appleCatalogProvider = "apple_music"

type appleChartItem struct {
	ArtistName            string `json:"artistName"`
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	ReleaseDate           string `json:"releaseDate"`
	ArtistID              string `json:"artistId"`
	ArtistURL             string `json:"artistUrl"`
	ArtworkURL100         string `json:"artworkUrl100"`
	URL                   string `json:"url"`
	ContentAdvisoryRating string `json:"contentAdvisoryRating"`
}

type appleChartResponse struct {
	Feed struct {
		Results []appleChartItem `json:"results"`
	} `json:"feed"`
}

type appleLookupResponse struct {
	Results []appleLookupItem `json:"results"`
}

// PreviewURL is deliberately absent: Apple previews must not enter Atoman's
// persistent catalog or global entertainment player.
type appleLookupItem struct {
	WrapperType            string `json:"wrapperType"`
	Kind                   string `json:"kind"`
	ArtistID               int64  `json:"artistId"`
	ArtistName             string `json:"artistName"`
	ArtistLinkURL          string `json:"artistLinkUrl"`
	ArtistViewURL          string `json:"artistViewUrl"`
	CollectionID           int64  `json:"collectionId"`
	CollectionName         string `json:"collectionName"`
	CollectionViewURL      string `json:"collectionViewUrl"`
	CollectionType         string `json:"collectionType"`
	TrackID                int64  `json:"trackId"`
	TrackName              string `json:"trackName"`
	TrackViewURL           string `json:"trackViewUrl"`
	ArtworkURL100          string `json:"artworkUrl100"`
	ReleaseDate            string `json:"releaseDate"`
	PrimaryGenreName       string `json:"primaryGenreName"`
	TrackCount             int    `json:"trackCount"`
	TrackNumber            int    `json:"trackNumber"`
	DiscNumber             int    `json:"discNumber"`
	TrackTimeMillis        int    `json:"trackTimeMillis"`
	TrackExplicitness      string `json:"trackExplicitness"`
	CollectionExplicitness string `json:"collectionExplicitness"`
}

type appleArtistCandidate struct {
	ExternalID       string
	Name             string
	URL              string
	ChartRank        int
	EditorialSources []string
}

type AppleCatalogImportOptions struct {
	Storefront   string
	ArtistLimit  int
	AlbumLimit   int
	SongLimit    int
	RequestDelay time.Duration
	Apply        bool
}

type AppleCatalogImportSummary struct {
	Storefront string
	Candidates int
	Artists    int
	Albums     int
	Songs      int
	Applied    bool
}

type AppleCatalogImporter struct {
	db            *gorm.DB
	client        *http.Client
	ownerID       uuid.UUID
	userAgent     string
	rssBaseURL    string
	lookupBaseURL string
	searchBaseURL string
	delay         time.Duration
	requestMu     sync.Mutex
	lastRequest   time.Time
}

func NewAppleCatalogImporter(db *gorm.DB, client *http.Client, ownerID uuid.UUID, userAgent string) *AppleCatalogImporter {
	return &AppleCatalogImporter{
		db: db, client: client, ownerID: ownerID, userAgent: strings.TrimSpace(userAgent),
		rssBaseURL:    "https://rss.marketingtools.apple.com/api/v2",
		lookupBaseURL: "https://itunes.apple.com/lookup",
		searchBaseURL: "https://itunes.apple.com/search",
	}
}

func (importer *AppleCatalogImporter) Import(ctx context.Context, options AppleCatalogImportOptions) (AppleCatalogImportSummary, error) {
	options = normalizeAppleCatalogOptions(options)
	importer.delay = options.RequestDelay
	storefront := strings.ToLower(options.Storefront)
	songChart, err := importer.fetchChart(ctx, storefront, "songs")
	if err != nil {
		return AppleCatalogImportSummary{}, err
	}
	albumChart, err := importer.fetchChart(ctx, storefront, "albums")
	if err != nil {
		return AppleCatalogImportSummary{}, err
	}
	candidates := appleArtistCandidates(songChart, albumChart, options.ArtistLimit)
	albumRanks := appleChartRanks(albumChart)
	songRanks := appleChartRanks(songChart)
	return importer.importCandidates(ctx, options, candidates, albumRanks, songRanks)
}

// ImportHipHopConsensus imports the fixed editorial-consensus rapper seed list.
func (importer *AppleCatalogImporter) ImportHipHopConsensus(ctx context.Context, options AppleCatalogImportOptions) (AppleCatalogImportSummary, error) {
	options = normalizeAppleCatalogOptions(options)
	importer.delay = options.RequestDelay
	storefront := strings.ToLower(options.Storefront)
	seeds := HipHopConsensusArtistSeeds()
	candidates := make([]appleArtistCandidate, 0, len(seeds))
	for _, seed := range seeds {
		candidate, err := importer.searchArtist(ctx, storefront, seed)
		if err != nil {
			return AppleCatalogImportSummary{Storefront: strings.ToUpper(storefront), Candidates: len(candidates), Applied: options.Apply}, err
		}
		candidates = append(candidates, candidate)
	}
	return importer.importCandidates(ctx, options, candidates, nil, nil)
}

func (importer *AppleCatalogImporter) importCandidates(ctx context.Context, options AppleCatalogImportOptions, candidates []appleArtistCandidate, albumRanks, songRanks map[string]int) (AppleCatalogImportSummary, error) {
	summary := AppleCatalogImportSummary{Storefront: strings.ToUpper(options.Storefront), Candidates: len(candidates), Applied: options.Apply}

	for _, candidate := range candidates {
		albums, err := importer.lookup(ctx, strings.ToLower(options.Storefront), candidate.ExternalID, "album", options.AlbumLimit)
		if err != nil {
			return summary, fmt.Errorf("lookup albums for %s: %w", candidate.Name, err)
		}
		songs, err := importer.lookup(ctx, strings.ToLower(options.Storefront), candidate.ExternalID, "song", options.SongLimit)
		if err != nil {
			return summary, fmt.Errorf("lookup songs for %s: %w", candidate.Name, err)
		}
		collections := appleCollections(albums, candidate.ExternalID, options.AlbumLimit)
		tracks := appleTracksForCollections(songs, collections, options.SongLimit)
		summary.Artists++
		summary.Albums += len(collections)
		summary.Songs += len(tracks)
		if !options.Apply {
			continue
		}
		if err := importer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return importer.syncArtistCatalog(tx, summary.Storefront, candidate, collections, tracks, albumRanks, songRanks)
		}); err != nil {
			return summary, fmt.Errorf("sync %s: %w", candidate.Name, err)
		}
	}
	return summary, nil
}

func normalizeAppleCatalogOptions(options AppleCatalogImportOptions) AppleCatalogImportOptions {
	options.Storefront = strings.TrimSpace(options.Storefront)
	if options.Storefront == "" {
		options.Storefront = "CN"
	}
	if options.ArtistLimit < 1 || options.ArtistLimit > 100 {
		options.ArtistLimit = 100
	}
	if options.AlbumLimit < 1 || options.AlbumLimit > 200 {
		options.AlbumLimit = 20
	}
	if options.SongLimit < 1 || options.SongLimit > 200 {
		options.SongLimit = 200
	}
	if options.RequestDelay < 0 {
		options.RequestDelay = 0
	}
	return options
}

func appleArtistCandidates(songs, albums []appleChartItem, limit int) []appleArtistCandidate {
	result := make([]appleArtistCandidate, 0, limit)
	byID := make(map[string]int, limit)
	maxRows := len(songs)
	if len(albums) > maxRows {
		maxRows = len(albums)
	}
	add := func(item appleChartItem, rank int) {
		id := strings.TrimSpace(item.ArtistID)
		if id == "" || strings.TrimSpace(item.ArtistName) == "" {
			return
		}
		if index, exists := byID[id]; exists {
			if rank < result[index].ChartRank {
				result[index].ChartRank = rank
			}
			return
		}
		if len(result) >= limit {
			return
		}
		byID[id] = len(result)
		result = append(result, appleArtistCandidate{ExternalID: id, Name: item.ArtistName, URL: item.ArtistURL, ChartRank: rank})
	}
	for index := 0; index < maxRows && len(result) < limit; index++ {
		if index < len(songs) {
			add(songs[index], index+1)
		}
		if index < len(albums) {
			add(albums[index], index+1)
		}
	}
	return result
}

func appleChartRanks(items []appleChartItem) map[string]int {
	result := make(map[string]int, len(items))
	for index, item := range items {
		if previous, exists := result[item.ID]; !exists || index+1 < previous {
			result[item.ID] = index + 1
		}
	}
	return result
}

func appleCollections(items []appleLookupItem, artistExternalID string, limit int) []appleLookupItem {
	result := make([]appleLookupItem, 0, limit)
	seen := make(map[int64]struct{}, limit)
	for _, item := range items {
		if item.WrapperType != "collection" {
			continue
		}
		if item.CollectionID == 0 || strings.TrimSpace(item.CollectionName) == "" {
			continue
		}
		if strconv.FormatInt(item.ArtistID, 10) != strings.TrimSpace(artistExternalID) {
			continue
		}
		if appleAlbumType(item.CollectionName, item.TrackCount) != "album" {
			continue
		}
		if _, exists := seen[item.CollectionID]; exists {
			continue
		}
		seen[item.CollectionID] = struct{}{}
		result = append(result, item)
		if len(result) == limit {
			break
		}
	}
	return result
}

func appleTracksForCollections(items, collections []appleLookupItem, limit int) []appleLookupItem {
	allowed := make(map[int64]struct{}, len(collections))
	for _, item := range collections {
		allowed[item.CollectionID] = struct{}{}
	}
	result := make([]appleLookupItem, 0, limit)
	seen := make(map[int64]struct{}, limit)
	for _, item := range items {
		if item.WrapperType != "track" || item.Kind != "song" || item.TrackID == 0 || strings.TrimSpace(item.TrackName) == "" {
			continue
		}
		if _, ok := allowed[item.CollectionID]; !ok {
			continue
		}
		if _, exists := seen[item.TrackID]; exists {
			continue
		}
		seen[item.TrackID] = struct{}{}
		result = append(result, item)
		if len(result) == limit {
			break
		}
	}
	return result
}

func appleAlbumType(name string, trackCount int) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasSuffix(lower, " - single") || trackCount == 1 {
		return "single"
	}
	if strings.HasSuffix(lower, " - ep") {
		return "ep"
	}
	return "album"
}

func (importer *AppleCatalogImporter) fetchChart(ctx context.Context, storefront, kind string) ([]appleChartItem, error) {
	endpoint := fmt.Sprintf("%s/%s/music/most-played/100/%s.json", strings.TrimRight(importer.rssBaseURL, "/"), storefront, kind)
	var response appleChartResponse
	if err := importer.fetchJSON(ctx, endpoint, &response); err != nil {
		return nil, fmt.Errorf("fetch Apple %s chart: %w", kind, err)
	}
	return response.Feed.Results, nil
}

func (importer *AppleCatalogImporter) lookup(ctx context.Context, storefront, id, entity string, limit int) ([]appleLookupItem, error) {
	query := url.Values{}
	query.Set("id", id)
	query.Set("country", strings.ToUpper(storefront))
	query.Set("entity", entity)
	query.Set("limit", strconv.Itoa(limit))
	var response appleLookupResponse
	if err := importer.fetchJSON(ctx, importer.lookupBaseURL+"?"+query.Encode(), &response); err != nil {
		return nil, err
	}
	return response.Results, nil
}

func (importer *AppleCatalogImporter) searchArtist(ctx context.Context, storefront string, seed HipHopConsensusArtistSeed) (appleArtistCandidate, error) {
	query := url.Values{}
	query.Set("term", seed.Name)
	query.Set("country", strings.ToUpper(storefront))
	query.Set("media", "music")
	query.Set("entity", "musicArtist")
	query.Set("limit", "10")
	var response struct {
		Results []struct {
			ArtistID      int64  `json:"artistId"`
			ArtistName    string `json:"artistName"`
			ArtistViewURL string `json:"artistViewUrl"`
		} `json:"results"`
	}
	if err := importer.fetchJSON(ctx, importer.searchBaseURL+"?"+query.Encode(), &response); err != nil {
		return appleArtistCandidate{}, fmt.Errorf("search Apple artist %s: %w", seed.Name, err)
	}
	for _, item := range response.Results {
		if item.ArtistID > 0 && normalizeAppleArtistName(item.ArtistName) == normalizeAppleArtistName(seed.Name) {
			return appleArtistCandidate{
				ExternalID: strconv.FormatInt(item.ArtistID, 10), Name: item.ArtistName, URL: item.ArtistViewURL,
				ChartRank: seed.Rank, EditorialSources: hipHopConsensusEditorialSources,
			}, nil
		}
	}
	return appleArtistCandidate{}, fmt.Errorf("Apple artist search did not return an exact match for %s", seed.Name)
}

func normalizeAppleArtistName(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		switch character {
		case 'á', 'à', 'â', 'ä', 'å':
			character = 'a'
		case 'é', 'è', 'ê', 'ë':
			character = 'e'
		case 'í', 'ì', 'î', 'ï':
			character = 'i'
		case 'ó', 'ò', 'ô', 'ö', 'ø':
			character = 'o'
		case 'ú', 'ù', 'û', 'ü':
			character = 'u'
		case 'ý', 'ÿ':
			character = 'y'
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func (importer *AppleCatalogImporter) fetchJSON(ctx context.Context, endpoint string, target any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := importer.fetchJSONOnce(ctx, endpoint, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (importer *AppleCatalogImporter) fetchJSONOnce(ctx context.Context, endpoint string, target any) error {
	importer.requestMu.Lock()
	if wait := importer.delay - time.Since(importer.lastRequest); wait > 0 && !importer.lastRequest.IsZero() {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			importer.requestMu.Unlock()
			return ctx.Err()
		case <-timer.C:
		}
	}
	importer.lastRequest = time.Now()
	importer.requestMu.Unlock()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if importer.userAgent != "" {
		request.Header.Set("User-Agent", importer.userAgent)
	}
	response, err := importer.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Apple API returned %s", response.Status)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func (importer *AppleCatalogImporter) syncArtistCatalog(tx *gorm.DB, storefront string, candidate appleArtistCandidate, collections, tracks []appleLookupItem, albumRanks, songRanks map[string]int) error {
	artist, err := importer.findOrCreateAppleArtist(tx, storefront, candidate)
	if err != nil {
		return err
	}
	albums := make(map[int64]model.Album, len(collections))
	for _, item := range collections {
		album, err := importer.findOrCreateAppleAlbum(tx, storefront, artist, item, albumRanks[strconv.FormatInt(item.CollectionID, 10)])
		if err != nil {
			return err
		}
		albums[item.CollectionID] = album
	}
	for _, item := range tracks {
		album, ok := albums[item.CollectionID]
		if !ok {
			continue
		}
		if err := importer.findOrCreateAppleSong(tx, storefront, artist, album, item, songRanks[strconv.FormatInt(item.TrackID, 10)]); err != nil {
			return err
		}
	}
	return nil
}

func (importer *AppleCatalogImporter) findOrCreateAppleArtist(tx *gorm.DB, storefront string, candidate appleArtistCandidate) (model.Artist, error) {
	externalID := candidate.ExternalID
	var artist model.Artist
	link, err := findMusicCatalogLink(tx, "artist", externalID)
	if err == nil {
		if err := tx.First(&artist, "id = ?", link.EntityID).Error; err != nil {
			return artist, err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return artist, err
	} else {
		query := tx.Where("LOWER(name) = LOWER(?)", candidate.Name).Limit(1).Find(&artist)
		if query.Error != nil {
			return artist, query.Error
		}
		if query.RowsAffected == 0 {
			artist = model.Artist{Name: candidate.Name, ArtistForm: "person", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive, EditStatus: model.MusicEditDevelopment, CreatedBy: &importer.ownerID, SourcesJSON: appleSourceJSON("", candidate.URL)}
			if err := tx.Create(&artist).Error; err != nil {
				return artist, err
			}
		} else if err := addAppleSource(tx, &artist, candidate.URL); err != nil {
			return artist, err
		}
	}
	metadata := map[string]any{"name": candidate.Name}
	if len(candidate.EditorialSources) > 0 {
		metadata["editorial_sources"] = candidate.EditorialSources
		metadata["editorial_rank"] = candidate.ChartRank
	}
	return artist, upsertMusicCatalogLink(tx, "artist", externalID, artist.ID, storefront, candidate.URL, candidate.ChartRank, metadata)
}

func (importer *AppleCatalogImporter) findOrCreateAppleAlbum(tx *gorm.DB, storefront string, artist model.Artist, item appleLookupItem, chartRank int) (model.Album, error) {
	externalID := strconv.FormatInt(item.CollectionID, 10)
	var album model.Album
	link, err := findMusicCatalogLink(tx, "album", externalID)
	if err == nil {
		if err := tx.First(&album, "id = ?", link.EntityID).Error; err != nil {
			return album, err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return album, err
	} else {
		query := tx.Table(`"Albums"`).Joins("JOIN album_artists ON album_artists.album_id = \"Albums\".id").Where("album_artists.artist_id = ? AND LOWER(\"Albums\".title) = LOWER(?)", artist.ID, item.CollectionName).Limit(1).Find(&album)
		if query.Error != nil {
			return album, query.Error
		}
		if query.RowsAffected == 0 {
			releaseDate := parseAppleDate(item.ReleaseDate)
			releaseYear := 0
			if !releaseDate.IsZero() {
				releaseYear = releaseDate.Year()
			}
			album = model.Album{Title: item.CollectionName, ReleaseDate: releaseDate, ReleaseYear: releaseYear, Year: releaseYear, ReleaseDatePrecision: appleDatePrecision(releaseDate), CoverURL: appleArtworkURL(item.ArtworkURL100), CoverSource: "apple_music", Status: "open", AlbumType: appleAlbumType(item.CollectionName, item.TrackCount), EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive, EditStatus: model.MusicEditDevelopment, UploadedBy: &importer.ownerID, SourcesJSON: appleSourceJSON("", appleCollectionURL(item.CollectionViewURL))}
			if err := tx.Create(&album).Error; err != nil {
				return album, err
			}
		} else if err := addAppleSource(tx, &album, appleCollectionURL(item.CollectionViewURL)); err != nil {
			return album, err
		}
	}
	if coverURL := appleArtworkURL(item.ArtworkURL100); coverURL != "" && album.CoverURL != coverURL {
		if err := tx.Model(&album).Updates(map[string]any{"cover_url": coverURL, "cover_source": "apple_music"}).Error; err != nil {
			return album, err
		}
		album.CoverURL = coverURL
	}
	credit := model.AlbumArtist{AlbumID: album.ID, ArtistID: artist.ID, Role: "primary", Position: 1}
	if err := tx.Where("album_id = ? AND artist_id = ? AND role = ? AND custom_role = ?", album.ID, artist.ID, credit.Role, "").FirstOrCreate(&credit).Error; err != nil {
		return album, err
	}
	return album, upsertMusicCatalogLink(tx, "album", externalID, album.ID, storefront, appleCollectionURL(item.CollectionViewURL), chartRank, item)
}

func (importer *AppleCatalogImporter) findOrCreateAppleSong(tx *gorm.DB, storefront string, artist model.Artist, album model.Album, item appleLookupItem, chartRank int) error {
	externalID := strconv.FormatInt(item.TrackID, 10)
	var song model.Song
	link, err := findMusicCatalogLink(tx, "song", externalID)
	if err == nil {
		if err := tx.First(&song, "id = ?", link.EntityID).Error; err != nil {
			return err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	} else {
		query := tx.Where("album_id = ? AND disc_number = ? AND track_number = ? AND LOWER(title) = LOWER(?)", album.ID, item.DiscNumber, item.TrackNumber, item.TrackName).Limit(1).Find(&song)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected == 0 {
			releaseDate := parseAppleDate(item.ReleaseDate)
			song = model.Song{Title: item.TrackName, ReleaseDate: releaseDate, ReleaseDatePrecision: appleDatePrecision(releaseDate), TrackNumber: item.TrackNumber, DiscNumber: max(item.DiscNumber, 1), AudioURL: "", AudioSource: "", SourcesJSON: appleSourceJSON("", item.TrackViewURL), Status: "open", LifecycleStatus: model.MusicLifecycleActive, EditStatus: model.MusicEditDevelopment, AlbumID: &album.ID, UploadedBy: &importer.ownerID, DurationSec: item.TrackTimeMillis / 1000}
			if err := tx.Create(&song).Error; err != nil {
				return err
			}
		} else if err := addAppleSource(tx, &song, item.TrackViewURL); err != nil {
			return err
		}
	}
	credit := model.SongArtist{SongID: song.ID, ArtistID: artist.ID, Role: "primary", Position: 1}
	if err := tx.Where("song_id = ? AND artist_id = ? AND role = ? AND custom_role = ?", song.ID, artist.ID, credit.Role, "").FirstOrCreate(&credit).Error; err != nil {
		return err
	}
	return upsertMusicCatalogLink(tx, "song", externalID, song.ID, storefront, item.TrackViewURL, chartRank, item)
}

func findMusicCatalogLink(tx *gorm.DB, entityType, externalID string) (model.MusicCatalogLink, error) {
	var link model.MusicCatalogLink
	query := tx.Where("provider = ? AND entity_type = ? AND external_id = ?", appleCatalogProvider, entityType, externalID).Limit(1).Find(&link)
	if query.Error != nil {
		return link, query.Error
	}
	if query.RowsAffected == 0 {
		return link, gorm.ErrRecordNotFound
	}
	return link, nil
}

func upsertMusicCatalogLink(tx *gorm.DB, entityType, externalID string, entityID uuid.UUID, storefront, sourceURL string, chartRank int, metadata any) error {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	link, err := findMusicCatalogLink(tx, entityType, externalID)
	now := time.Now().UTC()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&model.MusicCatalogLink{Provider: appleCatalogProvider, EntityType: entityType, ExternalID: externalID, EntityID: entityID, Storefront: storefront, URL: sourceURL, ChartRank: chartRank, MetadataJSON: string(raw), LastSyncedAt: now}).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&link).Updates(map[string]any{"entity_id": entityID, "storefront": storefront, "url": sourceURL, "chart_rank": chartRank, "metadata_json": string(raw), "last_synced_at": now}).Error
}

func appleSourceJSON(existing, sourceURL string) string {
	var sources []model.MusicSource
	_ = json.Unmarshal([]byte(existing), &sources)
	for index := range sources {
		if sources[index].Title == "Apple Music" {
			sources[index].URL = sourceURL
			raw, _ := json.Marshal(sources)
			return string(raw)
		}
	}
	if strings.TrimSpace(sourceURL) != "" {
		sources = append(sources, model.MusicSource{Type: "url", URL: sourceURL, Title: "Apple Music"})
	}
	raw, _ := json.Marshal(sources)
	return string(raw)
}

func addAppleSource(tx *gorm.DB, entity any, sourceURL string) error {
	var current string
	switch value := entity.(type) {
	case *model.Artist:
		current = value.SourcesJSON
		value.SourcesJSON = appleSourceJSON(current, sourceURL)
		return tx.Model(value).Update("sources_json", value.SourcesJSON).Error
	case *model.Album:
		current = value.SourcesJSON
		value.SourcesJSON = appleSourceJSON(current, sourceURL)
		return tx.Model(value).Update("sources_json", value.SourcesJSON).Error
	case *model.Song:
		current = value.SourcesJSON
		value.SourcesJSON = appleSourceJSON(current, sourceURL)
		return tx.Model(value).Update("sources_json", value.SourcesJSON).Error
	default:
		return fmt.Errorf("unsupported Apple source entity %T", entity)
	}
}

func parseAppleDate(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed
}

func appleCollectionURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return value
	}
	query := parsed.Query()
	query.Del("i")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func appleArtworkURL(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "100x100bb", "1200x1200bb")
}

func appleDatePrecision(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return "day"
}
