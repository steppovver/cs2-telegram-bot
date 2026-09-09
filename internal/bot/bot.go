package bot

import (
	"context"
	"time"

	"cs2bot/internal/domain"

	"gopkg.in/telebot.v3"
)

type Storage interface {
	GetUserSubscriptions(userID int64) ([]string, error)
	GetAllSubscribedTeams() ([]string, error)
	GetUpcomingUserMatches(subs []string) ([]domain.Match, error)
	GetMatchesForReminder() ([]domain.Match, error)
	GetUsersByTeam(teamName string) ([]int64, error)
	GetTeamIDByName(name string) (string, error)
	GetTeamIDsByNames(names []string) (map[string]string, error)
	ProcessMatch(m domain.Match) (isNew, timeChanged, teamsChanged bool, oldTime time.Time, oldTeamA, oldTeamB string, err error)
	MarkMatchAsNotified(matchID int) error
	CleanOldMatches()
	Subscribe(userID int64, teamName string) error
	Unsubscribe(userID int64, teamName string) error
	SearchTeams(query string) ([]domain.SearchedTeam, error)
}

type PandaClient interface {
	FetchMatchesByTeamIDs(ctx context.Context, teamIDs []string) ([]domain.Match, error)
}

type BroadcastTask struct {
	UserID int64
	Text   string
}

type Bot struct {
	telebot        *telebot.Bot
	storage        Storage
	panda          PandaClient
	supportedTeams []domain.TeamInfo

	broadcastCh chan BroadcastTask

	mainMenu     *telebot.ReplyMarkup
	btnSchedule  telebot.Btn
	btnSubscribe telebot.Btn
	btnSearch    telebot.Btn
}

func New(b *telebot.Bot, s Storage, p PandaClient, baseTeams []domain.TeamInfo) *Bot {
	menu := &telebot.ReplyMarkup{ResizeKeyboard: true}
	botApp := &Bot{
		telebot:        b,
		storage:        s,
		panda:          p,
		supportedTeams: baseTeams,
		broadcastCh:    make(chan BroadcastTask, 10000),
		mainMenu:       menu,
		btnSchedule:    menu.Text("📅 Узнать расписание"),
		btnSubscribe:   menu.Text("🔔 Подписки на команды"),
		btnSearch:      menu.Text("🔍 Поиск команды"),
	}

	botApp.mainMenu.Reply(
		botApp.mainMenu.Row(botApp.btnSchedule),
		botApp.mainMenu.Row(botApp.btnSubscribe),
		botApp.mainMenu.Row(botApp.btnSearch),
	)

	return botApp
}

func (b *Bot) RegisterHandlers() {
	b.telebot.Use(b.loggingMiddleware)

	b.telebot.Handle("/start", b.handleStart)
	b.telebot.Handle(&b.btnSchedule, b.handleSchedule)
	b.telebot.Handle(&b.btnSubscribe, b.handleSubscribe)
	b.telebot.Handle(&b.btnSearch, b.handleSearchPrompt)
	b.telebot.Handle(telebot.OnText, b.handleTextSearch)
	b.telebot.Handle("\fsub_", b.handleToggleSub)
}

func (b *Bot) Start() {
	b.telebot.Start()
}

func (b *Bot) Stop() {
	b.telebot.Stop()
}
