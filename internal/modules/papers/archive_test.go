package papers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/papersassets"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type zipFixtureEntry struct {
	path    string
	content []byte
	mode    os.FileMode
}

func archiveFixture(t *testing.T, entries ...zipFixtureEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.path, Method: zip.Deflate}
		if entry.mode != 0 {
			h.SetMode(entry.mode)
		}
		f, err := w.CreateHeader(h)
		require.NoError(t, err)
		_, err = f.Write(entry.content)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}
func TestImportZIPRejectsTraversalLinksDuplicatesAndOversize(t *testing.T) {
	tex := zipFixtureEntry{"main.tex", []byte("\\documentclass{article}"), 0}
	for _, bad := range []zipFixtureEntry{
		{"../bad.tex", []byte("bad"), 0},
		{"/bad.tex", []byte("bad"), 0},
		{"a/../bad.tex", []byte("bad"), 0},
		{"a\\bad.tex", []byte("bad"), 0},
		{"main.tex", []byte("duplicate"), 0},
		{"MAIN.TEX", []byte("collision"), 0},
		{"main.tex/child.tex", []byte("file-dir-conflict"), 0},
		{"symlink.tex", []byte("/etc/passwd"), os.ModeSymlink | 0777},
		{"bad.sh", []byte("unsupported"), 0},
		{"bad.tex", []byte{0xff, 0x00}, 0},
		{"oversize.tex", bytes.Repeat([]byte("x"), maxTextBytes+1), 0},
	} {
		_, _, err := readImportArchive(archiveFixture(t, tex, bad), "")
		require.Error(t, err, bad.path)
	}
	many := []zipFixtureEntry{tex}
	for i := 0; i < 64; i++ {
		many = append(many, zipFixtureEntry{uuid.NewString() + ".tex", []byte("x"), 0})
	}
	_, _, err := readImportArchive(archiveFixture(t, many...), "")
	require.Error(t, err)
	_, _, err = readImportArchive(archiveFixture(t, tex, zipFixtureEntry{"figures/x.png", bytes.Repeat([]byte("x"), papersassets.MaxSnapshotBytes), 0}), "")
	require.Error(t, err)
}
func TestImportZIPDetectsMainAndPreservesBinary(t *testing.T) {
	raw := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 255, 128}
	files, main, err := readImportArchive(archiveFixture(t,
		zipFixtureEntry{"Research/paper.tex", []byte("\\documentclass{article}\\begin{document}Hello\\end{document}"), 0},
		zipFixtureEntry{"Research/sections/intro.tex", []byte("Hello"), 0},
		zipFixtureEntry{"Research/figures/plot 1.png", raw, 0},
		zipFixtureEntry{"Research/references.bib", []byte("@article{hello,title={Test}}"), 0},
		zipFixtureEntry{"Research/.DS_Store", []byte("metadata"), 0},
	), "")
	require.NoError(t, err)
	require.Equal(t, "paper.tex", main)
	require.Len(t, files, 4)
	var image importFile
	for _, f := range files {
		if f.Kind == "binary" {
			image = f
		}
	}
	require.Equal(t, "figures/plot 1.png", image.Path)
	decoded, err := base64.StdEncoding.DecodeString(image.Content)
	require.NoError(t, err)
	require.Equal(t, raw, decoded)
}
func TestProjectArchiveHTTPRoundTripAndPermissions(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	rawImage := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 255, 128}
	sources := map[string][]byte{
		"paper.tex":          []byte("\\documentclass{article}\\begin{document}Hello\\end{document}"),
		"sections/intro.tex": []byte("Section content"),
		"figures/plot.png":   rawImage,
		"refs.bib":           []byte("@article{hello,title={Test}}"),
	}
	var entries []zipFixtureEntry
	for p, content := range sources {
		entries = append(entries, zipFixtureEntry{"Folder/" + p, content, 0})
	}
	packed := archiveFixture(t, entries...)
	base := "/api/v1/papers/projects"
	denied := f.request("POST", base+"/import?name=Denied", string(packed), "")
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	imported := f.request("POST", base+"/import?name=Imported", string(packed), f.ownerToken)
	require.Equal(t, http.StatusCreated, imported.Code, imported.Body.String())
	var p model.PaperProject
	require.NoError(t, f.db.Where("owner_id=? AND name=?", f.owner.UUID, "Imported").First(&p).Error)
	require.NotEqual(t, uuid.Nil, p.MainFileID)
	require.EqualValues(t, 1, p.HeadRevision)
	user := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	files, err := f.svc.ListFiles(user, p.ID)
	require.NoError(t, err)
	require.Len(t, files, 4)
	main, err := f.svc.GetFile(user, p.ID, p.MainFileID)
	require.NoError(t, err)
	require.Equal(t, "paper.tex", main.Path)
	for _, file := range files {
		if file.Kind == "binary" {
			_, err := f.svc.SaveFile(user, p.ID, file.ID, "overwrite", 1)
			require.Equal(t, http.StatusBadRequest, apperr.FromError(err).HTTPStatus)
		}
	}
	snap, err := f.svc.ownedSnapshot(context.Background(), user, p.ID, 1)
	require.NoError(t, err)
	require.Len(t, snap.Files, 5)
	for _, file := range snap.Files {
		switch file.Path {
		case "main.tex":
			require.Equal(t, "\\input{paper.tex}\n", file.Content)
		case "figures/plot.png":
			data, err := file.Bytes()
			require.NoError(t, err)
			require.Equal(t, rawImage, data)
		}
	}
	exportPath := base + "/" + p.ID.String() + "/export"
	require.Equal(t, http.StatusNotFound, f.request("GET", exportPath, "", f.otherToken).Code)
	require.Equal(t, http.StatusUnauthorized, f.request("GET", exportPath, "", "").Code)
	exported := f.request("GET", exportPath, "", f.ownerToken)
	require.Equal(t, http.StatusOK, exported.Code)
	require.Contains(t, exported.Header().Get("Content-Type"), "application/zip")
	require.Contains(t, exported.Header().Get("Cache-Control"), "no-store")
	require.Contains(t, exported.Header().Get("Content-Disposition"), "attachment")
	z, err := zip.NewReader(bytes.NewReader(exported.Body.Bytes()), int64(exported.Body.Len()))
	require.NoError(t, err)
	require.Len(t, z.File, len(sources))
	for _, file := range z.File {
		r, err := file.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		require.Equal(t, sources[file.Name], data, file.Name)
	}
	bad := archiveFixture(t, zipFixtureEntry{"main.tex", []byte("text"), 0}, zipFixtureEntry{"../x.tex", []byte("invalid"), 0})
	require.Equal(t, http.StatusBadRequest, f.request("POST", base+"/import?name=Invalid", string(bad), f.ownerToken).Code)
	var count int64
	require.NoError(t, f.db.Model(&model.PaperProject{}).Where("owner_id=? AND name=?", f.owner.UUID, "Invalid").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", f.owner.UUID).Update("status", "revoked").Error)
	require.Equal(t, http.StatusForbidden, f.request("GET", exportPath, "", f.ownerToken).Code)
}
