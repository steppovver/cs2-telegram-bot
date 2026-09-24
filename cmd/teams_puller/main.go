package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"cs2bot/internal/config"
	"cs2bot/internal/storage"
)

type pandaScoreTeam struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
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

	client := &http.Client{Timeout: 15 * time.Second}
	page := 1
	perPage := 100
	totalTeams := 0

	fmt.Println("Начинаем загрузку...")

	for {
		url := fmt.Sprintf("https://api.pandascore.co/csgo/teams?page[number]=%d&page[size]=%d", page, perPage)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			slog.Error("Ошибка создания запроса", slog.Any("error", err))
			os.Exit(1)
		}
		req.Header.Set("Authorization", "Bearer "+cfg.PandaToken)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			slog.Error("Ошибка HTTP-запроса", slog.Int("page", page), slog.Any("error", err))
			os.Exit(1)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			slog.Error("PandaScore вернул статус", slog.Int("status", resp.StatusCode), slog.Int("page", page))
			os.Exit(1)
		}

		var teams []pandaScoreTeam
		err = json.NewDecoder(resp.Body).Decode(&teams)
		resp.Body.Close()
		if err != nil {
			slog.Error("Ошибка декодирования JSON", slog.Any("error", err))
			os.Exit(1)
		}

		if len(teams) == 0 {
			break
		}

		tx, err := db.Begin()
		if err != nil {
			slog.Error("Ошибка открытия транзакции", slog.Any("error", err))
			os.Exit(1)
		}

		teamStmt, err := tx.Prepare(`INSERT INTO teams (id, name) VALUES (?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name`)
		if err != nil {
			tx.Rollback()
			slog.Error("Ошибка подготовки teamStmt", slog.Any("error", err))
			os.Exit(1)
		}

		playerStmt, err := tx.Prepare(`INSERT INTO players (id, team_id, name) VALUES (?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, team_id=excluded.team_id`)
		if err != nil {
			tx.Rollback()
			slog.Error("Ошибка подготовки playerStmt", slog.Any("error", err))
			os.Exit(1)
		}

		insertedThisPage := 0
		for _, team := range teams {
			if len(team.Players) == 0 {
				continue
			}

			if _, err := teamStmt.Exec(team.ID, team.Name); err != nil {
				tx.Rollback()
				slog.Error("Ошибка вставки команды", slog.Int64("team_id", team.ID), slog.Any("error", err))
				os.Exit(1)
			}

			for _, p := range team.Players {
				if _, err := playerStmt.Exec(p.ID, team.ID, p.Name); err != nil {
					tx.Rollback()
					slog.Error("Ошибка вставки игрока", slog.Int64("player_id", p.ID), slog.Any("error", err))
					os.Exit(1)
				}
			}
			insertedThisPage++
		}

		if err := tx.Commit(); err != nil {
			slog.Error("Ошибка коммита", slog.Any("error", err))
			os.Exit(1)
		}

		teamStmt.Close()
		playerStmt.Close()

		totalTeams += insertedThisPage
		fmt.Printf("Страница %d обработана (сохранено команд с составом: %d)...\n", page, insertedThisPage)

		page++
		time.Sleep(200 * time.Millisecond)
	}

	fmt.Printf("Готово! Всего команд с составами сохранено: %d\n", totalTeams)
}
