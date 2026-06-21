package config

import (
	"errors"
	"testing"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

func TestLoad_AllVarsSet(t *testing.T) {
	setEnv(t, "BOT_TOKEN", "test-token")
	setEnv(t, "CHAT_ID", "-100123")
	setEnv(t, "DB_PATH", "/tmp/musiccontestbot.db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.BotToken != "test-token" {
		t.Errorf("BotToken = %q, want %q", cfg.BotToken, "test-token")
	}
	if cfg.ChatID != "-100123" {
		t.Errorf("ChatID = %q, want %q", cfg.ChatID, "-100123")
	}
	if cfg.DBPath != "/tmp/musiccontestbot.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/tmp/musiccontestbot.db")
	}
}

func TestLoad_MissingBotToken(t *testing.T) {
	setEnv(t, "BOT_TOKEN", "")
	setEnv(t, "CHAT_ID", "-100123")
	setEnv(t, "DB_PATH", "/tmp/musiccontestbot.db")

	_, err := Load()
	if !errors.Is(err, ErrMissingBotToken) {
		t.Fatalf("Load() error = %v, want %v", err, ErrMissingBotToken)
	}
}

func TestLoad_MissingChatID(t *testing.T) {
	setEnv(t, "BOT_TOKEN", "test-token")
	setEnv(t, "CHAT_ID", "")
	setEnv(t, "DB_PATH", "/tmp/musiccontestbot.db")

	_, err := Load()
	if !errors.Is(err, ErrMissingChatID) {
		t.Fatalf("Load() error = %v, want %v", err, ErrMissingChatID)
	}
}

func TestLoad_MissingDBPath(t *testing.T) {
	setEnv(t, "BOT_TOKEN", "test-token")
	setEnv(t, "CHAT_ID", "-100123")
	setEnv(t, "DB_PATH", "")

	_, err := Load()
	if !errors.Is(err, ErrMissingDBPath) {
		t.Fatalf("Load() error = %v, want %v", err, ErrMissingDBPath)
	}
}
