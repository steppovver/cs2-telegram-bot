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
	GetMatchesForReminder() ([]domain.Match, error)
	GetUsersByTeamIDs(teamAID, teamBID int) ([]int64, error)
	ProcessMatch(m domain.Match) (isNew, timeChanged, teamsChanged, statusChanged bool, oldTime time.Time, oldTeamA, oldTeamB, oldStatus string, err error)
	MarkMatchAsNotified(matchID int) error
	CleanOldMatches()
	CleanStaleRunningMatches(apiMatchIDs map[int]bool)
	Subscribe(userID, teamID int64, teamName string) error
	Unsubscribe(userID, teamID int64) error
	SearchTeams(query string) ([]domain.SearchedTeam, error)
	GetTeamsByIDs(ids []int) ([]domain.TeamInfo, error)
	GetDigestSettings(userID int64) (domain.DigestSettings, error)
	SetDigestEnabled(userID int64, enabled bool) error
	SetDigestHour(userID int64, hour int) error
	GetDigestDueUsers(hour int, today string) ([]int64, error)
	MarkDigestSent(userID int64, date string) error
	GetDigestMatches(userID int64, fromUnix, toUnix int64) ([]domain.Match, error)
}

type PandaClient interface {
	FetchMatchesByTeamIDs(ctx context.Context, teamIDs []string) ([]domain.Match, error)
}

type BroadcastTask struct {
	UserID int64
	Text   string
}

type Bot struct {
	telebot      *telebot.Bot
	storage      Storage
	panda        PandaClient
	defaultTeams []domain.TeamInfo

	broadcastCh chan BroadcastTask

	mainMenu     *telebot.ReplyMarkup
	btnSchedule  telebot.Btn
	btnSubscribe telebot.Btn
	btnSearch    telebot.Btn
	btnDigest    telebot.Btn

	awaitingHour   map[int64]bool
	awaitingHourMu sync.Mutex

	digestHours []int
}

func New(b *telebot.Bot, s Storage, p PandaClient, defaultTeams []domain.TeamInfo, digestPresetHours []int) *Bot {
	menu := &telebot.ReplyMarkup{ResizeKeyboard: true, IsPersistent: true}
	botApp := &Bot{
		telebot:      b,
		storage:      s,
		panda:        p,
		defaultTeams: defaultTeams,
		digestHours:  config.NormalizeDigestHours(digestPresetHours),
		awaitingHour: make(map[int64]bool),
		broadcastCh:  make(chan BroadcastTask, 10000),
		mainMenu:     menu,
		btnSchedule:  menu.Text("📅 Узнать расписание"),
		btnSubscribe: menu.Text("🔔 Подписки на команды"),
		btnSearch:    menu.Text("🔍 Поиск команды"),
		btnDigest:    menu.Text("⏰ Дайджест"),
	}

	botApp.mainMenu.Reply(
		botApp.mainMenu.Row(botApp.btnSchedule),
		botApp.mainMenu.Row(botApp.btnSubscribe),
		botApp.mainMenu.Row(botApp.btnSearch),
		botApp.mainMenu.Row(botApp.btnDigest),
	)

	return botApp
}

func (b *Bot) RegisterHandlers() {
	b.telebot.Use(b.loggingMiddleware)

	b.telebot.Handle("/start", b.handleStart)
	b.telebot.Handle(&b.btnSchedule, b.handleSchedule)
	b.telebot.Handle(&b.btnSubscribe, b.handleSubscribe)
	b.telebot.Handle(&b.btnSearch, b.handleSearchPrompt)
	b.telebot.Handle(&b.btnDigest, b.handleDigestMenu)
	b.telebot.Handle(telebot.OnText, b.handleTextSearch)
	b.telebot.Handle("\fsub_", b.handleToggleSub)
	b.telebot.Handle("\fdigest", b.handleDigestCallback)
}

func (b *Bot) Start() {
	b.telebot.Start()
}

func (b *Bot) Stop() {
	b.telebot.Stop()
}
