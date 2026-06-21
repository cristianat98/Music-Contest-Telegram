package bot

import (
	"testing"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
)

func TestCommandArgs(t *testing.T) {
	cases := map[string]string{
		"/startcontest Summer Jam": "Summer Jam",
		"/startcontest":            "",
		"/startweek":               "",
		"/modifylimit 3":           "3",
	}
	for input, want := range cases {
		if got := commandArgs(input); got != want {
			t.Errorf("commandArgs(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLifecycleErrorText_KnownSentinel(t *testing.T) {
	got := lifecycleErrorText(contest.ErrNotEnoughEligible)
	if got != contest.ErrNotEnoughEligible.Error() {
		t.Errorf("lifecycleErrorText() = %q, want %q", got, contest.ErrNotEnoughEligible.Error())
	}
}
