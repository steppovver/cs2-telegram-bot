package bot

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"cs2bot/internal/domain"

	"gopkg.in/telebot.v3"
)

const noSubscriptionsText = "Вы еще не подписаны ни на одну команду.\nНажмите «🔔 Подписки на команды»."

// viewKind — экран с кнопкой показать/скрыть турнирные матчи.
type viewKind string

const (
	viewSchedule viewKind = "sched"
	viewScore    viewKind = "score"
)

// tournamentViewDefault читает режим по умолчанию; ошибка = скрывать.
func (b *Bot) tournamentViewDefault(userID int64) bool {
	showAll, err := b.storage.GetTournamentView(userID)
	if err != nil {
		slog.Error("Ошибка получения режима турнирных матчей", slog.Int64("user_id", userID), slog.Any("error", err))
		return false
	}
	return showAll
}

func isFanMatch(m domain.Match, subs []domain.TeamInfo) bool {
	return isTeamSubscribed(subs, m.TeamAID) || isTeamSubscribed(subs, m.TeamBID)
}

func fanMatchesOnly(matches []domain.Match, subs []domain.TeamInfo) []domain.Match {
	var out []domain.Match
	for _, m := range matches {
		if isFanMatch(m, subs) {
			out = append(out, m)
		}
	}
	return out
}

func countExtraMatches(matches []domain.Match, subs []domain.TeamInfo) int {
	n := 0
	for _, m := range matches {
		if !isFanMatch(m, subs) {
			n++
		}
	}
	return n
}

// viewToggleMarkup — кнопка под расписанием/счетом. Нет турнирных
// матчей — нет кнопки (nil).
func viewToggleMarkup(kind viewKind, showAll bool, extra int) *telebot.ReplyMarkup {
	if extra == 0 {
		return nil
	}
	menu := &telebot.ReplyMarkup{}
	text := fmt.Sprintf("🏆 Показать турнирные матчи (%d)", extra)
	data := string(kind) + "_all"
	if showAll {
		text = "🙈 Скрыть турнирные матчи"
		data = string(kind) + "_mine"
	}
	menu.Inline(menu.Row(menu.Data(text, "view", data)))
	return menu
}

func (b *Bot) handleViewCallback(c telebot.Context) error {
	if c.Callback() == nil || c.Sender() == nil {
		return nil
	}
	userID := c.Sender().ID
	data := strings.TrimSpace(c.Callback().Data)
	kind, mode, ok := strings.Cut(data, "_")
	if !ok || (mode != "all" && mode != "mine") {
		return c.Respond(&telebot.CallbackResponse{Text: "Неизвестное действие."})
	}
	showAll := mode == "all"

	var text string
	var extra int
	var err error
	switch viewKind(kind) {
	case viewSchedule:
		text, extra, err = b.buildScheduleView(userID, showAll)
	case viewScore:
		text, extra, err = b.buildScoreView(userID, showAll)
	default:
		return c.Respond(&telebot.CallbackResponse{Text: "Неизвестное действие."})
	}
	if err != nil {
		slog.Error("Ошибка переключения турнирных матчей", slog.Int64("user_id", userID), slog.String("view", data), slog.Any("error", err))
		return c.Respond(&telebot.CallbackResponse{Text: "Не удалось загрузить матчи."})
	}
	_ = c.Respond()

	markup := viewToggleMarkup(viewKind(kind), showAll, extra)
	// Длинный текст в одно сообщение не влезет: шлем заново пачкой.
	if c.Message() == nil || len(chunkLines(text, tgChunkLimit)) > 1 {
		return b.sendChunked(c, text, markup)
	}
	err = c.Edit(text, markup, telebot.ModeHTML, telebot.NoPreview)
	if err == nil || errors.Is(err, telebot.ErrSameMessageContent) || errors.Is(err, telebot.ErrMessageNotModified) {
		return nil
	}
	slog.Warn("Не удалось отредактировать сообщение, шлем новое", slog.Int64("user_id", userID), slog.Any("error", err))
	return b.sendChunked(c, text, markup)
}

func (b *Bot) handleTournamentsMenu(c telebot.Context) error {
	if c.Sender() == nil {
		return nil
	}
	text, markup, err := b.buildTournamentsMenu(c.Sender().ID)
	if err != nil {
		slog.Error("Ошибка получения турниров", slog.Int64("user_id", c.Sender().ID), slog.Any("error", err))
		return c.Send("Не удалось загрузить турниры. Попробуйте позже.", telebot.NoPreview)
	}
	return c.Send(text, markup, telebot.ModeHTML, telebot.NoPreview)
}

func (b *Bot) buildTournamentsMenu(userID int64) (string, *telebot.ReplyMarkup, error) {
	tournaments, err := b.storage.GetTournamentsForUser(userID)
	if err != nil {
		return "", nil, err
	}
	showAll, err := b.storage.GetTournamentView(userID)
	if err != nil {
		return "", nil, err
	}
	return tournamentsMenuText(len(tournaments) == 0, showAll), buildTournamentsKeyboard(tournaments, showAll), nil
}

func tournamentsMenuText(empty, showAll bool) string {
	var sb strings.Builder
	sb.WriteString("🏆 <b>Подписки на турниры</b>\n\n")
	sb.WriteString("Здесь турниры, в которых играли, играют или будут играть ваши команды. ")
	sb.WriteString("Подписка добавляет матчи плей-офф: они появятся в расписании, счете и дайджесте, ")
	sb.WriteString("а о начале каждого матча придет уведомление. Других уведомлений по ним не будет.\n\n")
	if showAll {
		sb.WriteString("Сейчас расписание, счет и дайджест показывают матчи ваших команд и матчи плей-офф ваших турниров.\n")
		sb.WriteString("Кнопка «🙈 Скрыть турнирные матчи» под сообщением скрывает матчи турниров в этом сообщении.\n")
		sb.WriteString("Кнопка «Показывать только мои команды» выключает показ матчей плей-офф в расписании, счете и дайджесте.")
	} else {
		sb.WriteString("Сейчас расписание, счет и дайджест показывают только матчи ваших команд.\n")
		sb.WriteString("Кнопка «Показать турнирные матчи» под сообщением показывает матчи плей-офф ваших турниров в этом сообщении.\n")
		sb.WriteString("Кнопка «🙈 Показывать матчи турниров сразу» включает показ матчей плей-офф в расписании, счете и дайджесте.")
	}
	if empty {
		sb.WriteString("\n\nПока нет турниров с вашими командами. Подпишитесь на команды в «🔔 Подписки на команды».")
	}
	return sb.String()
}

func buildTournamentsKeyboard(tournaments []domain.TournamentInfo, showAll bool) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, t := range tournaments {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = fmt.Sprintf("Турнир %d", t.ID)
		}
		if t.Subscribed {
			name = "✅ " + name
		}
		rows = append(rows, menu.Row(menu.Data(name, "tsub", "t_"+strconv.Itoa(t.ID))))
	}
	// Подпись — действие по нажатию, а не текущее состояние: иначе кнопка
	// читается как статус и дублирует абзац выше.
	defaultText := "🙈 Показывать матчи турниров сразу"
	if showAll {
		defaultText = "Показывать только мои команды"
	}
	rows = append(rows, menu.Row(menu.Data(defaultText, "tsub", "default")))
	menu.Inline(rows...)
	return menu
}

func (b *Bot) handleTournamentCallback(c telebot.Context) error {
	if c.Callback() == nil || c.Sender() == nil {
		return nil
	}
	userID := c.Sender().ID
	data := strings.TrimSpace(c.Callback().Data)

	var toast string
	switch {
	case data == "default":
		showAll, err := b.storage.GetTournamentView(userID)
		if err != nil {
			slog.Error("Ошибка получения режима турнирных матчей", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Внутренняя ошибка. Попробуйте позже."})
		}
		if err := b.storage.SetTournamentView(userID, !showAll); err != nil {
			slog.Error("Ошибка изменения режима турнирных матчей", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить."})
		}
		toast = "Турнирные матчи скрыты по умолчанию."
		if !showAll {
			toast = "Турнирные матчи показываются по умолчанию."
		}

	case strings.HasPrefix(data, "t_"):
		id, err := strconv.Atoi(strings.TrimPrefix(data, "t_"))
		if err != nil || id <= 0 {
			return c.Respond(&telebot.CallbackResponse{Text: "Ошибка идентификатора турнира."})
		}
		tournaments, err := b.storage.GetTournamentsForUser(userID)
		if err != nil {
			slog.Error("Ошибка получения турниров", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Внутренняя ошибка. Попробуйте позже."})
		}
		var target *domain.TournamentInfo
		for i := range tournaments {
			if tournaments[i].ID == id {
				target = &tournaments[i]
				break
			}
		}
		if target == nil {
			toast = "Турнир уже закончился."
			break
		}
		if target.Subscribed {
			err = b.storage.UnsubscribeTournament(userID, id)
			toast = "Отписка от " + target.Name
		} else {
			err = b.storage.SubscribeTournament(userID, id)
			toast = fmt.Sprintf("Подписка на %s оформлена!", target.Name)
		}
		if err != nil {
			slog.Error("Ошибка изменения подписки на турнир", slog.Int64("user_id", userID), slog.Int("tournament_id", id), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить изменения."})
		}
		slog.Info("Подписка на турнир изменена",
			slog.Int64("user_id", userID),
			slog.Int("tournament_id", id),
			slog.String("tournament", target.Name),
			slog.Bool("subscribed", !target.Subscribed))

	default:
		return c.Respond(&telebot.CallbackResponse{Text: "Неизвестное действие."})
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: toast})
	if c.Message() == nil {
		return nil
	}
	text, markup, err := b.buildTournamentsMenu(userID)
	if err != nil {
		slog.Error("Ошибка получения турниров", slog.Int64("user_id", userID), slog.Any("error", err))
		return nil
	}
	err = c.Edit(text, markup, telebot.ModeHTML, telebot.NoPreview)
	if err != nil && !errors.Is(err, telebot.ErrSameMessageContent) && !errors.Is(err, telebot.ErrMessageNotModified) {
		return fmt.Errorf("обновление меню турниров: %w", err)
	}
	return nil
}
