package contest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// defaultTopicText is the seeded topic guaranteed to exist in the global
// catalog (see migration), auto-associated with every new contest so
// /startweek never finds an empty catalog (R16).
const defaultTopicText = "normal"

// associateDefaultTopic associates the seeded "normal" topic with a new
// contest's topic_usage, guaranteeing a non-empty catalog regardless of what
// else has been manually associated (R16).
func associateDefaultTopic(ctx context.Context, tx *sql.Tx, contestID int64) error {
	var topicID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM topics WHERE text = ?`, defaultTopicText).Scan(&topicID); err != nil {
		return fmt.Errorf("contest: look up default topic %q: %w", defaultTopicText, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO topic_usage (contest_id, topic_id, selectable) VALUES (?, ?, 1)
		ON CONFLICT (contest_id, topic_id) DO NOTHING
	`, contestID, topicID); err != nil {
		return fmt.Errorf("contest: associate default topic with contest %d: %w", contestID, err)
	}
	return nil
}

// pickTopic picks a topic for a new week from the contest's associated
// subset (R10, R11): preferring one not yet used (selectable = 1), falling
// back to a repeat once every associated topic has been used (R12), and
// erroring only if the contest has zero associated topics at all -- a
// defensive path, practically unreachable once the default topic is always
// associated (R16), but kept since manual DB seeding could remove it.
func pickTopic(ctx context.Context, tx *sql.Tx, contestID int64) (int64, string, error) {
	id, text, err := queryUnusedTopic(ctx, tx, contestID)
	if errors.Is(err, ErrNoTopicsAvailable) {
		id, text, err = queryAnyAssociatedTopic(ctx, tx, contestID)
	}
	if err != nil {
		return 0, "", err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE topic_usage SET selectable = 0 WHERE contest_id = ? AND topic_id = ?
	`, contestID, id); err != nil {
		return 0, "", fmt.Errorf("contest: mark topic %d unselectable: %w", id, err)
	}
	return id, text, nil
}

// queryUnusedTopic picks a random topic from the contest's subset that
// hasn't been used yet.
func queryUnusedTopic(ctx context.Context, tx *sql.Tx, contestID int64) (int64, string, error) {
	return scanTopic(tx.QueryRowContext(ctx, `
		SELECT t.id, t.text FROM topics t
		JOIN topic_usage tu ON tu.topic_id = t.id
		WHERE tu.contest_id = ? AND tu.selectable = 1
		ORDER BY RANDOM() LIMIT 1
	`, contestID))
}

// queryAnyAssociatedTopic picks a random topic from the contest's full
// associated subset regardless of whether it's been used -- the repeat
// fallback once every associated topic is exhausted (R12).
func queryAnyAssociatedTopic(ctx context.Context, tx *sql.Tx, contestID int64) (int64, string, error) {
	return scanTopic(tx.QueryRowContext(ctx, `
		SELECT t.id, t.text FROM topics t
		JOIN topic_usage tu ON tu.topic_id = t.id
		WHERE tu.contest_id = ?
		ORDER BY RANDOM() LIMIT 1
	`, contestID))
}

func scanTopic(row *sql.Row) (int64, string, error) {
	var id int64
	var text string
	err := row.Scan(&id, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrNoTopicsAvailable
	}
	if err != nil {
		return 0, "", fmt.Errorf("contest: pick topic: %w", err)
	}
	return id, text, nil
}
