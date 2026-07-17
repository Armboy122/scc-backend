package dashboard

import (
	"testing"
	"time"
)

func TestBangkokDeadlineWindowIncludesCalendarDaysZeroThroughThree(t *testing.T) {
	now := time.Date(2026, time.July, 11, 23, 30, 0, 0, time.UTC) // July 12, 06:30 in Bangkok
	start, end := bangkokDeadlineWindow(now)

	if got, want := start.Format("2006-01-02 15:04 MST"), "2026-07-12 00:00 +07"; got != want {
		t.Fatalf("start = %s, want %s", got, want)
	}
	if !time.Date(2026, time.July, 15, 23, 59, 59, 0, bangkokLocation).Before(end.Add(time.Nanosecond)) {
		t.Fatal("day 3 must be included in the due-soon window")
	}
	if !time.Date(2026, time.July, 16, 0, 0, 0, 0, bangkokLocation).After(end) {
		t.Fatal("day 4 must be excluded from the due-soon window")
	}
}
