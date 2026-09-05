package main

import (
	"fmt"
	"log"
	"os"
	"strings"
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

	pref := telebot.Settings{
		Token:  telegramToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}
	b, err := telebot.NewBot(pref)
	if err != nil {
		log.Fatal(err)
	}

	// Запускаем умный поллер
	go startPoller(b, db, pandaToken)

	// === ГЛАВНОЕ МЕНЮ ===
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

	// === НАЖАТИЕ: ПОДПИСАТЬСЯ ===
	b.Handle(&btnSubscribe, func(c telebot.Context) error {
		menu := buildTeamsKeyboard("sub_", SupportedTeams)
		return c.Send("Выбери команду, на которую хочешь подписаться:", menu)
	})

	// === НАЖАТИЕ: РАСПИСАНИЕ (ИЗ БАЗЫ ДАННЫХ) ===
	b.Handle(&btnSchedule, func(c telebot.Context) error {
		userID := c.Sender().ID

		subs, err := db.GetUserSubscriptions(userID)
		if err != nil {
			return c.Send("Произошла ошибка при обращении к базе данных.")
		}

		if len(subs) == 0 {
			return c.Send("Вы еще не подписаны ни на одну команду.\nНажмите «🔔 Подписаться на команды».")
		}

		// Теперь мы просто просим у базы отсортированный список матчей для этих подписок!
		matches, err := db.GetUpcomingUserMatches(subs)
		if err != nil {
			return c.Send("Ошибка получения расписания.")
		}

		if len(matches) == 0 {
			return c.Send("Для ваших команд в ближайшее время игр не найдено.")
		}

		var sb strings.Builder
		sb.WriteString("🎮 <b>Предстоящие матчи:</b>\n\n")

		for _, match := range matches {
			timeStr := match.Time.In(time.Local).Format("02.01 15:04")

			teamA, teamB := match.TeamA, match.TeamB
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

	// === ИНЛАЙН ПОДПИСКА ===
	b.Handle("\fsub_", func(c telebot.Context) error {
		payload := c.Callback().Data
		parts := strings.Split(payload, "|")
		if len(parts) != 3 {
			return c.Respond(&telebot.CallbackResponse{Text: "Ошибка формата данных."})
		}

		teamName := strings.ToUpper(parts[2])
		userID := c.Sender().ID

		if err := db.Subscribe(userID, teamName); err != nil {
			return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при подписке."})
		}

		c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("Подписка на %s оформлена!", teamName)})
		return c.Send(fmt.Sprintf("✅ Вы успешно подписались на уведомления об играх <b>%s</b>!", teamName), telebot.ModeHTML)
	})

	log.Println("Бот успешно запущен!")
	b.Start()
}

// === ФОНОВЫЙ ОПРОС И УВЕДОМЛЕНИЯ ===
func startPoller(bot *telebot.Bot, db *storage.Storage, pandaToken string) {
	updateRoutine := func() {
		subscribedTeams, err := db.GetAllSubscribedTeams()
		if err != nil || len(subscribedTeams) == 0 {
			return
		}

		var idsToFetch []string
		for _, teamName := range subscribedTeams {
			for _, t := range SupportedTeams {
				if strings.ToUpper(t.Name) == strings.ToUpper(teamName) {
					idsToFetch = append(idsToFetch, t.ID)
					break
				}
			}
		}

		if len(idsToFetch) == 0 {
			return
		}

		matches, err := api.FetchMatchesByTeamIDs(pandaToken, idsToFetch)
		if err != nil {
			log.Printf("Ошибка запроса матчей: %v", err)
			return
		}

		// Сверяем матчи с базой данных
		for _, match := range matches {
			isNew, timeChanged, oldTime, err := db.ProcessMatch(match)
			if err != nil {
				log.Printf("Ошибка сохранения матча %d: %v", match.ID, err)
				continue
			}

			// Если ничего не изменилось — идем дальше
			if !isNew && !timeChanged {
				continue
			}

			// Формируем красивое уведомление в зависимости от типа события
			var msg string
			timeStr := match.Time.In(time.Local).Format("15:04 02.01")

			if isNew {
				msg = fmt.Sprintf("🆕 <b>Добавлен новый матч!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n⏰ Время: %s", match.TeamA, match.TeamB, timeStr)
			} else if timeChanged {
				oldTimeStr := oldTime.In(time.Local).Format("15:04 02.01")
				msg = fmt.Sprintf("⚠️ <b>Время матча изменено!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n<s>Старое время: %s</s>\n⏰ Новое время: %s", match.TeamA, match.TeamB, oldTimeStr, timeStr)
			}

			// Находим всех, кому это интересно (объединяем подписчиков TeamA и TeamB без дубликатов)
			usersA, _ := db.GetUsersByTeam(match.TeamA)
			usersB, _ := db.GetUsersByTeam(match.TeamB)

			uniqueUsers := make(map[int64]bool)
			for _, u := range usersA {
				uniqueUsers[u] = true
			}
			for _, u := range usersB {
				uniqueUsers[u] = true
			}

			// Рассылаем
			for userID := range uniqueUsers {
				bot.Send(telebot.ChatID(userID), msg, telebot.ModeHTML)
			}
		}

		// Вычищаем матчи, которые прошли вчера, чтобы база не разрасталась бесконечно
		db.CleanOldMatches()
	}

	updateRoutine()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		updateRoutine()
	}
}

func buildTeamsKeyboard(actionPrefix string, teamsToDisplay []TeamInfo) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}

	var rows []telebot.Row
	var currentRow []telebot.Btn

	for _, t := range teamsToDisplay {
		payload := actionPrefix + "|" + t.ID + "|" + t.Name
		currentRow = append(currentRow, menu.Data(t.Name, actionPrefix, payload))

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
