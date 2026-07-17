package contest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	StateIdle              = "idle"
	StateSongsCollection   = "songs_collection"
	StateResultsCollection = "results_collection"
)

const minEligibleParticipants = 2

var (
	ErrNoActiveContest   = errors.New("contest: no active contest")
	ErrWeekInProgress    = errors.New("contest: a week is already in progress")
	ErrNotEnoughEligible = errors.New("contest: at least 2 eligible participants are required")
	ErrNoTopicsAvailable = errors.New("contest: no unused topics available for this contest")
	ErrNoActiveState     = errors.New("contest: no active songs_collection or results_collection state")
	ErrWeekNotIdle       = errors.New("contest: the active week must be idle before finishing the contest")
)

// SongsCollectionHooks lets U5 plug its strike and outbox-publish logic for
// songs_collection into the state machine this unit owns, without this
// package importing U5's. NewEngine defaults to a no-op implementation so
// this unit is fully testable on its own; main.go (U7) wires the real
// implementation once it exists.
type SongsCollectionHooks interface {
	// CloseSongsCollection runs inside the same transaction that transitions
	// a week out of songs_collection, either naturally (every required
	// participant submitted) or via /forceadvance (forced=true, meaning
	// stragglers exist and must be struck per R20).
	CloseSongsCollection(ctx context.Context, tx *sql.Tx, weekID int64, forced bool) error

	// SongsCollectionComplete reports whether every required participant
	// has submitted, used by Tick to detect a natural close (R19).
	SongsCollectionComplete(ctx context.Context, db *sql.DB, weekID int64) (bool, error)
}

// ResultsCollectionHooks is SongsCollectionHooks' counterpart for U6.
type ResultsCollectionHooks interface {
	// OpenResultsCollection runs inside the same transaction that
	// transitions a week into results_collection, letting U6 enqueue the
	// durable per-participant questionnaire/ranking DM dispatch (R21).
	OpenResultsCollection(ctx context.Context, tx *sql.Tx, weekID int64) error

	// CloseResultsCollection runs inside the same transaction that
	// transitions a week out of results_collection back to idle, either
	// naturally or via /forceadvance (R24-R28).
	CloseResultsCollection(ctx context.Context, tx *sql.Tx, weekID int64, forced bool) error

	// ResultsCollectionComplete reports whether every required participant
	// is done (ranking + questionnaire), used by Tick (R26).
	ResultsCollectionComplete(ctx context.Context, db *sql.DB, weekID int64) (bool, error)
}

type noopSongsHooks struct{}

func (noopSongsHooks) CloseSongsCollection(context.Context, *sql.Tx, int64, bool) error {
	return nil
}
func (noopSongsHooks) SongsCollectionComplete(context.Context, *sql.DB, int64) (bool, error) {
	return false, nil
}

type noopResultsHooks struct{}

func (noopResultsHooks) OpenResultsCollection(context.Context, *sql.Tx, int64) error {
	return nil
}
func (noopResultsHooks) CloseResultsCollection(context.Context, *sql.Tx, int64, bool) error {
	return nil
}
func (noopResultsHooks) ResultsCollectionComplete(context.Context, *sql.DB, int64) (bool, error) {
	return false, nil
}

// Engine drives the contest/week state machine. Every state-mutating method
// (and Tick's natural-advance check) takes mu before reading state, so two
// admins racing a command, or a command racing the tick, can't double-
// transition the state machine (KTD5; resolves doc-review finding A1, which
// flagged that the lock must cover the tick's own auto-transitions, not
// just admin commands).
type Engine struct {
	db           *sql.DB
	mu           sync.Mutex
	songsHooks   SongsCollectionHooks
	resultsHooks ResultsCollectionHooks
}

func NewEngine(db *sql.DB) *Engine {
	return &Engine{
		db:           db,
		songsHooks:   noopSongsHooks{},
		resultsHooks: noopResultsHooks{},
	}
}

// SetSongsHooks installs U5's real songs_collection close-and-completion
// logic. Must be called before Start; not safe to call concurrently with
// command handling.
func (e *Engine) SetSongsHooks(h SongsCollectionHooks) {
	e.songsHooks = h
}

// SetResultsHooks installs U6's real results_collection close-and-completion
// logic. Must be called before Start; not safe to call concurrently with
// command handling.
func (e *Engine) SetResultsHooks(h ResultsCollectionHooks) {
	e.resultsHooks = h
}

type week struct {
	id                   int64
	contestID            int64
	state                string
	topicID              sql.NullInt64
	stateStartedAt       sql.NullString
	deadlineOverrideDays sql.NullInt64
}

// activeContestID is a free function, not an Engine method, so
// SetParticipantLeft (participants.go) can share it without needing an
// Engine instance.
func activeContestID(ctx context.Context, q querier) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM contests WHERE active = 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoActiveContest
	}
	if err != nil {
		return 0, fmt.Errorf("contest: read active contest: %w", err)
	}
	return id, nil
}

// currentWeek returns the latest week row for a contest, or nil if no week
// has ever started (which is logically idle).
func (e *Engine) currentWeek(ctx context.Context, q querier, contestID int64) (*week, error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, contest_id, state, topic_id, state_started_at, deadline_override_days
		FROM weeks WHERE contest_id = ? ORDER BY id DESC LIMIT 1
	`, contestID)

	w := &week{}
	err := row.Scan(&w.id, &w.contestID, &w.state, &w.topicID, &w.stateStartedAt, &w.deadlineOverrideDays)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("contest: read current week: %w", err)
	}
	return w, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// WeekInfo is a read-only snapshot of the current week, used by U5/U6's
// command handlers (submission, voting, questionnaire) to check state
// without needing access to Engine's internals or its lock.
type WeekInfo struct {
	ID      int64
	State   string
	TopicID *int64
}

// CurrentWeek returns a snapshot of the active contest's current week, or a
// WeekInfo with State == StateIdle if no contest is active or no week has
// ever started.
func (e *Engine) CurrentWeek(ctx context.Context) (WeekInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	contestID, err := activeContestID(ctx, e.db)
	if errors.Is(err, ErrNoActiveContest) {
		return WeekInfo{State: StateIdle}, nil
	}
	if err != nil {
		return WeekInfo{}, err
	}

	w, err := e.currentWeek(ctx, e.db, contestID)
	if err != nil {
		return WeekInfo{}, err
	}
	if w == nil {
		return WeekInfo{State: StateIdle}, nil
	}

	info := WeekInfo{ID: w.id, State: w.state}
	if w.topicID.Valid {
		id := w.topicID.Int64
		info.TopicID = &id
	}
	return info, nil
}

// StartContest deactivates any previously active contest and activates a
// new one, enrolling every currently-active participant into it (R1) and
// associating the seeded default topic so its catalog is never empty (R16).
// phaseDays optionally configures the contest's songs-phase and
// results-phase durations (R1, R2), in that order -- a variadic tail rather
// than two required params so every existing two-arg call site (mostly
// tests seeding a bare contest) keeps compiling unchanged. A nil entry, or
// an omitted one, leaves that phase's column NULL, meaning "use
// DefaultDeadlineDays" (R3). Strikes need no reset: they're computed
// per-contest from contest_participants and week data, never stored (R9).
func (e *Engine) StartContest(ctx context.Context, name string, phaseDays ...*int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var songsDays, resultsDays *int
	if len(phaseDays) > 0 {
		songsDays = phaseDays[0]
	}
	if len(phaseDays) > 1 {
		resultsDays = phaseDays[1]
	}

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("contest: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE contests SET active = 0 WHERE active = 1`); err != nil {
		return "", fmt.Errorf("contest: deactivate previous contest: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO contests (name, active, songs_deadline_days, results_deadline_days) VALUES (?, 1, ?, ?)
	`, name, songsDays, resultsDays)
	if err != nil {
		return "", fmt.Errorf("contest: insert new contest: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("contest: read new contest id: %w", err)
	}

	if err := enrollContestParticipants(ctx, tx, id); err != nil {
		return "", err
	}
	if err := associateDefaultTopic(ctx, tx, id); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("contest: commit: %w", err)
	}

	resolvedSongs, resolvedResults := DefaultDeadlineDays, DefaultDeadlineDays
	if songsDays != nil {
		resolvedSongs = *songsDays
	}
	if resultsDays != nil {
		resolvedResults = *resultsDays
	}
	return fmt.Sprintf(
		"Contest %q started (id=%d). Songs phase: %d day(s). Results phase: %d day(s).",
		name, id, resolvedSongs, resolvedResults,
	), nil
}

// FinishContest deactivates the active contest, but only while its current
// week is idle (R5).
func (e *Engine) FinishContest(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	contestID, err := activeContestID(ctx, e.db)
	if err != nil {
		return "", err
	}

	w, err := e.currentWeek(ctx, e.db, contestID)
	if err != nil {
		return "", err
	}
	if w != nil && w.state != StateIdle {
		return "", ErrWeekNotIdle
	}

	if _, err := e.db.ExecContext(ctx, `UPDATE contests SET active = 0 WHERE id = ?`, contestID); err != nil {
		return "", fmt.Errorf("contest: deactivate contest: %w", err)
	}
	return "Contest finished.", nil
}

// StartWeek opens songs_collection for a new week: requires an active
// contest and at least 2 obligated participants, picks a topic from the
// contest's associated subset, and announces the topic (R6, R11). Required
// participants come directly from contest_participants -- no per-week
// roster snapshot is needed.
func (e *Engine) StartWeek(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("contest: begin tx: %w", err)
	}
	defer tx.Rollback()

	contestID, err := activeContestID(ctx, tx)
	if err != nil {
		return "", err
	}

	w, err := e.currentWeek(ctx, tx, contestID)
	if err != nil {
		return "", err
	}
	if w != nil && w.state != StateIdle {
		return "", ErrWeekInProgress
	}

	var eligibleCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM contest_participants WHERE contest_id = ? AND left_at IS NULL
	`, contestID).Scan(&eligibleCount); err != nil {
		return "", fmt.Errorf("contest: count eligible participants: %w", err)
	}
	if eligibleCount < minEligibleParticipants {
		return "", ErrNotEnoughEligible
	}

	topicID, topicText, err := pickTopic(ctx, tx, contestID)
	if err != nil {
		return "", err
	}

	now := time.Now().In(madridLocation)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO weeks (contest_id, state, topic_id, state_started_at)
		VALUES (?, ?, ?, ?)
	`, contestID, StateSongsCollection, topicID, now.Format(time.RFC3339)); err != nil {
		return "", fmt.Errorf("contest: insert week: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("contest: commit: %w", err)
	}

	defaultDays, err := contestPhaseDefaultDays(ctx, e.db, contestID, StateSongsCollection)
	if err != nil {
		return "", err
	}
	deadline := Deadline(now, nil, defaultDays)
	return fmt.Sprintf("Songs collection open! Topic: %q. Submit by %s.", topicText, deadline.Format("Mon 2 Jan 15:04")), nil
}

// ModifyLimit overrides the current state's deadline (in days from its
// start), applying only to whichever state is currently open (R8).
func (e *Engine) ModifyLimit(ctx context.Context, days int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	contestID, err := activeContestID(ctx, e.db)
	if err != nil {
		return "", err
	}
	w, err := e.currentWeek(ctx, e.db, contestID)
	if err != nil {
		return "", err
	}
	if w == nil || w.state == StateIdle {
		return "", ErrNoActiveState
	}

	if _, err := e.db.ExecContext(ctx, `UPDATE weeks SET deadline_override_days = ? WHERE id = ?`, days, w.id); err != nil {
		return "", fmt.Errorf("contest: update deadline override: %w", err)
	}

	started, err := time.Parse(time.RFC3339, w.stateStartedAt.String)
	if err != nil {
		return "", fmt.Errorf("contest: parse state_started_at: %w", err)
	}
	defaultDays, err := contestPhaseDefaultDays(ctx, e.db, contestID, w.state)
	if err != nil {
		return "", err
	}
	deadline := Deadline(started, &days, defaultDays)
	return fmt.Sprintf("Deadline for the current state updated to %s.", deadline.Format("Mon 2 Jan 15:04")), nil
}

// ForceAdvance closes the active state immediately regardless of
// completion, delegating the close-time strike/publish behavior to
// LifecycleHooks (R9).
func (e *Engine) ForceAdvance(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	contestID, err := activeContestID(ctx, e.db)
	if err != nil {
		return "", err
	}
	w, err := e.currentWeek(ctx, e.db, contestID)
	if err != nil {
		return "", err
	}
	if w == nil || w.state == StateIdle {
		return "", ErrNoActiveState
	}

	return e.advance(ctx, w, true)
}
