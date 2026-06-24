package contest

import (
	"testing"
	"time"
)

func TestDeadline_DefaultMondayToThursday(t *testing.T) {
	// AE1: a Monday /startweek with no override yields a Thursday 12:00 deadline.
	monday := time.Date(2026, 6, 22, 9, 0, 0, 0, madridLocation) // 2026-06-22 is a Monday
	got := Deadline(monday, nil)

	want := time.Date(2026, 6, 25, 12, 0, 0, 0, madridLocation)
	if !got.Equal(want) {
		t.Errorf("Deadline() = %v, want %v", got, want)
	}
	if got.Weekday() != time.Thursday {
		t.Errorf("Deadline() weekday = %v, want Thursday", got.Weekday())
	}
}

func TestDeadline_VotesDeadlineFollowsThursdayToSunday(t *testing.T) {
	// Confirmed worked example: if startweek happens Monday, the songs
	// deadline is Thursday and the votes (results_collection) deadline is
	// Sunday -- both inclusive counts from their own state's start day.
	thursday := time.Date(2026, 6, 25, 12, 0, 0, 0, madridLocation)
	got := Deadline(thursday, nil)

	want := time.Date(2026, 6, 28, 12, 0, 0, 0, madridLocation)
	if !got.Equal(want) {
		t.Errorf("Deadline() = %v, want %v", got, want)
	}
	if got.Weekday() != time.Sunday {
		t.Errorf("Deadline() weekday = %v, want Sunday", got.Weekday())
	}
}

func TestDeadline_Override(t *testing.T) {
	monday := time.Date(2026, 6, 22, 9, 0, 0, 0, madridLocation)
	override := 2
	got := Deadline(monday, &override)

	want := time.Date(2026, 6, 23, 12, 0, 0, 0, madridLocation)
	if !got.Equal(want) {
		t.Errorf("Deadline() = %v, want %v", got, want)
	}
}

func TestDeadline_AcrossDSTTransition_StaysAtWallClockHour(t *testing.T) {
	// 2026-03-29 is when Europe/Madrid springs forward (02:00 -> 03:00).
	// A state starting just before the transition must still land on
	// 12:00 local time on the deadline day, not shifted by the DST jump.
	start := time.Date(2026, 3, 26, 12, 0, 0, 0, madridLocation)
	got := Deadline(start, nil)

	want := time.Date(2026, 3, 29, 12, 0, 0, 0, madridLocation)
	if !got.Equal(want) {
		t.Errorf("Deadline() = %v, want %v", got, want)
	}
	if got.Hour() != 12 {
		t.Errorf("Deadline() hour = %d, want 12 (wall-clock should be stable across DST)", got.Hour())
	}
}
