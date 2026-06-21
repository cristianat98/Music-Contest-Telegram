package main

import (
	"log"

	"github.com/cristianat98/Music-Contest-Telegram/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	log.Printf("musiccontestbot starting (db=%s)", cfg.DBPath)
}
