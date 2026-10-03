package bot

import (
	"context"
	"sync"
	"time"

	"cs2bot/internal/config"
	"cs2bot/internal/domain"

	"gopkg.in/telebot.v3"
)

type Storage interface {
	GetUserSubscriptions(userID int64) ([]domain.TeamInfo, error)
	GetSubscribedTeamIDs() ([]string, error)
	GetUpcomingUserMatches(userID int64) ([]domain.Match, error)
	GetLiveUserMatches(userID int64) ([]domain.Match, error)
	GetScoreMatches(userID int64) ([]domain.Match, error)
	GetMatchesForReminder() ([]domain.Match, error)
	GetUsersByTeamIDs(teamAID, teamBID int) ([]int64, error)
	ProcessMatch(m domain.Match) (isNew, timeChanged, teamsChanged, statusChanged bool, oldTime time.Time, oldTeamA, oldTeamB, oldStatus string, err error)
	MarkMatchAsNotified(matchID int) error
	CleanOldMatches()
	CleanStaleRunningMatches(apiMatchIDs map[int]bool)
	Subscribe(userID, teamID int64, teamName string) error
	Unsubscribe(userID, teamID int64) error
	RemoveUser(userID int64) error
	GetStats() (domain.BotStats, error)
	SearchTeams(query string) ([]domain.SearchedTeam, error)
	GetTeamsByIDs(ids []int) ([]domain.TeamInfo, error)
	GetDigestSettings(userID int64) (domain.DigestSettings, error)
	SetDigestEnabled(userID int64, enabled bool) error
	SetDigestHour(userID int64, hour int) error
	GetUserOffset(userID int64) (int, error)
	SetUserOffset(userID int64, offset int) error
	GetUserOffsets(userIDs []int64) (map[int64]int, error)
	GetDigestDueUsers(hourUTC int, slot string) ([]domain.DigestDueUser, error)
	MarkDigestSent(userID int64, date string) error
	GetDigestMatches(userID int64, fromUnix, toUnix int64) ([]domain.Match, error)
	EnsureHltvLinkRow(matchID int) error
	GetHltvDueIDs(nowUnix, fromUnix, toUnix int64, limit int) ([]int, error)
	HltvAttempts(matchID int) (int, error)
	ClaimHltvAttempt(matchID int, nextTryUnix int64) (int, error)
	SetHltvURL(matchID int, url string) error
	HltvURLByIDs(ids []int) (map[int]string, error)
	GetMatchByID(matchID int) (domain.Match, error)
}

type PandaClient interface {
	FetchMatchesByTeamIDs(ctx context.Context, teamIDs []string) ([]domain.Match, error)
	FetchFinishedMatchesByTeamIDs(ctx context.Context, teamIDs []string, since time.Time) ([]domain.Match, error)
	// APIUsage: запросов за текущий UTC-час, минимальный остаток лимита
	// (-1 если ответов еще не было) и сам лимит в час.
	APIUsage() (used, remaining, limit int)
}

type BroadcastTask struct {
	UserID int64
	Text   string
}

// Version, BuildDate и BuildCommit подставляются при сборке через ldflags
// (см. deploy/deploy.sh). По умолчанию — dev-сборка.
var (
	Version     = "dev"
	BuildDate   = "unknown"
	BuildCommit = "unknown"
)

type Bot struct {
	telebot      *telebot.Bot
	storage      Storage
	panda        PandaClient
	defaultTeams []domain.TeamInfo

	broadcastCh chan BroadcastTask

	mainMenu     *telebot.ReplyMarkup
	btnSchedule  telebot.Btn
	btnScore     telebot.Btn
	btnSubscribe telebot.Btn
	btnDigest    telebot.Btn

	awaitingHour   map[int64]bool
	awaitingHourMu sync.Mutex

	awaitingTZ   map[int64]bool
	awaitingTZMu sync.Mutex

	digestHours []int

	maxStreams int

	// apiWarnAt — время последнего варнинга об израсходовании лимита API.
	// Throttle: чаще раза в час не предупреждаем.
	apiWarnMu sync.Mutex
	apiWarnAt time.Time

	// hltvQ — очередь ID матчей на резолв ссылки HLTV, hltvInflight —
	// множество "в очереди или в работе" против дублей между тиками.
	hltvQ        chan int
	hltvMu       sync.Mutex
	hltvInflight map[int]bool

	admins map[int64]bool
}

func New(b *telebot.Bot, s Storage, p PandaClient, defaultTeams []domain.TeamInfo, digestPresetHours []int, adminIDs []int64, maxStreams int) *Bot {
	menu := &telebot.ReplyMarkup{ResizeKeyboard: true, IsPersistent: true}
	admins := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		admins[id] = true
	}
	botApp := &Bot{
		telebot:      b,
		storage:      s,
		panda:        p,
		defaultTeams: defaultTeams,
		digestHours:  config.NormalizeDigestHours(digestPresetHours),
		maxStreams:   config.NormalizeMaxStreams(maxStreams),
		admins:       admins,
		awaitingHour: make(map[int64]bool),
		awaitingTZ:   make(map[int64]bool),
		broadcastCh:  make(chan BroadcastTask, 10000),
		hltvQ:        make(chan int, hltvQueueSize),
		hltvInflight: make(map[int]bool),
		mainMenu:     menu,
		btnSchedule:  menu.Text("📅 Узнать расписание"),
		btnScore:     menu.Text("📊 Счет матчей"),
		btnSubscribe: menu.Text("🔔 Подписки на команды"),
		btnDigest:    menu.Text("⏰ Дайджест"),
	}

	botApp.mainMenu.Reply(
		botApp.mainMenu.Row(botApp.btnSchedule),
		botApp.mainMenu.Row(botApp.btnScore),
		botApp.mainMenu.Row(botApp.btnSubscribe),
		botApp.mainMenu.Row(botApp.btnDigest),
	)

	return botApp
}

func (b *Bot) RegisterHandlers() {
	b.telebot.Use(b.loggingMiddleware)

	b.telebot.Handle("/start", b.handleStart)
	b.telebot.Handle("/version", b.handleVersion)
	b.telebot.Handle("/admin", b.handleAdmin)
	b.telebot.Handle(&b.btnSchedule, b.handleSchedule)
	b.telebot.Handle(&b.btnScore, b.handleScore)
	b.telebot.Handle(&b.btnSubscribe, b.handleSubscribe)
	b.telebot.Handle(&b.btnDigest, b.handleDigestMenu)
	b.telebot.Handle(telebot.OnText, b.handleTextSearch)
	b.telebot.Handle("\fsub_", b.handleToggleSub)
	b.telebot.Handle("\fdigest", b.handleDigestCallback)
	b.telebot.Handle("\fadmin", b.handleAdminCallback)
}

// isAdmin проверяет ID по списку admin_ids из конфига.
// Задел под будущие админ-команды — проверка в одном месте.
func (b *Bot) isAdmin(userID int64) bool {
	return b.admins[userID]
}

func (b *Bot) Start() {
	b.telebot.Start()
}

func (b *Bot) Stop() {
	b.telebot.Stop()
}
