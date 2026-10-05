package config

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"cs2bot/internal/domain"
)

type Config struct {
	TelegramToken     string            `json:"telegram_token"`
	PandaToken        string            `json:"pandascore_token"`
	DBPath            string            `json:"db_path"`
	Debug             bool              `json:"debug"`
	DefaultTeams      []domain.TeamInfo `json:"default_teams"`
	DigestPresetHours []int             `json:"digest_preset_hours"`
	AdminIDs          []int64           `json:"admin_ids"`
	MaxStreams        int               `json:"max_streams_per_match"`
	// Интервалы в секундах: 0 = дефолт (60 / 900).
	PollIntervalSec         int `json:"poll_interval_seconds"`
	FinishedPollIntervalSec int `json:"finished_poll_interval_seconds"`
	// Лимит REST-запросов PandaScore в час для варнингов: 0 = дефолт (1000).
	APIRateLimit int `json:"api_rate_limit_per_hour"`
	// Поиск точных ссылок на матчи HLTV: "ddg" (по умолчанию), "searxng"
	// или "none" (резолвер выключен, ссылки — поиск Google).
	SearchProvider string `json:"search_provider"`
	// SearchBaseURL — адрес инстанса SearXNG (только для search_provider=searxng).
	SearchBaseURL string `json:"search_base_url"`
}

// Провайдеры поиска ссылок HLTV.
const (
	SearchProviderDDG     = "ddg"
	SearchProviderSearXNG = "searxng"
	SearchProviderNone    = "none"
)

// NormalizeSearchProvider приводит значение к известному провайдеру:
// пустое = ddg, неизвестное ok=false.
func NormalizeSearchProvider(p string) (string, bool) {
	switch p = strings.ToLower(strings.TrimSpace(p)); p {
	case "":
		return SearchProviderDDG, true
	case SearchProviderDDG, SearchProviderSearXNG, SearchProviderNone:
		return p, true
	}
	return SearchProviderNone, false
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
	cfg.MaxStreams = NormalizeMaxStreams(cfg.MaxStreams)
	cfg.PollIntervalSec = NormalizePollInterval(cfg.PollIntervalSec)
	cfg.FinishedPollIntervalSec = NormalizeFinishedPollInterval(cfg.FinishedPollIntervalSec)
	cfg.APIRateLimit = NormalizeAPIRateLimit(cfg.APIRateLimit)

	return &cfg, nil
}

// Интервалы опроса в секундах. Дефолты: поллер матчей — 60с, добор
// завершенных — 900с. Нижние границы — от спама в PandaScore API.
const (
	DefaultPollIntervalSec         = 60
	MinPollIntervalSec             = 15
	MaxPollIntervalSec             = 3600
	DefaultFinishedPollIntervalSec = 900
	MinFinishedPollIntervalSec     = 60
	MaxFinishedPollIntervalSec     = 86400
)

// NormalizePollInterval чистит интервал опроса матчей: 0 и мусор = дефолт,
// дальше clamp в [Min, Max].
func NormalizePollInterval(sec int) int {
	if sec <= 0 {
		return DefaultPollIntervalSec
	}
	if sec < MinPollIntervalSec {
		return MinPollIntervalSec
	}
	if sec > MaxPollIntervalSec {
		return MaxPollIntervalSec
	}
	return sec
}

// NormalizeFinishedPollInterval чистит интервал добора завершенных матчей.
func NormalizeFinishedPollInterval(sec int) int {
	if sec <= 0 {
		return DefaultFinishedPollIntervalSec
	}
	if sec < MinFinishedPollIntervalSec {
		return MinFinishedPollIntervalSec
	}
	if sec > MaxFinishedPollIntervalSec {
		return MaxFinishedPollIntervalSec
	}
	return sec
}

// Границы лимита REST API в час для статистики использования.
const (
	DefaultAPIRateLimit = 1000
	MinAPIRateLimit     = 100
	MaxAPIRateLimit     = 1000000
)

// NormalizeAPIRateLimit чистит лимит REST-запросов в час: 0 = дефолт.
func NormalizeAPIRateLimit(n int) int {
	if n <= 0 {
		return DefaultAPIRateLimit
	}
	if n < MinAPIRateLimit {
		return MinAPIRateLimit
	}
	if n > MaxAPIRateLimit {
		return MaxAPIRateLimit
	}
	return n
}

// MaxStreamsHardCap — верхняя граница лимита стримов на матч, чтобы кривой
// ответ API с десятками ссылок не разорвал сообщение Telegram.
const MaxStreamsHardCap = 10

// NormalizeMaxStreams чистит лимит стримов: 0 и отрицательные = показать все,
// сверху hard-cap чтобы кривой ответ API не разорвал сообщение.
func NormalizeMaxStreams(n int) int {
	if n <= 0 {
		return 0
	}
	if n > MaxStreamsHardCap {
		return MaxStreamsHardCap
	}
	return n
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
