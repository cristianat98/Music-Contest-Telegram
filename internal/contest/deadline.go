package contest

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DefaultDeadlineDays is the fallback number of days (inclusive of the start
// day) a state has before its deadline, used when a contest has no
// configured phase duration of its own (legacy contests, or an omitted
// /startcontest argument), per origin's worked example: a Monday /startweek
// yields a Thursday deadline (R8, AE1).
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
// defaultDays when non-nil (R6, R8). The result is always recomputed from
// stateStartedAt rather than cached, so a DST transition between the
// state's start and its deadline doesn't silently shift the wall-clock hour
// (KTD6).
func Deadline(stateStartedAt time.Time, overrideDays *int, defaultDays int) time.Time {
	days := defaultDays
	if overrideDays != nil {
		days = *overrideDays
	}

	local := stateStartedAt.In(madridLocation)
	y, m, d := local.Date()
	base := time.Date(y, m, d, 12, 0, 0, 0, madridLocation)
	return base.AddDate(0, 0, days-1)
}

// contestPhaseDefaultDays reads the configured default duration for
// whichever phase state names, for the given contest, falling back to
// DefaultDeadlineDays when the contest has no configured value of its own
// (R2, R3) -- COALESCE happens in Go rather than SQL so the fallback
// constant stays a single source of truth.
func contestPhaseDefaultDays(ctx context.Context, q querier, contestID int64, state string) (int, error) {
	var query string
	switch state {
	case StateSongsCollection:
		query = `SELECT songs_deadline_days FROM contests WHERE id = ?`
	case StateResultsCollection:
		query = `SELECT results_deadline_days FROM contests WHERE id = ?`
	default:
		return 0, fmt.Errorf("contest: no configured phase duration for state %q", state)
	}

	var days sql.NullInt64
	if err := q.QueryRowContext(ctx, query, contestID).Scan(&days); err != nil {
		return 0, fmt.Errorf("contest: read phase default days: %w", err)
	}
	if days.Valid {
		return int(days.Int64), nil
	}
	return DefaultDeadlineDays, nil
}
