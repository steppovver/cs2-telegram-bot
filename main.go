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
	{ID: "3212", Name: "FaZe"},
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

	// Запускаем воркер напоминаний
	go startMatchReminders(b, db)

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
		userID := c.Sender().ID
		subs, _ := db.GetUserSubscriptions(userID) // Получаем подписки

		menu := buildTeamsKeyboard("sub_", SupportedTeams, subs)
		return c.Send("Выбери команды для получения уведомлений (нажми, чтобы подписаться/отписаться):", menu)
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
			// Используем %d для Unix-времени и dt для короткой даты и времени.
			// Внутри тега — fallback на случай старого клиента Telegram.
			unixTime := match.Time.Unix()
			fallback := match.Time.UTC().Format("02.01 15:04 UTC")
			timeStr := fmt.Sprintf(`<tg-time unix="%d" format="dt">%s</tg-time>`, unixTime, fallback)

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

	// === ИНЛАЙН ПОДПИСКА (TOGGLE) ===
	b.Handle("\fsub_", func(c telebot.Context) error {
		payload := c.Callback().Data
		parts := strings.Split(payload, "|")
		if len(parts) != 3 {
			return c.Respond(&telebot.CallbackResponse{Text: "Ошибка формата данных."})
		}

		teamName := strings.ToUpper(parts[2])
		userID := c.Sender().ID

		// 1. Смотрим, подписан ли пользователь сейчас
		subs, _ := db.GetUserSubscriptions(userID)
		isSubbed := false
		for _, s := range subs {
			if strings.EqualFold(s, teamName) {
				isSubbed = true
				break
			}
		}

		var toastMsg string
		// 2. Меняем статус
		if isSubbed {
			if err := db.Unsubscribe(userID, teamName); err != nil {
				return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при отписке."})
			}
			toastMsg = fmt.Sprintf("Отписка от %s", teamName)
		} else {
			if err := db.Subscribe(userID, teamName); err != nil {
				return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при подписке."})
			}
			toastMsg = fmt.Sprintf("Подписка на %s оформлена!", teamName)
		}

		// 3. Отправляем красивое всплывающее уведомление сверху экрана
		c.Respond(&telebot.CallbackResponse{Text: toastMsg})

		// 4. Получаем ОБНОВЛЕННЫЙ список подписок и перерисовываем клавиатуру
		newSubs, _ := db.GetUserSubscriptions(userID)
		menu := buildTeamsKeyboard("sub_", SupportedTeams, newSubs)

		// 5. Магия! Редактируем сообщение (c.Edit вместо c.Send)
		// Текст останется тем же, а клавиатура обновится, и галочка появится/исчезнет мгновенно
		return c.Edit("Выбери команды для получения уведомлений (нажми, чтобы подписаться/отписаться):", menu)
	})

	log.Println("Бот успешно запущен!")
	b.Start()
}

// === ФОНОВЫЙ ОПРОС И УВЕДОМЛЕНИЯ ===
func startPoller(bot *telebot.Bot, db *storage.Storage, pandaToken string) {
	updateRoutine := func() {
		log.Println("Обновляем список подписок!")
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
			isNew, timeChanged, teamsChanged, oldTime, oldTeamA, oldTeamB, err := db.ProcessMatch(match)
			if err != nil {
				log.Printf("Ошибка сохранения матча %d: %v", match.ID, err)
				continue
			}

			// Если ничего не изменилось — идем дальше
			if !isNew && !timeChanged && !teamsChanged {
				continue
			}

			if match.TeamA == "TBD" || match.TeamB == "TBD" {
				// Матч сохранен в базу, он появится в расписании, но мы не спамим в чат.
				continue
			}

			// Формируем красивое уведомление
			var msg string

			// Текущее время матча для всех часовых поясов
			unixTime := match.Time.Unix()
			fallbackTime := match.Time.UTC().Format("15:04 02.01 UTC")
			timeStr := fmt.Sprintf(`<tg-time unix="%d" format="dt">%s</tg-time>`, unixTime, fallbackTime)

			if isNew {
				msg = fmt.Sprintf("🆕 <b>Добавлен новый матч!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n⏰ Время: %s",
					match.TeamA, match.TeamB, timeStr)
			} else if teamsChanged {
				timeText := fmt.Sprintf("⏰ Время: %s", timeStr)

				if timeChanged {
					oldUnix := oldTime.Unix()
					oldFallback := oldTime.UTC().Format("15:04 02.01 UTC")
					oldTimeStr := fmt.Sprintf(`<tg-time unix="%d" format="dt">%s</tg-time>`, oldUnix, oldFallback)
					timeText = fmt.Sprintf("<s>Время: %s</s>\n⏰ Новое: %s", oldTimeStr, timeStr)
				}

				msg = fmt.Sprintf("🔄 <b>Определился соперник!</b>\n\n<s>%s vs %s</s>\n🛡 <b>%s</b> vs <b>%s</b>\n%s",
					oldTeamA, oldTeamB, match.TeamA, match.TeamB, timeText)
			} else if timeChanged {
				oldUnix := oldTime.Unix()
				oldFallback := oldTime.UTC().Format("15:04 02.01 UTC")
				oldTimeStr := fmt.Sprintf(`<tg-time unix="%d" format="dt">%s</tg-time>`, oldUnix, oldFallback)

				msg = fmt.Sprintf("⚠️ <b>Время матча изменено!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n<s>Старое время: %s</s>\n⏰ Новое время: %s",
					match.TeamA, match.TeamB, oldTimeStr, timeStr)
			}

			// Находим всех, кому это интересно
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

func startMatchReminders(bot *telebot.Bot, db *storage.Storage) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		// База сама фильтрует матчи, которые начнутся в ближайшие 5 минут
		matches, err := db.GetMatchesForReminder()
		if err != nil {
			log.Printf("Ошибка получения матчей для напоминаний: %v", err)
			continue
		}

		for _, match := range matches {
			if match.TeamA == "TBD" || match.TeamB == "TBD" {
				continue
			}

			// Сразу помечаем матч в БД, чтобы избежать дублей, если рассылка займет время
			db.MarkMatchAsNotified(match.ID)

			unixTime := match.Time.Unix()
			fallback := match.Time.UTC().Format("15:04 UTC")
			timeStr := fmt.Sprintf(`<tg-time unix="%d" format="t">%s</tg-time>`, unixTime, fallback)

			msg := fmt.Sprintf("🔥 <b>Матч начнется с минуты на минуту!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\nНачало в %s",
				match.TeamA, match.TeamB, timeStr)

			usersA, _ := db.GetUsersByTeam(match.TeamA)
			usersB, _ := db.GetUsersByTeam(match.TeamB)

			uniqueUsers := make(map[int64]bool)
			for _, u := range usersA {
				uniqueUsers[u] = true
			}
			for _, u := range usersB {
				uniqueUsers[u] = true
			}

			for userID := range uniqueUsers {
				bot.Send(telebot.ChatID(userID), msg, telebot.ModeHTML)
			}
		}
	}
}

func buildTeamsKeyboard(actionPrefix string, teamsToDisplay []TeamInfo, userSubs []string) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}

	var rows []telebot.Row
	var currentRow []telebot.Btn

	// Вспомогательная функция проверки подписки
	isSubscribed := func(team string) bool {
		for _, s := range userSubs {
			if strings.EqualFold(s, team) {
				return true
			}
		}
		return false
	}

	for _, t := range teamsToDisplay {
		payload := actionPrefix + "|" + t.ID + "|" + t.Name

		btnText := t.Name
		if isSubscribed(t.Name) {
			btnText = "✅ " + t.Name
		}

		currentRow = append(currentRow, menu.Data(btnText, actionPrefix, payload))

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
