package papers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/papersassets"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MaxImportZIPBytes = 5 << 20
const MaxImportEntries = 512

var archiveSlots = make(chan struct{}, 2)
var safeRootTex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,250}\.tex$`)

type importFile struct{ Path, Kind, Content string }

func importError(message string) error { return apperr.BadRequest("papers.invalid_archive", message) }

// A generated entrypoint exists ONLY in the immutable compile snapshot,
// never in the project or its export. Keep the fixed no-shell-escape TeX
// invocation and output filename. A non-main.tex root entry is allowed
// only for a strictly portable TeX-safe basename.
func addCompileEntrypoint(snap *papersassets.Snapshot, main string) error {
	if main == "main.tex" {
		return nil
	}
	if !safeRootTex.MatchString(main) {
		return apperr.Unprocessable("papers.invalid_main_file", "The main TeX file must be a portable root-level .tex filename")
	}
	for _, f := range snap.Files {
		if strings.EqualFold(f.Path, "main.tex") {
			return apperr.Unprocessable("papers.invalid_main_file", "The generated compile entrypoint would conflict with main.tex")
		}
	}
	snap.Files = append(snap.Files, papersassets.SourceFile{Path: "main.tex", Content: "\\input{" + main + "}\n"})
	return nil
}

func archiveMetadata(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == "__MACOSX" || part == ".DS_Store" || part == "Thumbs.db" || part == ".git" {
			return true
		}
	}
	return false
}

// Read uploads without extracting anything onto the host filesystem.
// Reject symlinks, encrypted or invalid streams, duplicate/case-colliding
// names and file-directory conflicts; bound actual decompressed bytes,
// not only ZIP central-directory declarations.
func readImportArchive(raw []byte, requestedMain string, confirmation ...string) ([]importFile, string, error) {
	if len(raw) == 0 || len(raw) > MaxImportZIPBytes {
		return nil, "", importError("ZIP must be no larger than 5 MiB")
	}
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(r.File) == 0 || len(r.File) > MaxImportEntries {
		return nil, "", importError("Invalid or oversized ZIP archive")
	}
	// First validate the entire entry table, including excluded entries.
	// An exclusion acknowledgement is not a waiver of archive safety checks.
	var supported []*zip.File
	var excluded []string
	seen := map[string]bool{}
	dirs := map[string]bool{}
	for _, entry := range r.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
			return nil, "", importError("ZIP contains an unsafe path")
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, "", importError("ZIP contains a symbolic link")
		}
		if archiveMetadata(name) {
			continue
		}
		key := strings.ToLower(name)
		for parent := path.Dir(key); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return nil, "", importError("ZIP contains a file and directory conflict")
			}
			dirs[parent] = true
		}
		if entry.FileInfo().IsDir() {
			if seen[key] {
				return nil, "", importError("ZIP contains a file and directory conflict")
			}
			dirs[key] = true
			continue
		}
		if !entry.Mode().IsRegular() || seen[key] || dirs[key] || entry.Flags&1 != 0 {
			return nil, "", importError("ZIP contains duplicate, encrypted or non-regular entries")
		}
		seen[key] = true
		if papersassets.SourceKind(name) == "" || !papersassets.ValidSourcePath(name) {
			excluded = append(excluded, name)
			continue
		}
		supported = append(supported, entry)
	}
	// A user can approve the complete list of unsupported regular files for
	// this exact archive. Never silently drop a changed archive's entries.
	if len(excluded) > 0 {
		sort.Strings(excluded)
		digest := papersassets.HashBytes(raw)
		if len(confirmation) != 1 || confirmation[0] != digest {
			return nil, "", apperr.New(http.StatusConflict, "papers.archive_exclusion_required",
				"Unsupported project files require confirmation before import",
				map[string]any{"files": excluded, "confirmation": digest})
		}
	}
	if len(supported) == 0 {
		return nil, "", importError("ZIP contains no supported source files")
	}
	var files []importFile
	total := 0
	for _, entry := range supported {
		name := entry.Name
		kind := papersassets.SourceKind(name)
		if len(files) >= 64 || entry.UncompressedSize64 > uint64(papersassets.MaxSnapshotBytes-total) {
			return nil, "", importError("Project exceeds the 64-file / 4 MiB source limit")
		}
		limit := papersassets.MaxSnapshotBytes - total
		if kind == "text" && limit > maxTextBytes {
			limit = maxTextBytes
		}
		if kind == "binary" {
			limit = limit / 4 * 3
		}
		if entry.UncompressedSize64 > uint64(limit) {
			return nil, "", importError("A file exceeds the project source limit")
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, "", importError("ZIP entry could not be opened")
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(content) > limit || uint64(len(content)) != entry.UncompressedSize64 {
			return nil, "", importError("ZIP entry failed bounded decompression")
		}
		value := string(content)
		if kind == "text" {
			if validateText(value) != nil {
				return nil, "", importError("Text sources must use UTF-8: " + name)
			}
		} else {
			if len(content) == 0 {
				return nil, "", importError("Empty binary resource: " + name)
			}
			value = base64.StdEncoding.EncodeToString(content)
		}
		total += len(value)
		if total > papersassets.MaxSnapshotBytes {
			return nil, "", importError("Project exceeds the 4 MiB encoded source limit")
		}
		files = append(files, importFile{name, kind, value})
	}
	if len(files) == 0 {
		return nil, "", importError("ZIP contains no supported source files")
	}
	// Remove an outer archive folder only when it wraps every source file.
	// All project-relative paths below that folder are preserved.
	for {
		first, _, ok := strings.Cut(files[0].Path, "/")
		if !ok {
			break
		}
		prefix := first + "/"
		all := true
		for _, f := range files {
			if !strings.HasPrefix(f.Path, prefix) {
				all = false
				break
			}
		}
		if !all {
			break
		}
		for i := range files {
			files[i].Path = strings.TrimPrefix(files[i].Path, prefix)
		}
	}
	main := ""
	var candidates, tex []string
	for _, f := range files {
		if f.Kind != "text" || !safeRootTex.MatchString(f.Path) {
			continue
		}
		tex = append(tex, f.Path)
		if f.Path == "main.tex" {
			main = f.Path
		}
		if strings.Contains(f.Content, "\\documentclass") {
			candidates = append(candidates, f.Path)
		}
	}
	if requestedMain != "" {
		main = ""
		for _, name := range tex {
			if name == requestedMain {
				main = name
				break
			}
		}
		if main == "" {
			return nil, "", importError("The selected main TeX file was not found at the project root")
		}
	} else if main == "" {
		if len(candidates) == 1 {
			main = candidates[0]
		} else if len(candidates) == 0 && len(tex) == 1 {
			main = tex[0]
		}
	}
	if main == "" {
		return nil, "", importError("Cannot identify the main TeX file; specify its root-level .tex filename")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	// Validate the exact snapshot format before storing a single DB row.
	snap := papersassets.Snapshot{Version: 1, ProjectID: uuid.New(), Revision: 1}
	for _, f := range files {
		kind := ""
		if f.Kind == "binary" {
			kind = "binary"
		}
		snap.Files = append(snap.Files, papersassets.SourceFile{Path: f.Path, Content: f.Content, Kind: kind})
	}
	if err := addCompileEntrypoint(&snap, main); err != nil {
		return nil, "", err
	}
	if err := snap.Validate(); err != nil {
		return nil, "", importError("Project cannot fit the trusted compiler input format")
	}
	encoded, err := json.Marshal(snap)
	if err != nil || len(encoded) > papersassets.MaxSnapshotBytes+65536 {
		return nil, "", importError("Encoded project exceeds the compile source limit")
	}
	return files, main, nil
}

func (s *Service) ImportProject(ctx context.Context, user authctx.CurrentUser, name, main string, raw []byte, confirmation ...string) (model.PaperProject, error) {
	var p model.PaperProject
	if err := s.RequireAccess(user); err != nil {
		return p, err
	}
	clean, err := validatedName(name)
	if err != nil {
		return p, err
	}
	files, entry, err := readImportArchive(raw, main, confirmation...)
	if err != nil {
		return p, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockInvitationEligibilityWithAccountLock(tx, user.ID, "UPDATE"); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.PaperProject{}).Where("owner_id=?", user.ID).Count(&count).Error; err != nil {
			return err
		}
		if count >= maxProjects {
			return apperr.Conflict("papers.project_limit", "Project limit reached")
		}
		p = model.PaperProject{OwnerID: user.ID, Name: clean, HeadRevision: 1}
		if err := tx.Create(&p).Error; err != nil {
			return err
		}
		for _, src := range files {
			file := model.PaperFile{ProjectID: p.ID, Path: src.Path, Kind: src.Kind, Content: src.Content, SHA256: hashText(src.Content), Revision: 1}
			if err := tx.Create(&file).Error; err != nil {
				return err
			}
			if err := tx.Create(&[]model.PaperFileVersion{fileVersion(file, user.ID)}).Error; err != nil {
				return err
			}
			if src.Path == entry {
				p.MainFileID = file.ID
			}
		}
		if p.MainFileID == uuid.Nil {
			return errors.New("import main file missing")
		}
		if err := tx.Model(&p).Update("main_file_id", p.MainFileID).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.project.import", EntityType: "paper_project", EntityID: &p.ID})
	})
	return p, err
}

// Export a consistent revision under the same project row lock used by
// source writes. The caller supplies a private bounded output spool; no
// guest path is ever interpreted as a host path.
func (s *Service) ExportProject(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, out io.Writer) (string, error) {
	if err := s.RequireAccess(user); err != nil {
		return "", err
	}
	var name string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p model.PaperProject
		err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND owner_id=?", id, user.ID).Take(&p).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("papers.project_not_found", "Project not found")
		}
		if err != nil {
			return err
		}
		if err := lockInvitationEligibility(tx, user.ID); err != nil {
			return err
		}
		name = p.Name
		rows, err := tx.Model(&model.PaperFile{}).Where("project_id=?", id).Order("path ASC").Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		z := zip.NewWriter(out)
		count, total := 0, 0
		seen := map[string]bool{}
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var file model.PaperFile
			if err := tx.ScanRows(rows, &file); err != nil {
				return err
			}
			count++
			total += len(file.Content)
			if count > maxFiles || total > maxFiles*maxTextBytes+papersassets.MaxSnapshotBytes {
				return importError("Project exceeds the export size limit")
			}
			if seen[file.Path] || hashText(file.Content) != file.SHA256 {
				return fmt.Errorf("project file integrity failure")
			}
			seen[file.Path] = true
			var data []byte
			if file.Kind == "text" {
				if _, err := validatedPath(file.Path); err != nil {
					return err
				}
				if err := validateText(file.Content); err != nil {
					return err
				}
				data = []byte(file.Content)
			} else if file.Kind == "binary" {
				data, err = (papersassets.SourceFile{Path: file.Path, Content: file.Content, Kind: "binary"}).Bytes()
				if err != nil {
					return err
				}
			} else {
				return fmt.Errorf("unknown file kind")
			}
			w, err := z.Create(file.Path)
			if err != nil {
				return err
			}
			if _, err = w.Write(data); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return z.Close()
	})
	return name, err
}
