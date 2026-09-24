package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"cs2bot/internal/config"
	"cs2bot/internal/storage"

	"github.com/joho/godotenv"
)

type PandaScorePlayer struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type PandaScoreTeam struct {
	ID      int64              `json:"id"`
	Name    string             `json:"name"`
	Players []PandaScorePlayer `json:"players"`
}

func main() {
	if err := run(); err != nil {
		log.Printf("Фатальная ошибка: %v", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.json", "Путь к файлу конфигурации")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		slog.Warn("Файл .env не найден, используем системные переменные")
	}

	apiToken := os.Getenv("PANDASCORE_TOKEN")
	if apiToken == "" {
		return fmt.Errorf("переменная окружения PANDASCORE_TOKEN не установлена")
	}

	dbPath := "bot.db"
	if cfg, err := config.Load(*configPath); err == nil && cfg.DBPath != "" {
		dbPath = cfg.DBPath
	} else if err != nil {
		slog.Warn("Не удалось загрузить конфиг, используем bot.db", slog.Any("error", err))
	}

	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("ошибка открытия БД: %w", err)
	}
	defer db.Close()

	if err := storage.InitSchema(db); err != nil {
		return fmt.Errorf("ошибка создания схемы БД: %w", err)
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
			return fmt.Errorf("ошибка создания запроса: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiToken)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("ошибка выполнения HTTP-запроса на стр. %d: %w", page, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("PandaScore вернул статус %d на стр. %d", resp.StatusCode, page)
		}

		var teams []PandaScoreTeam
		err = json.NewDecoder(resp.Body).Decode(&teams)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("ошибка декодирования JSON: %w", err)
		}

		if len(teams) == 0 {
			break // Страницы закончились
		}

		// Записываем пачку в БД
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("ошибка открытия транзакции: %w", err)
		}

		teamStmt, err := tx.Prepare(`INSERT INTO teams (id, name) VALUES (?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name`)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("ошибка подготовки teamStmt: %w", err)
		}
		defer teamStmt.Close()

		playerStmt, err := tx.Prepare(`INSERT INTO players (id, team_id, name) VALUES (?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, team_id=excluded.team_id`)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("ошибка подготовки playerStmt: %w", err)
		}
		defer playerStmt.Close()

		insertedThisPage := 0
		for _, team := range teams {
			// Пропускаем команду, если у нее нет игроков в составе
			if len(team.Players) == 0 {
				continue
			}

			if _, err := teamStmt.Exec(team.ID, team.Name); err != nil {
				tx.Rollback()
				return fmt.Errorf("ошибка вставки команды %d: %w", team.ID, err)
			}

			for _, p := range team.Players {
				if _, err := playerStmt.Exec(p.ID, team.ID, p.Name); err != nil {
					tx.Rollback()
					return fmt.Errorf("ошибка вставки игрока %d: %w", p.ID, err)
				}
			}
			insertedThisPage++
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("ошибка коммита транзакции: %w", err)
		}

		totalTeams += insertedThisPage
		fmt.Printf("Страница %d обработана (сохранено команд с составом: %d)...\n", page, insertedThisPage)

		page++
		time.Sleep(200 * time.Millisecond) // Соблюдаем rate limit
	}

	fmt.Printf("Готово! Всего команд с составами сохранено: %d\n", totalTeams)
	return nil
}
