package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"cs2bot/internal/api"
	"cs2bot/internal/bot"
	"cs2bot/internal/config"
	"cs2bot/internal/hltv"
	"cs2bot/internal/storage"

	"github.com/lmittmann/tint"
	"gopkg.in/telebot.v3"
)

func main() {
	configPath := flag.String("config", "config.json", "Путь к файлу конфигурации")
	showVersion := flag.Bool("version", false, "Показать версию сборки и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Printf("cs2bot %s\nсборка: %s\nкоммит: %s\n", bot.Version, bot.BuildDate, bot.BuildCommit)
		return
	}

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

	db, err := storage.NewStorage(cfg.DBPath)
	if err != nil {
		slog.Error("Ошибка инициализации БД", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	pandaClient := api.NewClient(cfg.PandaToken, cfg.APIRateLimit)

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

	botApp := bot.New(tb, db, pandaClient, cfg.DefaultTeams, cfg.DigestPresetHours, cfg.AdminIDs, cfg.MaxStreams)
	botApp.RegisterHandlers()

	searchProvider, ok := config.NormalizeSearchProvider(cfg.SearchProvider)
	if !ok {
		slog.Warn("Неизвестный search_provider, резолвер ссылок HLTV выключен", slog.String("value", cfg.SearchProvider))
	}
	hltvSearch, err := hltv.NewSearchProvider(searchProvider, cfg.SearchBaseURL)
	if err != nil {
		slog.Error("Ошибка настройки поиска ссылок HLTV", slog.Any("error", err))
		os.Exit(1)
	}
	botApp.SetHLTVResolver(hltv.NewResolver(db, hltvSearch))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Создаем WaitGroup для отслеживания горутин
	var wg sync.WaitGroup

	// Запускаем воркер обновления расписания
	wg.Add(1)
	go func() {
		defer wg.Done() // Уменьшаем счетчик при выходе из воркера
		botApp.StartPoller(ctx, time.Duration(cfg.PollIntervalSec)*time.Second)
	}()

	// Запускаем воркер добора завершенных матчей (счет для кнопки 📊)
	wg.Add(1)
	go func() {
		defer wg.Done()
		botApp.StartFinishedPoller(ctx, time.Duration(cfg.FinishedPollIntervalSec)*time.Second)
	}()

	// Запускаем воркер поиска точных ссылок на матчи HLTV
	wg.Add(1)
	go func() {
		defer wg.Done()
		botApp.StartHLTVResolver(ctx)
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

	// Запускаем воркер ежедневного дайджеста
	wg.Add(1)
	go func() {
		defer wg.Done()
		botApp.StartDailyDigest(ctx)
	}()

	// Telegram-бот запускается в отдельной горутине, т.к. Start() блокирует поток
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("Telegram-бот успешно запущен")
		botApp.Start()
		slog.Info("Telegram-бот остановлен")
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
