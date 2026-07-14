package comment

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeContent(t *testing.T) {
	require.Equal(t, "第一行\n  第二行\n第三行", NormalizeContent(" \r\n第一行\r  第二行\r\n第三行 \t"))
}

func TestRenderCommentMarkdownAllowsMinimalFormatting(t *testing.T) {
	got, err := RenderCommentMarkdown("普通 **粗体** *斜体* `代码` [链接](https://example.com/a?q=1)\n换行\n\n> 引用")
	require.NoError(t, err)
	for _, tag := range []string{"<p>", "<strong>", "<em>", "<code>", `<a href="https://example.com/a?q=1">`, "<br>", "<blockquote>"} {
		require.Contains(t, got, tag)
	}
	require.NotContains(t, got, "<html")
}

func TestRenderCommentMarkdownAllowsStrikethroughMarkersInsideCode(t *testing.T) {
	got, err := RenderCommentMarkdown("`~~不是删除线~~`")
	require.NoError(t, err)
	require.Contains(t, got, "<code>~~不是删除线~~</code>")
}

func TestRenderCommentMarkdownAllowsTableDelimiterInsideMultilineCodeSpan(t *testing.T) {
	got, err := RenderCommentMarkdown("`第一行\n| - | - |\n第三行`")
	require.NoError(t, err)
	require.Contains(t, got, "<code>第一行 | - | - | 第三行</code>")
}

func TestRenderCommentMarkdownRejectsStrikethroughAfterUnclosedBacktick(t *testing.T) {
	_, err := RenderCommentMarkdown("`未闭合 ``合法代码`` ~~删除线~~")
	require.Error(t, err)
}

func TestRenderCommentMarkdownRejectsHTMLAndImages(t *testing.T) {
	_, err := RenderCommentMarkdown("<script>x</script> ![x](https://x.test/a.png)")
	require.Error(t, err)
}

func TestRenderCommentMarkdownRejectsUnsupportedSyntax(t *testing.T) {
	tests := map[string]string{
		"unordered list": "- 一项",
		"ordered list":   "1. 一项",
		"heading":        "# 标题",
		"code block":     "```go\nfmt.Println()\n```",
		"table":          "| A | B |\n| - | - |\n| 1 | 2 |",
		"strikethrough":  "~~删除~~",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := RenderCommentMarkdown(input)
			require.Error(t, err)
		})
	}
}

func TestRenderCommentMarkdownRejectsUnsafeOrRelativeLinks(t *testing.T) {
	for _, input := range []string{
		"[x](javascript:alert(1))",
		"[x](data:text/html;base64,eA==)",
		"[x](/relative)",
		"[x](//example.com/path)",
	} {
		_, err := RenderCommentMarkdown(input)
		require.Error(t, err, input)
	}
}

func TestRenderCommentMarkdownAllowsCaseInsensitiveHTTPScheme(t *testing.T) {
	got, err := RenderCommentMarkdown("[x](HTTPS://example.com/path)")
	require.NoError(t, err)
	require.Contains(t, got, `href="HTTPS://example.com/path"`)
}

func TestRenderCommentMarkdownEscapesPlainText(t *testing.T) {
	got, err := RenderCommentMarkdown("1 < 2 & 3")
	require.NoError(t, err)
	require.Contains(t, got, "1 &lt; 2 &amp; 3")
	require.False(t, strings.Contains(got, "< 2"))
}
