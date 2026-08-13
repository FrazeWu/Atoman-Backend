package musiclyrics

import "testing"

func TestParseLRC(t *testing.T) {
	lines, err := ParseLRC("[00:01.5]Hello\n[01:02.345]World", "[00:01.5]你好\n[01:02.345]世界")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	if lines[0].TimeMS == nil || *lines[0].TimeMS != 1500 || lines[0].Text != "Hello" || lines[0].Translation != "你好" {
		t.Fatalf("unexpected first LRC line: %#v", lines[0])
	}
	if lines[1].TimeMS == nil || *lines[1].TimeMS != 62345 || lines[1].Text != "World" || lines[1].Translation != "世界" {
		t.Fatalf("unexpected second LRC line: %#v", lines[1])
	}
}

func TestParseLRCRejectsMixedPlainContent(t *testing.T) {
	if _, err := ParseLRC("[00:01.00]Hello\nplain", ""); err == nil {
		t.Fatal("expected mixed plain content to be rejected")
	}
}
