package music

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const internetArchiveProvider = "internet_archive"

var (
	internetArchiveHTMLTagPattern = regexp.MustCompile(`<[^>]+>`)
	internetArchiveSpacePattern   = regexp.MustCompile(`\s+`)
	internetArchiveDatePattern    = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
)

type internetArchiveText string

func (value *internetArchiveText) UnmarshalJSON(raw []byte) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		*value = internetArchiveText(text)
		return nil
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		*value = internetArchiveText(strings.Join(values, ", "))
		return nil
	}
	return nil
}

type internetArchiveInt64 int64

func (value *internetArchiveInt64) UnmarshalJSON(raw []byte) error {
	var number int64
	if json.Unmarshal(raw, &number) == nil {
		*value = internetArchiveInt64(number)
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parsed, _ := strconv.ParseInt(text, 10, 64)
		*value = internetArchiveInt64(parsed)
	}
	return nil
}

type InternetArchiveCandidate struct {
	Identifier string               `json:"identifier"`
	Title      internetArchiveText  `json:"title"`
	Creator    internetArchiveText  `json:"creator"`
	Date       internetArchiveText  `json:"date"`
	LicenseURL internetArchiveText  `json:"licenseurl"`
	Downloads  internetArchiveInt64 `json:"downloads"`
}

type internetArchiveMetadata struct {
	Identifier  string              `json:"identifier"`
	Title       internetArchiveText `json:"title"`
	Creator     internetArchiveText `json:"creator"`
	Date        internetArchiveText `json:"date"`
	Year        internetArchiveText `json:"year"`
	Description internetArchiveText `json:"description"`
	LicenseURL  internetArchiveText `json:"licenseurl"`
}

type internetArchiveFile struct {
	Name   string               `json:"name"`
	Title  internetArchiveText  `json:"title"`
	Artist internetArchiveText  `json:"artist"`
	Album  internetArchiveText  `json:"album"`
	Track  internetArchiveText  `json:"track"`
	Format internetArchiveText  `json:"format"`
	Source internetArchiveText  `json:"source"`
	SHA1   string               `json:"sha1"`
	Size   internetArchiveInt64 `json:"size"`
}

type internetArchiveMetadataResponse struct {
	Metadata internetArchiveMetadata `json:"metadata"`
	Files    []internetArchiveFile   `json:"files"`
}

type InternetArchiveImportFile struct {
	Name        string `json:"name"`
	ArchiveName string `json:"archive_name"`
	DownloadURL string `json:"download_url"`
	SHA1        string `json:"sha1,omitempty"`
	Size        int64  `json:"size"`
	Kind        string `json:"kind"`
}

type InternetArchiveImportPlan struct {
	Identifier      string                      `json:"identifier"`
	Title           string                      `json:"title"`
	Creator         string                      `json:"creator"`
	CreatorKey      string                      `json:"creator_key"`
	Description     string                      `json:"description"`
	ReleaseDate     string                      `json:"release_date"`
	ReleaseYear     int                         `json:"release_year"`
	SourceURL       string                      `json:"source_url"`
	LicenseCode     string                      `json:"license_code"`
	LicenseURL      string                      `json:"license_url"`
	AttributionText string                      `json:"attribution_text"`
	Downloads       int64                       `json:"downloads"`
	TotalBytes      int64                       `json:"total_bytes"`
	Files           []InternetArchiveImportFile `json:"files"`
}

type InternetArchiveImportOptions struct {
	Limit        int
	MaxItemBytes int64
	Apply        bool
}

type InternetArchiveImportResult struct {
	Plan            InternetArchiveImportPlan
	Status          string
	ImportSessionID *uuid.UUID
	AlbumID         *uuid.UUID
	SongID          *uuid.UUID
	Err             error
}

type InternetArchiveImporter struct {
	db        *gorm.DB
	service   *Service
	client    *http.Client
	userAgent string
	user      authctx.CurrentUser
}

func NewInternetArchiveImporter(db *gorm.DB, service *Service, client *http.Client, user authctx.CurrentUser, userAgent string) *InternetArchiveImporter {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &InternetArchiveImporter{db: db, service: service, client: client, user: user, userAgent: strings.TrimSpace(userAgent)}
}

func (importer *InternetArchiveImporter) ImportPopular(ctx context.Context, options InternetArchiveImportOptions) ([]InternetArchiveImportResult, error) {
	if importer.db == nil || importer.user.ID == uuid.Nil {
		return nil, fmt.Errorf("database and importing user are required")
	}
	if options.Apply && importer.service == nil {
		return nil, fmt.Errorf("music import service is required when apply is enabled")
	}
	if options.Limit < 1 {
		options.Limit = 20
	}
	if options.Limit > 100 {
		options.Limit = 100
	}
	if options.MaxItemBytes <= 0 {
		options.MaxItemBytes = 512 * 1024 * 1024
	}

	candidates, err := importer.searchPopular(ctx, options.Limit)
	if err != nil {
		return nil, err
	}
	results := make([]InternetArchiveImportResult, 0, len(candidates))
	for _, candidate := range candidates {
		result := InternetArchiveImportResult{Status: "failed", Plan: InternetArchiveImportPlan{
			Identifier: candidate.Identifier, Title: strings.TrimSpace(string(candidate.Title)),
			Creator: strings.TrimSpace(string(candidate.Creator)), Downloads: int64(candidate.Downloads),
		}}
		if imported, err := importer.findExisting(candidate.Identifier); err != nil {
			result.Err = err
			results = append(results, result)
			continue
		} else if imported != nil {
			result.Status = "skipped"
			result.ImportSessionID = imported.ImportSessionID
			result.AlbumID = imported.AlbumID
			result.SongID = imported.SongID
			results = append(results, result)
			continue
		}

		plan, err := importer.buildPlan(ctx, candidate, options.MaxItemBytes)
		result.Plan = plan
		if err != nil {
			result.Err = err
			results = append(results, result)
			continue
		}
		if !options.Apply {
			result.Status = "planned"
			results = append(results, result)
			continue
		}
		result.ImportSessionID, result.AlbumID, result.SongID, result.Err = importer.applyPlan(ctx, plan, options.MaxItemBytes)
		if result.Err == nil {
			result.Status = "queued"
			if result.AlbumID != nil || result.SongID != nil {
				result.Status = "committed"
			}
		}
		results = append(results, result)
	}
	return results, nil
}

func (importer *InternetArchiveImporter) searchPopular(ctx context.Context, limit int) ([]InternetArchiveCandidate, error) {
	licenseURLs := internetArchiveSearchLicenseURLs()
	clauses := make([]string, 0, len(licenseURLs))
	for _, licenseURL := range licenseURLs {
		clauses = append(clauses, `licenseurl:"`+licenseURL+`"`)
	}
	query := `mediatype:audio AND collection:netlabels AND (` + strings.Join(clauses, " OR ") + `)`
	values := url.Values{
		"q":      {query},
		"fl[]":   {"identifier", "title", "creator", "date", "licenseurl", "downloads"},
		"sort[]": {"downloads desc"},
		"rows":   {strconv.Itoa(limit)},
		"page":   {"1"},
		"output": {"json"},
	}
	var response struct {
		Response struct {
			Docs []InternetArchiveCandidate `json:"docs"`
		} `json:"response"`
	}
	if err := importer.getJSON(ctx, "https://archive.org/advancedsearch.php?"+values.Encode(), &response); err != nil {
		return nil, fmt.Errorf("search Internet Archive: %w", err)
	}
	return response.Response.Docs, nil
}

func (importer *InternetArchiveImporter) buildPlan(ctx context.Context, candidate InternetArchiveCandidate, maxBytes int64) (InternetArchiveImportPlan, error) {
	identifier := strings.TrimSpace(candidate.Identifier)
	plan := InternetArchiveImportPlan{Identifier: identifier, Downloads: int64(candidate.Downloads)}
	if identifier == "" {
		return plan, fmt.Errorf("Internet Archive result has no identifier")
	}
	var response internetArchiveMetadataResponse
	if err := importer.getJSON(ctx, "https://archive.org/metadata/"+url.PathEscape(identifier), &response); err != nil {
		return plan, fmt.Errorf("load %s metadata: %w", identifier, err)
	}
	licenseCode, ok := internetArchiveLicenseCode(string(response.Metadata.LicenseURL))
	if !ok {
		return plan, fmt.Errorf("%s has unsupported license %q", identifier, response.Metadata.LicenseURL)
	}
	plan.Title = strings.TrimSpace(string(response.Metadata.Title))
	plan.Creator = strings.TrimSpace(string(response.Metadata.Creator))
	if plan.Title == "" || plan.Creator == "" {
		return plan, fmt.Errorf("%s is missing title or creator", identifier)
	}
	if isInternetArchiveVariousArtist(plan.Creator) {
		return plan, fmt.Errorf("%s uses a various-artists credit that cannot be mapped safely", identifier)
	}
	if taggedAlbum := internetArchiveTaggedAlbum(response.Files); taggedAlbum != "" {
		plan.Title = taggedAlbum
	}
	if !hasInternetArchiveMusicTags(response.Files) {
		return plan, fmt.Errorf("%s has no artist or album tags on original audio", identifier)
	}
	plan.CreatorKey = normalizeInternetArchiveCreator(plan.Creator)
	plan.Description = cleanInternetArchiveDescription(string(response.Metadata.Description))
	plan.ReleaseDate, plan.ReleaseYear = internetArchiveReleaseDate(string(response.Metadata.Date), string(response.Metadata.Year), string(candidate.Date))
	plan.SourceURL = "https://archive.org/details/" + url.PathEscape(identifier)
	plan.LicenseCode = licenseCode
	plan.LicenseURL = strings.TrimSpace(string(response.Metadata.LicenseURL))
	plan.AttributionText = fmt.Sprintf("%s - %s (%s), %s", plan.Creator, plan.Title, plan.LicenseCode, plan.SourceURL)
	plan.Files = selectInternetArchiveFiles(identifier, response.Files)
	if internetArchiveAudioFileCount(plan.Files) == 0 {
		return plan, fmt.Errorf("%s has no supported original audio", identifier)
	}
	for _, file := range plan.Files {
		plan.TotalBytes += file.Size
	}
	if plan.TotalBytes > maxBytes {
		return plan, fmt.Errorf("%s selected files require %d bytes, limit is %d", identifier, plan.TotalBytes, maxBytes)
	}
	return plan, nil
}

func (importer *InternetArchiveImporter) applyPlan(ctx context.Context, plan InternetArchiveImportPlan, maxBytes int64) (*uuid.UUID, *uuid.UUID, *uuid.UUID, error) {
	archive, err := importer.downloadArchive(ctx, plan, maxBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() {
		archive.Close()
		os.Remove(archive.Name())
	}()

	artist, err := importer.resolveArtist(plan)
	if err != nil {
		return nil, nil, nil, err
	}
	session, err := importer.service.CreateAlbumImportSession(importer.user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload, InputMode: AlbumImportInputModeAuto,
		ArtistID: artist.ID.String(), ArtistName: artist.Name,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	record, err := importer.createExternalImport(plan, artist.ID, session.ID)
	if err != nil {
		_, _ = importer.service.CancelAlbumImportSession(importer.user, session.ID)
		return nil, nil, nil, err
	}
	cleanup := func() {
		_, _ = importer.service.CancelAlbumImportSession(importer.user, session.ID)
		_ = importer.db.Unscoped().Delete(&record).Error
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	if _, err := importer.service.UploadAlbumImportArchive(importer.user, session.ID, plan.Identifier+".zip", archive); err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	committed, err := importer.service.CommitAlbumImportSession(importer.user, session.ID, CommitAlbumImportSessionInput{
		Artists: []CommitAlbumImportArtistInput{{
			ArtistID: artist.ID.String(), Roles: []AlbumArtistRoleInput{{Role: "primary"}},
		}},
		ArtistSources: []Source{{Type: "url", URL: plan.SourceURL, Title: "Internet Archive"}},
		Album: AlbumImportAlbumPayload{
			Title: plan.Title, Description: plan.Description, AlbumType: "album",
			ReleaseDate: plan.ReleaseDate, ReleaseYear: plan.ReleaseYear,
		},
		AlbumSources: []Source{{Type: "url", URL: plan.SourceURL, Title: plan.LicenseCode}},
	})
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	if err := importer.syncExternalRecord(record.ID, committed); err != nil {
		return &session.ID, committed.TargetAlbumID, committed.TargetSongID, err
	}
	return &session.ID, committed.TargetAlbumID, committed.TargetSongID, nil
}

func (importer *InternetArchiveImporter) downloadArchive(ctx context.Context, plan InternetArchiveImportPlan, maxBytes int64) (*os.File, error) {
	tmp, err := os.CreateTemp("", "atoman-internet-archive-*.zip")
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	writer := zip.NewWriter(tmp)
	var downloaded int64
	for _, file := range plan.Files {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, file.DownloadURL, nil)
		if err != nil {
			return nil, err
		}
		importer.setUserAgent(request)
		response, err := importer.client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", file.Name, err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("download %s: status %d", file.Name, response.StatusCode)
		}
		header := &zip.FileHeader{Name: file.ArchiveName, Method: zip.Store}
		header.SetModTime(time.Now().UTC())
		entry, err := writer.CreateHeader(header)
		if err != nil {
			response.Body.Close()
			return nil, err
		}
		hash := sha1.New()
		remaining := maxBytes - downloaded
		written, copyErr := io.Copy(io.MultiWriter(entry, hash), io.LimitReader(response.Body, remaining+1))
		response.Body.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("download %s: %w", file.Name, copyErr)
		}
		if written > remaining {
			return nil, fmt.Errorf("%s exceeds the item byte limit", plan.Identifier)
		}
		downloaded += written
		if file.SHA1 != "" && !strings.EqualFold(file.SHA1, hex.EncodeToString(hash.Sum(nil))) {
			return nil, fmt.Errorf("download %s: SHA-1 mismatch", file.Name)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	failed = false
	return tmp, nil
}

func (importer *InternetArchiveImporter) resolveArtist(plan InternetArchiveImportPlan) (model.Artist, error) {
	var previous model.MusicExternalImport
	err := importer.db.Where("provider = ? AND creator_key = ? AND artist_id IS NOT NULL", internetArchiveProvider, plan.CreatorKey).
		Order("created_at ASC").First(&previous).Error
	if err == nil && previous.ArtistID != nil {
		var artist model.Artist
		if loadErr := importer.db.First(&artist, "id = ?", *previous.ArtistID).Error; loadErr == nil {
			return artist, nil
		}
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return model.Artist{}, err
	}
	sources, _ := json.Marshal([]model.MusicSource{{Type: "url", URL: plan.SourceURL, Title: "Internet Archive"}})
	artist := model.Artist{
		Name: plan.Creator, ArtistForm: "person", EntryStatus: artistEntryDraft,
		LifecycleStatus: model.MusicLifecycleDraft, EditStatus: model.MusicEditDevelopment,
		CreatedBy: &importer.user.ID, SourcesJSON: string(sources), BirthDatePrecision: "unknown",
	}
	if err := importer.db.Create(&artist).Error; err != nil {
		return model.Artist{}, err
	}
	return artist, nil
}

func (importer *InternetArchiveImporter) createExternalImport(plan InternetArchiveImportPlan, artistID, sessionID uuid.UUID) (model.MusicExternalImport, error) {
	hashes := map[string]string{}
	for _, file := range plan.Files {
		if file.SHA1 != "" {
			hashes[file.Name] = file.SHA1
		}
	}
	hashesJSON, _ := json.Marshal(hashes)
	metadataJSON, _ := json.Marshal(plan)
	record := model.MusicExternalImport{
		Provider: internetArchiveProvider, ExternalID: plan.Identifier, CreatorKey: plan.CreatorKey,
		SourceURL: plan.SourceURL, LicenseCode: plan.LicenseCode, LicenseURL: plan.LicenseURL,
		AttributionText: plan.AttributionText, LicenseObservedAt: time.Now().UTC(), Popularity: plan.Downloads,
		FileHashesJSON: string(hashesJSON), MetadataJSON: string(metadataJSON),
		ImportSessionID: &sessionID, ArtistID: &artistID,
	}
	return record, importer.db.Create(&record).Error
}

func (importer *InternetArchiveImporter) syncExternalRecord(recordID uuid.UUID, session model.AlbumImportSession) error {
	return importer.db.Model(&model.MusicExternalImport{}).Where("id = ?", recordID).
		Updates(map[string]any{"album_id": session.TargetAlbumID, "song_id": session.TargetSongID}).Error
}

func (importer *InternetArchiveImporter) findExisting(identifier string) (*model.MusicExternalImport, error) {
	if !importer.db.Migrator().HasTable(&model.MusicExternalImport{}) {
		return nil, nil
	}
	var record model.MusicExternalImport
	err := importer.db.Where("provider = ? AND external_id = ?", internetArchiveProvider, identifier).First(&record).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &record, err
}

func (importer *InternetArchiveImporter) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	importer.setUserAgent(request)
	response, err := importer.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16*1024*1024)).Decode(target)
}

func (importer *InternetArchiveImporter) setUserAgent(request *http.Request) {
	if importer.userAgent != "" {
		request.Header.Set("User-Agent", importer.userAgent)
	}
}

func internetArchiveSearchLicenseURLs() []string {
	versions := []string{"1.0", "2.0", "2.5", "3.0", "4.0"}
	licenses := make([]string, 0, 24)
	for _, scheme := range []string{"http", "https"} {
		for _, code := range []string{"by", "by-sa"} {
			for _, version := range versions {
				licenses = append(licenses, fmt.Sprintf("%s://creativecommons.org/licenses/%s/%s/", scheme, code, version))
			}
		}
		licenses = append(licenses,
			scheme+"://creativecommons.org/publicdomain/zero/1.0/",
			scheme+"://creativecommons.org/publicdomain/mark/1.0/",
		)
	}
	return licenses
}

func internetArchiveLicenseCode(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Host, "creativecommons.org") {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 3 && parts[0] == "licenses" && (parts[1] == "by" || parts[1] == "by-sa") {
		if _, err := strconv.ParseFloat(parts[2], 64); err == nil {
			return "CC " + strings.ToUpper(parts[1]) + " " + parts[2], true
		}
	}
	if len(parts) == 3 && parts[0] == "publicdomain" && parts[2] == "1.0" {
		switch parts[1] {
		case "zero":
			return "CC0 1.0", true
		case "mark":
			return "PDM 1.0", true
		}
	}
	return "", false
}

func hasInternetArchiveMusicTags(files []internetArchiveFile) bool {
	for _, file := range files {
		if !strings.EqualFold(string(file.Source), "original") || !isInternetArchiveAudioExtension(strings.ToLower(path.Ext(file.Name))) {
			continue
		}
		if strings.TrimSpace(string(file.Artist)) != "" || strings.TrimSpace(string(file.Album)) != "" {
			return true
		}
	}
	return false
}

func internetArchiveTaggedAlbum(files []internetArchiveFile) string {
	album := ""
	for _, file := range files {
		if !strings.EqualFold(string(file.Source), "original") || !isInternetArchiveAudioExtension(strings.ToLower(path.Ext(file.Name))) {
			continue
		}
		value := strings.TrimSpace(string(file.Album))
		if value == "" {
			continue
		}
		if album != "" && !strings.EqualFold(album, value) {
			return ""
		}
		album = value
	}
	return album
}

func isInternetArchiveVariousArtist(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	return value == "va" || value == "v.a." || value == "various" || value == "various artists"
}

func selectInternetArchiveFiles(identifier string, files []internetArchiveFile) []InternetArchiveImportFile {
	audio := make([]internetArchiveFile, 0)
	cues := make([]internetArchiveFile, 0)
	images := make([]internetArchiveFile, 0)
	for _, file := range files {
		name, ok := safeInternetArchivePath(file.Name)
		if !ok {
			continue
		}
		file.Name = name
		ext := strings.ToLower(path.Ext(name))
		if strings.EqualFold(string(file.Source), "original") && isInternetArchiveAudioExtension(ext) {
			audio = append(audio, file)
			continue
		}
		if strings.EqualFold(string(file.Source), "original") && ext == ".cue" {
			cues = append(cues, file)
			continue
		}
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
			images = append(images, file)
		}
	}
	if len(cues) == 0 {
		audio = preferredInternetArchiveAudio(audio)
	}
	sort.SliceStable(audio, func(left, right int) bool {
		leftTrack := internetArchiveTrackNumber(string(audio[left].Track))
		rightTrack := internetArchiveTrackNumber(string(audio[right].Track))
		if leftTrack != rightTrack {
			return leftTrack < rightTrack
		}
		return strings.ToLower(audio[left].Name) < strings.ToLower(audio[right].Name)
	})
	selected := make([]InternetArchiveImportFile, 0, len(audio)+len(cues)+1)
	for _, file := range append(audio, cues...) {
		kind := "audio"
		if strings.EqualFold(path.Ext(file.Name), ".cue") {
			kind = "cue"
		}
		selected = append(selected, internetArchiveImportFile(identifier, file, file.Name, kind))
	}
	if cover, ok := preferredInternetArchiveCover(images); ok {
		ext := strings.ToLower(path.Ext(cover.Name))
		selected = append(selected, internetArchiveImportFile(identifier, cover, "cover"+ext, "cover"))
	} else if len(audio) > 0 {
		selected = append(selected, InternetArchiveImportFile{
			Name: "Internet Archive thumbnail", ArchiveName: "cover.jpg", Kind: "cover",
			DownloadURL: "https://archive.org/services/img/" + url.PathEscape(identifier),
		})
	}
	return selected
}

func preferredInternetArchiveAudio(files []internetArchiveFile) []internetArchiveFile {
	rank := map[string]int{".mp3": 1, ".m4a": 2, ".ogg": 3, ".oga": 3, ".opus": 4, ".flac": 5, ".wav": 6, ".aac": 7}
	byStem := map[string]internetArchiveFile{}
	for _, file := range files {
		ext := strings.ToLower(path.Ext(file.Name))
		stem := strings.ToLower(strings.TrimSuffix(file.Name, path.Ext(file.Name)))
		current, exists := byStem[stem]
		if !exists || rank[ext] < rank[strings.ToLower(path.Ext(current.Name))] {
			byStem[stem] = file
		}
	}
	selected := make([]internetArchiveFile, 0, len(byStem))
	for _, file := range byStem {
		selected = append(selected, file)
	}
	return selected
}

func preferredInternetArchiveCover(files []internetArchiveFile) (internetArchiveFile, bool) {
	if len(files) == 0 {
		return internetArchiveFile{}, false
	}
	sort.SliceStable(files, func(left, right int) bool {
		return internetArchiveCoverRank(files[left]) < internetArchiveCoverRank(files[right])
	})
	return files[0], true
}

func internetArchiveCoverRank(file internetArchiveFile) int {
	name := strings.ToLower(path.Base(file.Name))
	rank := 20
	if strings.Contains(name, "cover") || strings.Contains(name, "front") || strings.Contains(name, "folder") {
		rank = 0
	} else if strings.Contains(name, "thumb") {
		rank = 10
	}
	if !strings.EqualFold(string(file.Source), "original") {
		rank++
	}
	return rank
}

func internetArchiveImportFile(identifier string, file internetArchiveFile, archiveName, kind string) InternetArchiveImportFile {
	return InternetArchiveImportFile{
		Name: file.Name, ArchiveName: archiveName, DownloadURL: internetArchiveDownloadURL(identifier, file.Name),
		SHA1: strings.TrimSpace(file.SHA1), Size: int64(file.Size), Kind: kind,
	}
}

func internetArchiveDownloadURL(identifier, name string) string {
	segments := strings.Split(strings.ReplaceAll(name, "\\", "/"), "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	return "https://archive.org/download/" + url.PathEscape(identifier) + "/" + strings.Join(segments, "/")
}

func safeInternetArchivePath(name string) (string, bool) {
	cleaned := path.Clean(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if cleaned == "." || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func isInternetArchiveAudioExtension(extension string) bool {
	switch extension {
	case ".mp3", ".flac", ".wav", ".m4a", ".aac", ".ogg", ".oga", ".opus":
		return true
	default:
		return false
	}
}

func internetArchiveAudioFileCount(files []InternetArchiveImportFile) int {
	count := 0
	for _, file := range files {
		if file.Kind == "audio" {
			count++
		}
	}
	return count
}

func internetArchiveTrackNumber(raw string) int {
	value := strings.TrimSpace(strings.Split(raw, "/")[0])
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 1 << 30
	}
	return number
}

func internetArchiveReleaseDate(values ...string) (string, int) {
	for _, raw := range values {
		if match := internetArchiveDatePattern.FindStringSubmatch(strings.TrimSpace(raw)); len(match) == 4 {
			year, _ := strconv.Atoi(match[1])
			return strings.Join(match[1:], "-"), year
		}
		if year, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && year >= 1000 && year <= 9999 {
			return fmt.Sprintf("%04d/--/--", year), year
		}
	}
	return "----/--/--", 0
}

func cleanInternetArchiveDescription(raw string) string {
	cleaned := html.UnescapeString(internetArchiveHTMLTagPattern.ReplaceAllString(raw, " "))
	cleaned = strings.TrimSpace(internetArchiveSpacePattern.ReplaceAllString(cleaned, " "))
	runes := []rune(cleaned)
	if len(runes) > 4000 {
		cleaned = string(runes[:4000])
	}
	return cleaned
}

func normalizeInternetArchiveCreator(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(raw)), " "))
}
