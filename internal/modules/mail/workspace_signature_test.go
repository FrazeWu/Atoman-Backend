package mail

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestMailSignatureDoesNotModifyExistingSieveScript(t *testing.T) {
	v := defaultMailSettings()
	original := renderMailSieve(v, map[string]string{})
	v.Signature = MailSignature{Enabled: true, Text: "Hello", HTML: "<strong>Hello</strong>", OnReply: true}
	require.NoError(t, validateSettings(&v))
	require.Equal(t, original, renderMailSieve(v, map[string]string{}))
}

func TestMailSignatureSettingsValidationAndSanitization(t *testing.T) {
	v := defaultMailSettings()
	v.Signature = MailSignature{Enabled: true, Text: "Hello\\n-- User", HTML: "<strong>Hello</strong><img src=\\\"https://tracker.example/pixel\\\"><script>alert(1)</script>", OnReply: true}
	require.NoError(t, validateSettings(&v))
	require.Contains(t, v.Signature.HTML, "<strong>Hello</strong>")
	require.NotContains(t, v.Signature.HTML, "<script>")
	require.NotContains(t, v.Signature.HTML, "<img")
	require.True(t, v.Signature.OnReply)
	v.Signature.Text = strings.Repeat("x", 8193)
	require.Error(t, validateSettings(&v))
	v.Signature.Text = ""
	v.Signature.HTML = ""
	require.Error(t, validateSettings(&v))
}
