package config

import (
	"errors"
	"os"
)

var (
	ErrMissingBotToken   = errors.New("config: BOT_TOKEN is required")
	ErrMissingChatID     = errors.New("config: CHAT_ID is required")
	ErrMissingDBPath     = errors.New("config: DB_PATH is required")
)

// Config holds the bot's runtime configuration, loaded from environment variables.
type Config struct {
	BotToken string
	ChatID   string
	DBPath   string
}

// Load reads the bot's configuration from the environment, failing fast with a
// named error if any required value is missing.
func Load() (Config, error) {
	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		return Config{}, ErrMissingBotToken
	}

	chatID := os.Getenv("CHAT_ID")
	if chatID == "" {
		return Config{}, ErrMissingChatID
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		return Config{}, ErrMissingDBPath
	}

	return Config{
		BotToken: botToken,
		ChatID:   chatID,
		DBPath:   dbPath,
	}, nil
}
