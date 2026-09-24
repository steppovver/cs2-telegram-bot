package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	TelegramToken string `json:"telegram_token"`
	PandaToken    string `json:"pandascore_token"`
	DBPath        string `json:"db_path"`
	Debug         bool   `json:"debug"`
}

type configFile struct {
	Config
	BotDBPath   string `json:"bot_db_path"`
	TeamsDBPath string `json:"teams_db_path"`
}

func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var raw configFile
	if err := json.NewDecoder(file).Decode(&raw); err != nil {
		return nil, err
	}

	cfg := raw.Config
	if cfg.DBPath == "" {
		cfg.DBPath = raw.BotDBPath
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "bot.db"
	}

	return &cfg, nil
}
