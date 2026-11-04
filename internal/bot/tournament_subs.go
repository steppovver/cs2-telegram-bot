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

func (b *Bot) handleTournamentsMenu(c telebot.Context) error {
	if c.Sender() == nil {
		return nil
	}
	b.clearDialog(c.Sender().ID)
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
	return tournamentsMenuText(len(tournaments) == 0), buildTournamentsKeyboard(tournaments), nil
}

func tournamentsMenuText(empty bool) string {
	var sb strings.Builder
	sb.WriteString("🏆 <b>Подписки на турниры</b>\n\n")
	sb.WriteString("Здесь турниры, в которых играли, играют или будут играть ваши команды. ")
	sb.WriteString("Подписка добавляет матчи плей-офф в расписание, счет и дайджест и присылает уведомление о начале такого матча.")
	if empty {
		sb.WriteString("\n\nПока нет турниров с вашими командами. Подпишитесь на команды в «🔔 Подписки на команды».")
	}
	return sb.String()
}

func buildTournamentsKeyboard(tournaments []domain.TournamentInfo) *telebot.ReplyMarkup {
	if len(tournaments) == 0 {
		return nil
	}
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
	// Пустой ReplyMarkup снимает кнопки, если турниров не осталось.
	if markup == nil {
		markup = &telebot.ReplyMarkup{}
	}
	err = c.Edit(text, markup, telebot.ModeHTML, telebot.NoPreview)
	if err != nil && !errors.Is(err, telebot.ErrSameMessageContent) && !errors.Is(err, telebot.ErrMessageNotModified) {
		return fmt.Errorf("обновление меню турниров: %w", err)
	}
	return nil
}
