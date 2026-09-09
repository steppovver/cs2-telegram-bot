package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	TelegramToken string `json:"telegram_token"`
	PandaToken    string `json:"pandascore_token"`
	BotDBPath     string `json:"bot_db_path"`
	TeamsDBPath   string `json:"teams_db_path"`
	Debug         bool   `json:"debug"`
}

func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cfg Config
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
