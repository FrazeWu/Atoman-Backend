package books

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

// BookFileQualityReport contains measured observations, never a subjective score.
// In particular, absence of EPUB/PDF evidence does not imply poor quality.
type BookFileQualityReport struct {
	Version           int      `json:"version"`
	Format            string   `json:"format"`
	Bytes             int64    `json:"bytes"`
	EPUBSpineItems    *int     `json:"epub_spine_items,omitempty"`
	EPUBHasNavigation *bool    `json:"epub_has_navigation,omitempty"`
	ComicImagePages   *int     `json:"comic_image_pages,omitempty"`
	TXTValidUTF8      *bool    `json:"txt_valid_utf8,omitempty"`
	TXTCharacters     *int64   `json:"txt_characters,omitempty"`
	Limitations       []string `json:"limitations"`
}

type bookQualityPackage struct {
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		Items []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
		TOC string `xml:"toc,attr"`
	} `xml:"spine"`
}

// File sizes, safety and archive structure were verified before this routine.
// All optional probes are bounded and never make a safe upload fail.
func inspectBookFileQuality(file *os.File, format string, size int64, metadata map[string]any) BookFileQualityReport {
	out := BookFileQualityReport{Version: 1, Format: format, Bytes: size, Limitations: []string{}}
	switch format {
	case "epub":
		root, _ := metadata["rootfile"].(string)
		archive, err := zip.NewReader(file, size)
		if err != nil || root == "" {
			out.Limitations = append(out.Limitations, "未能进一步解析 EPUB 章节与导航信息")
			break
		}
		var pkg *zip.File
		for _, entry := range archive.File {
			if path.Clean(entry.Name) == root {
				pkg = entry
				break
			}
		}
		if pkg == nil || pkg.UncompressedSize64 > 2<<20 {
			out.Limitations = append(out.Limitations, "EPUB 内容清单不可读取或过大")
			break
		}
		body, err := pkg.Open()
		if err != nil {
			out.Limitations = append(out.Limitations, "无法打开 EPUB 内容清单")
			break
		}
		var doc bookQualityPackage
		parseErr := xml.NewDecoder(io.LimitReader(body, 2<<20)).Decode(&doc)
		_ = body.Close()
		if parseErr != nil {
			out.Limitations = append(out.Limitations, "无法解析 EPUB 内容清单")
			break
		}
		count := len(doc.Spine.Items)
		navigation := strings.TrimSpace(doc.Spine.TOC) != ""
		for _, item := range doc.Manifest.Items {
			for _, prop := range strings.Fields(item.Properties) {
				if prop == "nav" {
					navigation = true
				}
			}
		}
		out.EPUBSpineItems, out.EPUBHasNavigation = &count, &navigation
		out.Limitations = append(out.Limitations, "未逐章验证正文完整性，导航标记不代表目录内容完整")
	case "cbz", "cbr":
		if count, ok := metadata["image_count"].(int); ok {
			out.ComicImagePages = &count
		}
		out.Limitations = append(out.Limitations, "图片数量来自压缩包清单，未逐页评估分辨率与视觉质量")
	case "txt":
		const sampleLimit = 2 << 20
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			break
		}
		data, err := io.ReadAll(io.LimitReader(file, sampleLimit+1))
		if err != nil {
			break
		}
		if len(data) <= sampleLimit {
			valid := utf8.Valid(data)
			out.TXTValidUTF8 = &valid
			if valid {
				count := int64(utf8.RuneCount(data))
				out.TXTCharacters = &count
			}
		} else {
			// The upload validator has already checked UTF-8 across the whole file.
			valid := true
			out.TXTValidUTF8 = &valid
			out.Limitations = append(out.Limitations, "文本超过 2 MiB，未统计全书字符数")
		}
		out.Limitations = append(out.Limitations, "文字数量不代表内容完整")
	case "pdf":
		out.Limitations = append(out.Limitations, "暂未检测 PDF 页数、文字层、OCR 质量与扫描清晰度")
	default:
		out.Limitations = append(out.Limitations, "未执行此格式的内容质量探测")
	}
	if len(out.Limitations) == 0 {
		out.Limitations = append(out.Limitations, "未验证内容是否完整")
	}
	return out
}
