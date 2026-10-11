package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cs2bot/internal/config"
	"cs2bot/internal/storage"
)

type pandaScoreTeam struct {
	ID      int64              `json:"id"`
	Name    string             `json:"name"`
	Players []pandaScorePlayer `json:"players"`
}

type pandaScorePlayer struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func main() {
	configPath := flag.String("config", "config.json", "Путь к файлу конфигурации")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("Не удалось загрузить конфигурацию", slog.String("path", *configPath), slog.Any("error", err))
		os.Exit(1)
	}

	if cfg.PandaToken == "" {
		slog.Error("Отсутствует pandascore_token в конфиге")
		os.Exit(1)
	}

	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = "bot.db"
	}

	db, err := storage.Open(dbPath)
	if err != nil {
		slog.Error("Ошибка открытия БД", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	if err := storage.InitSchema(db); err != nil {
		slog.Error("Ошибка создания схемы БД", slog.Any("error", err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 15 * time.Second}
	fmt.Println("Начинаем загрузку...")
	teams, err := fetchTeams(ctx, client, cfg.PandaToken)
	if err != nil {
		slog.Error("Ошибка загрузки каталога команд", slog.Any("error", err))
		os.Exit(1)
	}
	if err := applyCatalog(ctx, db, teams); err != nil {
		slog.Error("Ошибка записи каталога команд", slog.Any("error", err))
		os.Exit(1)
	}
}
