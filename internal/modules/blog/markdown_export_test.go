package blog

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type exportAssetReaderFunc func(context.Context, string, int64) (io.ReadCloser, error)

func (f exportAssetReaderFunc) ReadExportAsset(ctx context.Context, key string, size int64) (io.ReadCloser, error) {
	return f(ctx, key, size)
}

func TestBuildBlogMarkdownExportRejectsUnavailableOrInvalidAssets(t *testing.T) {
	content := BlogContent{ID: uuid.New(), Title: "Export", Content: "body", Visibility: "private", UpdatedAt: time.Now().UTC()}
	asset := blogExportAsset{URL: "https://assets.example.test/image.png", Key: "blog/image.png", ContentType: "image/png", Size: 4, ArchivePath: "assets/image.png"}
	if _, err := buildBlogMarkdownExport(context.Background(), content, []blogExportAsset{asset}, nil); err == nil {
		t.Fatal("expected unavailable storage error")
	}
	shortReader := exportAssetReaderFunc(func(context.Context, string, int64) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("abc")), nil
	})
	if _, err := buildBlogMarkdownExport(context.Background(), content, []blogExportAsset{asset}, shortReader); err == nil {
		t.Fatal("expected recorded-size mismatch")
	}
	readErr := errors.New("storage read failed")
	failingReader := exportAssetReaderFunc(func(context.Context, string, int64) (io.ReadCloser, error) {
		return nil, readErr
	})
	if _, err := buildBlogMarkdownExport(context.Background(), content, []blogExportAsset{asset}, failingReader); !errors.Is(err, readErr) {
		t.Fatalf("expected reader error, got %v", err)
	}
}

func TestBuildBlogMarkdownExportWritesCurrentContentAndFrontMatter(t *testing.T) {
	assetReader := exportAssetReaderFunc(func(_ context.Context, key string, _ int64) (io.ReadCloser, error) {
		if key != "blog/cover.png" {
			t.Fatalf("unexpected asset key %q", key)
		}
		return io.NopCloser(strings.NewReader("asset-content")), nil
	})
	publishedAt := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	updatedAt := publishedAt.Add(time.Hour)
	content := BlogContent{
		ID: uuid.New(), Title: "Exported title", Summary: "Exported summary", Content: "# Body\n\n![cover](https://assets.example.test/cover.png)\n\nHello.",
		Visibility: "followers", CoverURL: "https://assets.example.test/cover.png", PublishedAt: &publishedAt, UpdatedAt: updatedAt,
		User: &model.User{Username: "alice"}, Channel: &model.Channel{Name: "Writing"},
		Collections: []BlogCollection{{Name: "Essays"}},
	}
	archive, err := buildBlogMarkdownExport(context.Background(), content, []blogExportAsset{{URL: "https://assets.example.test/cover.png", Key: "blog/cover.png", ContentType: "image/png", Size: int64(len("asset-content")), ArchivePath: "assets/cover.png"}}, assetReader)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 3 || reader.File[0].Name != "assets/cover.png" || reader.File[1].Name != "post.md" || reader.File[2].Name != "assets/manifest.json" {
		t.Fatalf("unexpected export files: %#v", reader.File)
	}
	assetFile, err := reader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	assetBody, err := io.ReadAll(assetFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := assetFile.Close(); err != nil {
		t.Fatal(err)
	}
	if string(assetBody) != "asset-content" {
		t.Fatalf("unexpected exported asset body %q", assetBody)
	}
	file, err := reader.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`title: "Exported title"`, `summary: "Exported summary"`, `visibility: "followers"`,
		`channel: "Writing"`, `collections: ["Essays"]`, `author: "alice"`,
		`cover_url: "assets/cover.png"`, `![cover](assets/cover.png)`, `# Body`,
	} {
		if !strings.Contains(string(body), expected) {
			t.Fatalf("export missing %q: %s", expected, body)
		}
	}
}
