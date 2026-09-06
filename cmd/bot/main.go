package main

import (
	"flag"
	"log/slog"
	"os"
	"time"

	"cs2bot/internal/storage"

	"github.com/joho/godotenv"
	"github.com/lmittmann/tint"
	"gopkg.in/telebot.v3"
)

type TeamInfo struct {
	ID   string
	Name string
}

var SupportedTeams = []TeamInfo{
	{ID: "124523", Name: "Spirit"},
	{ID: "130564", Name: "Falcons"},
	{ID: "135177", Name: "BC.Game"},
	{ID: "3210", Name: "G2"},
	{ID: "3212", Name: "FaZe"},
	{ID: "3240", Name: "MOUZ"},
}

var (
	mainMenu     = &telebot.ReplyMarkup{ResizeKeyboard: true}
	btnSchedule  = mainMenu.Text("📅 Узнать расписание")
	btnSubscribe = mainMenu.Text("🔔 Подписаться на команды")
)

type Application struct {
	bot        *telebot.Bot
	db         *storage.Storage
	pandaToken string
}

func main() {
	debug := flag.Bool("debug", false, "Включить уровень логирования DEBUG")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}

	logger := slog.New(tint.NewTextHandler(os.Stdout, &tint.Options{
		Level:      logLevel,
		TimeFormat: time.TimeOnly,
	}))
	slog.SetDefault(logger)

	if err := godotenv.Load(); err != nil {
		slog.Warn("Файл .env не найден, используем системные переменные")
	}

	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	pandaToken := os.Getenv("PANDASCORE_TOKEN")

	if telegramToken == "" || pandaToken == "" {
		slog.Error("Отсутствуют необходимые токены (TELEGRAM_TOKEN, PANDASCORE_TOKEN)")
		os.Exit(1)
	}

	db, err := storage.NewStorage("bot.db")
	if err != nil {
		slog.Error("Ошибка БД", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	pref := telebot.Settings{
		Token:  telegramToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}
	b, err := telebot.NewBot(pref)
	if err != nil {
		slog.Error("Ошибка инициализации бота", slog.Any("error", err))
		os.Exit(1)
	}

	app := &Application{
		bot:        b,
		db:         db,
		pandaToken: pandaToken,
	}

	// Системное меню команд (защита от "Clear history")
	err = b.SetCommands([]telebot.Command{
		{Text: "start", Description: "Открыть главное меню"},
	})
	if err != nil {
		slog.Warn("Не удалось установить команды бота", slog.Any("error", err))
	}

	mainMenu.Reply(
		mainMenu.Row(btnSchedule),
		mainMenu.Row(btnSubscribe),
	)

	b.Use(app.loggingMiddleware)

	b.Handle("/start", app.handleStart)
	b.Handle(telebot.OnText, app.handleStart) // Catch-all для любого текста
	b.Handle(&btnSchedule, app.handleSchedule)
	b.Handle(&btnSubscribe, app.handleSubscribe)
	b.Handle("\fsub_", app.handleToggleSub)

	go app.startPoller()
	go app.startMatchReminders()

	slog.Info("Бот успешно запущен!")
	b.Start()
}
