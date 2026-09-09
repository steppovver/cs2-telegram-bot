package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"cs2bot/internal/api"
	"cs2bot/internal/bot"
	"cs2bot/internal/config"
	"cs2bot/internal/domain"
	"cs2bot/internal/storage"

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
	configPath := flag.String("config", "config.json", "Путь к файлу конфигурации")
	flag.Parse()

	// Загружаем конфигурацию
	cfg, err := config.Load(*configPath)
	if err != nil {
		// Логгера еще нет, используем стандартный вывод ошибок
		slog.Error("Не удалось загрузить конфигурацию", slog.String("path", *configPath), slog.Any("error", err))
		os.Exit(1)
	}

	logLevel := slog.LevelInfo
	if cfg.Debug {
		logLevel = slog.LevelDebug
	}

	slog.SetDefault(slog.New(tint.NewTextHandler(os.Stdout, &tint.Options{
		Level:      logLevel,
		TimeFormat: time.TimeOnly,
	})))

	if cfg.TelegramToken == "" || cfg.PandaToken == "" {
		slog.Error("Отсутствуют необходимые токены в файле конфигурации")
		os.Exit(1)
	}

	// Передаем пути к БД из конфига вместо хардкода
	db, err := storage.NewStorage(cfg.BotDBPath, cfg.TeamsDBPath)
	if err != nil {
		slog.Error("Ошибка инициализации БД", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	pandaClient := api.NewClient(cfg.PandaToken)

	tb, err := telebot.NewBot(telebot.Settings{
		Token:  cfg.TelegramToken,
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

	// Создаем WaitGroup для отслеживания горутин
	var wg sync.WaitGroup

	// Запускаем воркер обновления расписания
	wg.Add(1)
	go func() {
		defer wg.Done() // Уменьшаем счетчик при выходе из воркера
		botApp.StartPoller(ctx)
	}()

	// Запускаем воркер напоминаний о матчах
	wg.Add(1)
	go func() {
		defer wg.Done()
		botApp.StartMatchReminders(ctx)
	}()

	// Запускаем воркер глобальной рассылки
	wg.Add(1)
	go func() {
		defer wg.Done()
		botApp.StartBroadcaster(ctx)
	}()

	// Telegram-бот запускается в отдельной горутине, т.к. Start() блокирует поток
	go func() {
		slog.Info("Telegram-бот успешно запущен")
		botApp.Start()
	}()

	// Ждем сигнала прерывания (Ctrl+C или SIGTERM от Docker/Systemd)
	<-ctx.Done()
	slog.Info("Получен сигнал завершения. Останавливаем бота...")

	// Сначала останавливаем прием новых сообщений от пользователей
	botApp.Stop()

	slog.Info("Ожидание завершения фоновых задач...")
	// Блокируем main, пока оба воркера не вызовут wg.Done()
	wg.Wait()

	slog.Info("Сервис успешно остановлен")
}
