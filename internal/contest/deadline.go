package contest

import "time"

// DefaultDeadlineDays is the default number of days (inclusive of the start
// day) a state has before its deadline, per origin's worked example: a
// Monday /startweek yields a Thursday deadline (R8, AE1).
const DefaultDeadlineDays = 4

var madridLocation = mustLoadLocation("Europe/Madrid")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic("contest: failed to load timezone " + name + ": " + err.Error())
	}
	return loc
}

// Deadline computes a state's deadline as 12:00 Europe/Madrid on the
// (days-1)-th day after stateStartedAt, using overrideDays in place of
// DefaultDeadlineDays when non-nil (R8). The result is always recomputed
// from stateStartedAt rather than cached, so a DST transition between the
// state's start and its deadline doesn't silently shift the wall-clock hour
// (KTD6).
func Deadline(stateStartedAt time.Time, overrideDays *int) time.Time {
	days := DefaultDeadlineDays
	if overrideDays != nil {
		days = *overrideDays
	}

	local := stateStartedAt.In(madridLocation)
	y, m, d := local.Date()
	base := time.Date(y, m, d, 12, 0, 0, 0, madridLocation)
	return base.AddDate(0, 0, days-1)
}
