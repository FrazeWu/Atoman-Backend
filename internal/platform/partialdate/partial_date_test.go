package partialdate

import (
	"testing"
	"time"
)

func TestCompletelyUnknownDateRoundTrip(t *testing.T) {
	value, precision, err := Parse("----/--/--")
	if err != nil {
		t.Fatalf("parse unknown date: %v", err)
	}
	if value != nil || precision != Unknown {
		t.Fatalf("got value=%v precision=%q", value, precision)
	}
	if formatted := Format(time.Time{}, precision); formatted != "----/--/--" {
		t.Fatalf("format unknown date: %q", formatted)
	}
}
