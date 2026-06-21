package contest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const reminderHour = 20 // 20:00 Europe/Madrid (R17)

// SendDueReminders posts a daily reminder naming participants who haven't
// submitted yet, while songs_collection is open (R17). Safe to call on
// every tick: it no-ops unless it's past 20:00 Europe/Madrid for the
// current day and no reminder has been sent yet today for this week.
func SendDueReminders(ctx context.Context, db *sql.DB, notifier GroupNotifier) error {
	weekID, lastReminder, err := songsCollectionWeekForReminder(ctx, db)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	now := time.Now().In(madridLocation)
	if now.Hour() < reminderHour {
		return nil
	}
	today := now.Format("2006-01-02")
	if lastReminder.Valid && lastReminder.String == today {
		return nil
	}

	missing, err := missingSubmitterNames(ctx, db, weekID)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}

	text := "Reminder: still waiting on songs from: " + strings.Join(missing, ", ")
	if err := notifier.SendGroupMessage(ctx, text); err != nil {
		return fmt.Errorf("contest: send reminder: %w", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE weeks SET last_reminder_at = ? WHERE id = ?`, today, weekID); err != nil {
		return fmt.Errorf("contest: record reminder sent: %w", err)
	}
	return nil
}

func songsCollectionWeekForReminder(ctx context.Context, db *sql.DB) (int64, sql.NullString, error) {
	var weekID int64
	var lastReminder sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT w.id, w.last_reminder_at FROM weeks w
		JOIN contests c ON c.id = w.contest_id AND c.active = 1
		WHERE w.state = ?
		ORDER BY w.id DESC LIMIT 1
	`, StateSongsCollection).Scan(&weekID, &lastReminder)
	if err != nil {
		return 0, sql.NullString{}, err
	}
	return weekID, lastReminder, nil
}

func missingSubmitterNames(ctx context.Context, db *sql.DB, weekID int64) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT p.display_name FROM week_participants wp
		JOIN participants p ON p.id = wp.participant_id
		WHERE wp.week_id = ? AND NOT EXISTS (
			SELECT 1 FROM submissions s WHERE s.week_id = wp.week_id AND s.participant_id = wp.participant_id
		)
	`, weekID)
	if err != nil {
		return nil, fmt.Errorf("contest: list missing submitters: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("contest: scan missing submitter name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
