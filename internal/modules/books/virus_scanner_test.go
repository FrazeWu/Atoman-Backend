package books

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestClamAVBookVirusScannerWithInstalledEngine(t *testing.T) {
	binary, err := exec.LookPath("clamscan")
	if err != nil {
		t.Skip("clamscan is not installed")
	}
	rawScanner, err := NewClamAVBookVirusScanner(binary)
	require.NoError(t, err)
	scanner := rawScanner.(*clamAVBookVirusScanner)
	require.NoError(t, scanner.Scan(context.Background(), bytes.NewReader([]byte("clean book evidence"))))

	eicar := []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`)
	err = scanner.Scan(context.Background(), bytes.NewReader(eicar))
	require.ErrorIs(t, err, ErrBookVirusDetected)
}

func TestProcessBookAssetsUsesConfiguredVirusScanner(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	importID, assetID := uuid.New(), uuid.New()
	key := "books/private/scanned.txt"
	body := []byte("scanner input")
	require.NoError(t, db.Create(&model.UserBookImport{Base: model.Base{ID: importID}, UserID: owner.ID, Title: "Scanned", OriginalFilename: "scanned.txt", Format: "txt", ContentType: "text/plain", SizeBytes: int64(len(body)), ObjectKey: key, Status: model.BookImportStatusScanning, MetadataJSON: "{}", CompletedPartsJSON: "[]"}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{Base: model.Base{ID: assetID}, ImportID: importID, UserID: owner.ID, OriginalFilename: "scanned.txt", ContentType: "text/plain", Format: "txt", SizeBytes: int64(len(body)), ObjectKey: key, ProcessingStatus: model.BookAssetStatusScanning}).Error)
	scanner := &recordingBookVirusScanner{}
	service := NewService(db).WithBookUploadStore(&fakeBookUploadStore{objects: map[string][]byte{key: body}}).WithVirusScanner(scanner)
	_, err := service.ProcessBookAssets(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(len(body)), scanner.bytesRead)
	var asset model.UserBookAsset
	require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
	require.Equal(t, "clean", asset.ScanStatus)
}

type recordingBookVirusScanner struct{ bytesRead int64 }

func (scanner *recordingBookVirusScanner) Scan(_ context.Context, reader io.Reader) error {
	count, err := io.Copy(io.Discard, reader)
	scanner.bytesRead += count
	return err
}
