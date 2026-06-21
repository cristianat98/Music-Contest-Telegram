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

func (e *Engine) activeContestID(ctx context.Context, q querier) (int64, error) {
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

	contestID, err := e.activeContestID(ctx, e.db)
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

// StartContest deactivates any previously active contest, resets every
// participant's strike count, and activates a new contest. The new
// contest's topic pool starts fully unused because topic_usage rows are
// keyed by contest_id -- there's nothing to reset (R4, R10, R12; KTD7).
func (e *Engine) StartContest(ctx context.Context, name string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("contest: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE contests SET active = 0 WHERE active = 1`); err != nil {
		return "", fmt.Errorf("contest: deactivate previous contest: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE participants SET strikes = 0`); err != nil {
		return "", fmt.Errorf("contest: reset strikes: %w", err)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO contests (name, active) VALUES (?, 1)`, name)
	if err != nil {
		return "", fmt.Errorf("contest: insert new contest: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("contest: read new contest id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("contest: commit: %w", err)
	}

	return fmt.Sprintf("Contest %q started (id=%d). All strikes reset.", name, id), nil
}

// FinishContest deactivates the active contest, but only while its current
// week is idle (R5).
func (e *Engine) FinishContest(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	contestID, err := e.activeContestID(ctx, e.db)
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
// contest and at least 2 eligible participants, picks a random unused
// topic, snapshots the required-participant set, and announces the topic
// (R6, R11).
func (e *Engine) StartWeek(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("contest: begin tx: %w", err)
	}
	defer tx.Rollback()

	contestID, err := e.activeContestID(ctx, tx)
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
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM participants WHERE active = 1`).Scan(&eligibleCount); err != nil {
		return "", fmt.Errorf("contest: count eligible participants: %w", err)
	}
	if eligibleCount < minEligibleParticipants {
		return "", ErrNotEnoughEligible
	}

	topicID, topicText, err := e.pickUnusedTopic(ctx, tx, contestID)
	if err != nil {
		return "", err
	}

	now := time.Now().In(madridLocation)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO weeks (contest_id, state, topic_id, state_started_at)
		VALUES (?, ?, ?, ?)
	`, contestID, StateSongsCollection, topicID, now.Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("contest: insert week: %w", err)
	}
	weekID, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("contest: read new week id: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO topic_usage (contest_id, topic_id, used) VALUES (?, ?, 1)
		ON CONFLICT (contest_id, topic_id) DO UPDATE SET used = 1
	`, contestID, topicID); err != nil {
		return "", fmt.Errorf("contest: mark topic used: %w", err)
	}

	if err := e.snapshotRequiredParticipants(ctx, tx, weekID); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("contest: commit: %w", err)
	}

	deadline := Deadline(now, nil)
	return fmt.Sprintf("Songs collection open! Topic: %q. Submit by %s.", topicText, deadline.Format("Mon 2 Jan 15:04")), nil
}

func (e *Engine) pickUnusedTopic(ctx context.Context, tx *sql.Tx, contestID int64) (int64, string, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT t.id, t.text FROM topics t
		WHERE t.id NOT IN (
			SELECT topic_id FROM topic_usage WHERE contest_id = ? AND used = 1
		)
		ORDER BY RANDOM() LIMIT 1
	`, contestID)

	var id int64
	var text string
	err := row.Scan(&id, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrNoTopicsAvailable
	}
	if err != nil {
		return 0, "", fmt.Errorf("contest: pick unused topic: %w", err)
	}
	return id, text, nil
}

func (e *Engine) snapshotRequiredParticipants(ctx context.Context, tx *sql.Tx, weekID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM participants WHERE active = 1`)
	if err != nil {
		return fmt.Errorf("contest: list eligible participants: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("contest: scan participant id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO week_participants (week_id, participant_id) VALUES (?, ?)
		`, weekID, id); err != nil {
			return fmt.Errorf("contest: snapshot participant %d: %w", id, err)
		}
	}
	return nil
}

// ModifyLimit overrides the current state's deadline (in days from its
// start), applying only to whichever state is currently open (R8).
func (e *Engine) ModifyLimit(ctx context.Context, days int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	contestID, err := e.activeContestID(ctx, e.db)
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
	deadline := Deadline(started, &days)
	return fmt.Sprintf("Deadline for the current state updated to %s.", deadline.Format("Mon 2 Jan 15:04")), nil
}

// ForceAdvance closes the active state immediately regardless of
// completion, delegating the close-time strike/publish behavior to
// LifecycleHooks (R9).
func (e *Engine) ForceAdvance(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	contestID, err := e.activeContestID(ctx, e.db)
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
