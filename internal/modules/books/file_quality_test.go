package books

import (
	"archive/zip"
	"atoman/internal/model"
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeQualityTempFile(t *testing.T, data []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "quality-*")
	require.NoError(t, err)
	_, err = f.Write(data)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func testQualityEPUB(t *testing.T, navigation bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	mimetype, err := archive.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	require.NoError(t, err)
	_, err = mimetype.Write([]byte("application/epub+zip"))
	require.NoError(t, err)
	container, err := archive.Create("META-INF/container.xml")
	require.NoError(t, err)
	_, err = container.Write([]byte(`<container><rootfiles><rootfile full-path="OEBPS/package.opf"/></rootfiles></container>`))
	require.NoError(t, err)
	properties := ""
	if navigation {
		properties = ` properties="nav"`
	}
	opf, err := archive.Create("OEBPS/package.opf")
	require.NoError(t, err)
	_, err = opf.Write([]byte(`<package><manifest><item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/><item id="c2" href="chapter2.xhtml" media-type="application/xhtml+xml"/><item id="nav" href="nav.xhtml"` + properties + `/></manifest><spine><itemref idref="c1"/><itemref idref="c2"/></spine></package>`))
	require.NoError(t, err)
	for _, name := range []string{"chapter1.xhtml", "chapter2.xhtml", "nav.xhtml"} {
		w, err := archive.Create("OEBPS/" + name)
		require.NoError(t, err)
		_, err = w.Write([]byte("<html><body><p>内容</p></body></html>"))
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	return buffer.Bytes()
}

func TestBookFileQualityObservationsAreMeasuredNotGuessed(t *testing.T) {
	data := testQualityEPUB(t, true)
	f := writeQualityTempFile(t, data)
	meta, err := validateBookEPUB(f, int64(len(data)))
	require.NoError(t, err)
	report := inspectBookFileQuality(f, "epub", int64(len(data)), meta)
	require.Equal(t, 1, report.Version)
	require.EqualValues(t, len(data), report.Bytes)
	require.NotNil(t, report.EPUBSpineItems)
	require.Equal(t, 2, *report.EPUBSpineItems)
	require.NotNil(t, report.EPUBHasNavigation)
	require.True(t, *report.EPUBHasNavigation)
	require.Nil(t, report.ComicImagePages)
	require.NotEmpty(t, report.Limitations)
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "score")

	data = testQualityEPUB(t, false)
	f = writeQualityTempFile(t, data)
	meta, err = validateBookEPUB(f, int64(len(data)))
	require.NoError(t, err)
	report = inspectBookFileQuality(f, "epub", int64(len(data)), meta)
	require.NotNil(t, report.EPUBHasNavigation)
	require.False(t, *report.EPUBHasNavigation)

	pdf := inspectBookFileQuality(writeQualityTempFile(t, []byte("%PDF-1.5")), "pdf", 8, nil)
	require.Nil(t, pdf.EPUBSpineItems)
	require.Nil(t, pdf.TXTCharacters)
	require.True(t, strings.Contains(pdf.Limitations[0], "未检测 PDF 页数"))

	text := []byte("测试A")
	txt := inspectBookFileQuality(writeQualityTempFile(t, text), "txt", int64(len(text)), nil)
	require.NotNil(t, txt.TXTValidUTF8)
	require.True(t, *txt.TXTValidUTF8)
	require.NotNil(t, txt.TXTCharacters)
	require.EqualValues(t, 3, *txt.TXTCharacters)
	bad := []byte{0xff, 0xfe}
	txt = inspectBookFileQuality(writeQualityTempFile(t, bad), "txt", 2, nil)
	require.NotNil(t, txt.TXTValidUTF8)
	require.False(t, *txt.TXTValidUTF8)
	require.Nil(t, txt.TXTCharacters)

	comic := inspectBookFileQuality(writeQualityTempFile(t, []byte("unused")), "cbz", 6, map[string]any{"image_count": 42})
	require.NotNil(t, comic.ComicImagePages)
	require.Equal(t, 42, *comic.ComicImagePages)
}

func TestBookFileQualityVisibleOnlyWithCorrespondingReadableFile(t *testing.T) {
	s, user, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	edition, err := s.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{Language: "zh", Publisher: "测试出版社", EditionStatement: "试刊"})
	require.NoError(t, err)
	editionID := uuid.MustParse(edition.ID)
	privateAsset, err := s.CreateBookResource(ctx, user, workID, CreateBookResourceInput{EditionID: &editionID, Language: "zh", Format: "txt"})
	require.NoError(t, err)
	privateUpload := queueBookTestFile(t, s, user, store, privateAsset, "private.txt", "text/plain", []byte("Only owner can read"))
	require.NoError(t, s.db.Model(&model.BookUploadSession{}).Where("id = ?", privateUpload.ID).Update("public_distribution_confirmed", false).Error)
	_, err = s.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	ownerView, err := s.ListBookEditionFilesForViewer(ctx, editionID, user.ID)
	require.NoError(t, err)
	require.Len(t, ownerView, 1)
	require.NotNil(t, ownerView[0].Quality)
	otherView, err := s.ListBookEditionFilesForViewer(ctx, editionID, uuid.New())
	require.NoError(t, err)
	require.Empty(t, otherView, "private file names and diagnostics must not leak")
	anonymous, err := s.ListBookEditionFilesForViewer(ctx, editionID, uuid.Nil)
	require.NoError(t, err)
	require.Empty(t, anonymous)
}
