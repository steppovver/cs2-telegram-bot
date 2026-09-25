package config

import (
	"encoding/json"
	"os"
	"sort"

	"cs2bot/internal/domain"
)

type Config struct {
	TelegramToken     string            `json:"telegram_token"`
	PandaToken        string            `json:"pandascore_token"`
	DBPath            string            `json:"db_path"`
	Debug             bool              `json:"debug"`
	DefaultTeams      []domain.TeamInfo `json:"default_teams"`
	DigestPresetHours []int             `json:"digest_preset_hours"`
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
	cfg.DigestPresetHours = NormalizeDigestHours(cfg.DigestPresetHours)

	return &cfg, nil
}

// NormalizeDigestHours чистит пресеты часов дайджеста: только 0–23,
// без дублей, по возрастанию. Пустой/мусорный ввод -> дефолт 9/10/11/12.
func NormalizeDigestHours(hours []int) []int {
	seen := make(map[int]bool)
	out := make([]int, 0, len(hours))
	for _, h := range hours {
		if h < 0 || h > 23 || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	if len(out) == 0 {
		return []int{9, 10, 11, 12}
	}
	sort.Ints(out)
	return out
}
