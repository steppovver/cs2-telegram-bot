package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"cs2bot/internal/api"
	"cs2bot/internal/storage"

	"github.com/joho/godotenv"
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
	{ID: "3240", Name: "MOUZ"},
}

// === КЭШ В ОПЕРАТИВНОЙ ПАМЯТИ ===
type MatchCache struct {
	mu      sync.RWMutex
	matches map[string][]api.Match
}

func NewMatchCache() *MatchCache {
	return &MatchCache{
		matches: make(map[string][]api.Match),
	}
}

func (c *MatchCache) UpdateMultiple(newData map[string][]api.Match) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for teamID, matches := range newData {
		c.matches[teamID] = matches
	}
}

func (c *MatchCache) Get(teamID string) ([]api.Match, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, exists := c.matches[teamID]
	return data, exists
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Файл .env не найден.")
	}

	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	pandaToken := os.Getenv("PANDASCORE_TOKEN")

	if telegramToken == "" || pandaToken == "" {
		log.Fatal("Пожалуйста, установите TELEGRAM_TOKEN и PANDASCORE_TOKEN")
	}

	db, err := storage.NewStorage("bot.db")
	if err != nil {
		log.Fatalf("Ошибка БД: %v", err)
	}
	defer db.Close()

	matchCache := NewMatchCache()

	pref := telebot.Settings{
		Token:  telegramToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}
	b, err := telebot.NewBot(pref)
	if err != nil {
		log.Fatal(err)
	}

	go startPoller(db, matchCache, pandaToken)

	// === 1. ГЛАВНОЕ МЕНЮ ===
	mainMenu := &telebot.ReplyMarkup{ResizeKeyboard: true}
	btnSchedule := mainMenu.Text("📅 Узнать расписание")
	btnSubscribe := mainMenu.Text("🔔 Подписаться на команды")
	mainMenu.Reply(
		mainMenu.Row(btnSchedule),
		mainMenu.Row(btnSubscribe),
	)

	b.Handle("/start", func(c telebot.Context) error {
		return c.Send("Привет! Выбери нужное действие в меню ниже:", mainMenu)
	})

	// === 2. НАЖАТИЕ: ПОДПИСАТЬСЯ ===
	b.Handle(&btnSubscribe, func(c telebot.Context) error {
		menu := buildTeamsKeyboard("sub_", SupportedTeams)
		return c.Send("Выбери команду, на которую хочешь подписаться:", menu)
	})

	// === 3. НАЖАТИЕ: РАСПИСАНИЕ (ХРОНОЛОГИЧЕСКИЙ СПИСОК) ===
	b.Handle(&btnSchedule, func(c telebot.Context) error {
		userID := c.Sender().ID

		subs, err := db.GetUserSubscriptions(userID)
		if err != nil {
			return c.Send("Произошла ошибка при обращении к базе данных.")
		}

		if len(subs) == 0 {
			return c.Send("Вы еще не подписаны ни на одну команду.\nНажмите «🔔 Подписаться на команды».")
		}

		// 1. Собираем все матчи пользователя в единую мапу (чтобы избежать дубликатов)
		// Используем ID матча как ключ
		uniqueMatches := make(map[int]api.Match)
		cacheIsLoading := false

		for _, subName := range subs {
			var teamID string
			for _, t := range SupportedTeams {
				if strings.ToUpper(t.Name) == strings.ToUpper(subName) {
					teamID = t.ID
					break
				}
			}

			if teamID == "" {
				continue
			}

			matches, exists := matchCache.Get(teamID)
			if !exists {
				// Если хотя бы одной команды еще нет в кэше, ставим флаг
				cacheIsLoading = true
				continue
			}

			for _, match := range matches {
				uniqueMatches[match.ID] = match
			}
		}

		if cacheIsLoading && len(uniqueMatches) == 0 {
			return c.Send("⏳ <i>Расписание еще загружается в кэш. Попробуй через минуту.</i>", telebot.ModeHTML)
		}

		if len(uniqueMatches) == 0 {
			return c.Send("Для ваших команд в ближайшее время игр не найдено.")
		}

		// 2. Перекладываем уникальные матчи в массив для сортировки
		var sortedMatches []api.Match
		for _, m := range uniqueMatches {
			sortedMatches = append(sortedMatches, m)
		}

		// 3. Сортируем массив по времени возрастания (от ближайших к дальним)
		sort.Slice(sortedMatches, func(i, j int) bool {
			return sortedMatches[i].Time.Before(sortedMatches[j].Time)
		})

		// 4. Формируем красивый хронологический список
		var sb strings.Builder
		sb.WriteString("🎮 <b>Предстоящие матчи:</b>\n\n")

		for _, match := range sortedMatches {
			timeStr := match.Time.In(time.Local).Format("02.01 15:04")

			// Выделяем жирным команды, на которые подписан пользователь
			// (опциональное украшение, чтобы было видно, из-за кого матч попал в список)
			teamA := match.TeamA
			teamB := match.TeamB

			for _, sub := range subs {
				if strings.EqualFold(match.TeamA, sub) {
					teamA = "<b>" + teamA + "</b>"
				}
				if strings.EqualFold(match.TeamB, sub) {
					teamB = "<b>" + teamB + "</b>"
				}
			}

			sb.WriteString(fmt.Sprintf("⏰ %s | %s vs %s\n", timeStr, teamA, teamB))
		}

		return c.Send(sb.String(), telebot.ModeHTML)
	})

	// === 4. ИНЛАЙН ОБРАБОТЧИК ДЛЯ ПОДПИСОК ===
	// Обработчик schedule_ полностью удален, так как он больше не нужен!

	b.Handle("\fsub_", func(c telebot.Context) error {
		payload := c.Callback().Data
		parts := strings.Split(payload, "|")
		if len(parts) != 3 {
			return c.Respond(&telebot.CallbackResponse{Text: "Ошибка формата данных."})
		}

		teamName := strings.ToUpper(parts[2])
		userID := c.Sender().ID

		if err := db.Subscribe(userID, teamName); err != nil {
			log.Printf("Ошибка подписки: %v", err)
			return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при подписке."})
		}

		c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("Подписка на %s оформлена!", teamName)})
		return c.Send(fmt.Sprintf("✅ Вы успешно подписались на уведомления об играх <b>%s</b>!", teamName), telebot.ModeHTML)
	})

	log.Println("Бот успешно запущен!")
	b.Start()
}

// === 5. ФОНОВЫЙ ОПРОС API ===
func startPoller(db *storage.Storage, cache *MatchCache, pandaToken string) {
	updateRoutine := func() {
		subscribedTeams, err := db.GetAllSubscribedTeams()
		if err != nil || len(subscribedTeams) == 0 {
			return
		}

		var idsToFetch []string
		requestedIDs := make(map[string]bool)

		for _, teamName := range subscribedTeams {
			for _, t := range SupportedTeams {
				if strings.ToUpper(t.Name) == strings.ToUpper(teamName) {
					idsToFetch = append(idsToFetch, t.ID)
					requestedIDs[t.ID] = true
					break
				}
			}
		}

		if len(idsToFetch) == 0 {
			return
		}

		matches, err := api.FetchMatchesByTeamIDs(pandaToken, idsToFetch)
		if err != nil {
			log.Printf("Ошибка группового запроса матчей: %v", err)
			return
		}

		groupedMatches := make(map[string][]api.Match)

		for id := range requestedIDs {
			groupedMatches[id] = []api.Match{}
		}

		for _, match := range matches {
			idA := fmt.Sprintf("%d", match.TeamAID)
			idB := fmt.Sprintf("%d", match.TeamBID)

			if requestedIDs[idA] {
				groupedMatches[idA] = append(groupedMatches[idA], match)
			}
			if requestedIDs[idB] {
				groupedMatches[idB] = append(groupedMatches[idB], match)
			}
		}

		cache.UpdateMultiple(groupedMatches)
	}

	log.Println("Выполняю первичный прогрев кэша (Bulk request)...")
	updateRoutine()
	log.Println("Кэш успешно прогрет.")

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		updateRoutine()
	}
}

// === 6. ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ ===
func buildTeamsKeyboard(actionPrefix string, teamsToDisplay []TeamInfo) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}

	var rows []telebot.Row
	var currentRow []telebot.Btn

	for _, t := range teamsToDisplay {
		payload := actionPrefix + "|" + t.ID + "|" + t.Name
		btn := menu.Data(t.Name, actionPrefix, payload)

		currentRow = append(currentRow, btn)

		if len(currentRow) == 2 {
			rows = append(rows, menu.Row(currentRow...))
			currentRow = nil
		}
	}

	if len(currentRow) > 0 {
		rows = append(rows, menu.Row(currentRow...))
	}

	menu.Inline(rows...)
	return menu
}
