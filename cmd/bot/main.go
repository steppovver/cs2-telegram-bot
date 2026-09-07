package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cs2bot/internal/api"
	"cs2bot/internal/bot"
	"cs2bot/internal/domain"
	"cs2bot/internal/storage"

	"github.com/joho/godotenv"
	"github.com/lmittmann/tint"
	"gopkg.in/telebot.v3"
)

var defaultTeams = []domain.TeamInfo{
	{ID: "124523", Name: "Spirit"},
	{ID: "130564", Name: "Team Falcons"},
	{ID: "135177", Name: "BC.Game Esports"},
	{ID: "3210", Name: "G2"},
	{ID: "3212", Name: "FaZe"},
	{ID: "3240", Name: "MOUZ"},
}

func main() {
	debug := flag.Bool("debug", false, "Включить DEBUG-логирование")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}

	slog.SetDefault(slog.New(tint.NewTextHandler(os.Stdout, &tint.Options{
		Level:      logLevel,
		TimeFormat: time.TimeOnly,
	})))

	if err := godotenv.Load(); err != nil {
		slog.Warn("Файл .env не найден, используются системные переменные")
	}

	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	pandaToken := os.Getenv("PANDASCORE_TOKEN")
	if telegramToken == "" || pandaToken == "" {
		slog.Error("Отсутствуют необходимые переменные окружения (TELEGRAM_TOKEN, PANDASCORE_TOKEN)")
		os.Exit(1)
	}

	db, err := storage.NewStorage("bot.db", "teams.db")
	if err != nil {
		slog.Error("Ошибка инициализации БД", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	pandaClient := api.NewClient(pandaToken)

	tb, err := telebot.NewBot(telebot.Settings{
		Token:  telegramToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		slog.Error("Ошибка создания бота", slog.Any("error", err))
		os.Exit(1)
	}

	_ = tb.SetCommands([]telebot.Command{
		{Text: "start", Description: "Открыть главное меню"},
	})

	botApp := bot.New(tb, db, pandaClient, defaultTeams)
	botApp.RegisterHandlers()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go botApp.StartPoller(ctx)
	go botApp.StartMatchReminders(ctx)

	go func() {
		slog.Info("Telegram-бот успешно запущен")
		botApp.Start()
	}()

	<-ctx.Done()
	slog.Info("Завершение работы сервиса...")
	botApp.Stop()
	slog.Info("Сервис успешно остановлен")
}
