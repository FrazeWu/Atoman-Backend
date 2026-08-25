package blog

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// exportMarkdownPost godoc
// @Summary 导出文章 Markdown 备份
// @Description 返回当前文章版本及已验证站内资源的 ZIP 备份。仅作者或管理员可导出。
// @Tags blog
// @Produce application/zip
// @Param id path string true "文章 UUID"
// @Success 200 {file} binary
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/posts/{id}/export [get]
func (h *Handler) exportMarkdownPost(c *gin.Context) {
	viewer, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	contentID, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	content, err := loadCanonicalBlogContent(h.service.db, contentID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if content.UserID != viewer.ID && !authctx.RoleAtLeast(viewer.Role, authctx.RoleAdmin) {
		httpx.Error(c, apperr.Forbidden("blog.export_forbidden", "Only the author or an administrator can export this post"))
		return
	}
	assets, err := loadBlogExportAssets(h.service.db, content.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	archive, err := buildBlogMarkdownExport(c.Request.Context(), content, assets, h.service.exportAssets)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "blog-"+content.ID.String()+".zip"))
	c.Data(http.StatusOK, "application/zip", archive)
}

type blogExportAsset struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	ArchivePath string `json:"archive_path"`
}

func loadBlogExportAssets(db *gorm.DB, contentID uuid.UUID) ([]blogExportAsset, error) {
	var assets []model.MediaAsset
	if err := db.Table("media_assets").
		Joins("JOIN content_media_assets ON content_media_assets.media_asset_id = media_assets.id").
		Where("content_media_assets.content_id = ?", contentID).
		Order("media_assets.id ASC").Find(&assets).Error; err != nil {
		return nil, err
	}
	result := make([]blogExportAsset, 0, len(assets))
	for _, asset := range assets {
		result = append(result, blogExportAsset{URL: asset.URL, Key: asset.Key, ContentType: asset.ContentType, Size: asset.Size, ArchivePath: "assets/" + asset.ID.String() + assetExtension(asset.ContentType)})
	}
	return result, nil
}

func assetExtension(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

const maxBlogExportAssetBytes int64 = 5 << 20
const maxBlogExportTotalAssetBytes int64 = 50 << 20

func buildBlogMarkdownExport(ctx context.Context, content BlogContent, assets []blogExportAsset, reader ExportAssetReader) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	markdown := content.Content
	var totalSize int64
	for _, asset := range assets {
		if reader == nil {
			return nil, apperr.New(http.StatusServiceUnavailable, "storage.unavailable", "Storage service is unavailable", nil)
		}
		if asset.Size <= 0 || asset.Size > maxBlogExportAssetBytes || totalSize > maxBlogExportTotalAssetBytes-asset.Size {
			return nil, apperr.BadRequest("blog.export_asset_invalid", "Export asset exceeds the archive limit")
		}
		body, err := reader.ReadExportAsset(ctx, asset.Key, asset.Size)
		if err != nil {
			return nil, err
		}
		assetFile, err := archive.Create(asset.ArchivePath)
		if err != nil {
			_ = body.Close()
			return nil, err
		}
		written, copyErr := io.CopyN(assetFile, body, asset.Size+1)
		closeErr := body.Close()
		if copyErr != nil && copyErr != io.EOF {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if written != asset.Size {
			return nil, fmt.Errorf("export asset size does not match metadata")
		}
		markdown = strings.ReplaceAll(markdown, asset.URL, asset.ArchivePath)
		if content.CoverURL == asset.URL {
			content.CoverURL = asset.ArchivePath
		}
		totalSize += written
	}
	file, err := archive.Create("post.md")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write([]byte(blogMarkdownFrontMatter(content) + markdown + "\n")); err != nil {
		return nil, err
	}
	manifest, err := archive.Create("assets/manifest.json")
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(assets, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := manifest.Write(encoded); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func blogMarkdownFrontMatter(content BlogContent) string {
	collections := make([]string, 0, len(content.Collections))
	for _, collection := range content.Collections {
		collections = append(collections, strconv.Quote(collection.Name))
	}
	if len(collections) == 0 && content.Collection != nil {
		collections = append(collections, strconv.Quote(content.Collection.Name))
	}
	channel := ""
	if content.Channel != nil {
		channel = content.Channel.Name
	}
	author := ""
	if content.User != nil {
		author = content.User.Username
	}
	publishedAt := ""
	if content.PublishedAt != nil {
		publishedAt = content.PublishedAt.UTC().Format(time.RFC3339)
	}
	return strings.Join([]string{
		"---",
		"title: " + strconv.Quote(content.Title),
		"summary: " + strconv.Quote(content.Summary),
		"slug: " + strconv.Quote(content.ID.String()),
		"visibility: " + strconv.Quote(content.Visibility),
		"channel: " + strconv.Quote(channel),
		"collections: [" + strings.Join(collections, ", ") + "]",
		"author: " + strconv.Quote(author),
		"published_at: " + strconv.Quote(publishedAt),
		"updated_at: " + strconv.Quote(content.UpdatedAt.UTC().Format(time.RFC3339)),
		"cover_url: " + strconv.Quote(content.CoverURL),
		"---",
		"",
	}, "\n")
}
