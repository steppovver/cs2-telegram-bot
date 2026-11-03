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
	notify, err := b.storage.GetTournamentNotify(userID)
	if err != nil {
		return "", nil, err
	}
	return tournamentsMenuText(len(tournaments) == 0, notify), buildTournamentsKeyboard(tournaments, notify), nil
}

func tournamentsMenuText(empty, notify bool) string {
	var sb strings.Builder
	sb.WriteString("🏆 <b>Подписки на турниры</b>\n\n")
	sb.WriteString("Здесь турниры, в которых играли, играют или будут играть ваши команды. ")
	sb.WriteString("Подписка добавляет матчи плей-офф в расписание, счет и дайджест.\n\n")
	if notify {
		sb.WriteString("Уведомление о начале матча плей-офф включено.\n")
		sb.WriteString("Кнопка «🔕 Выключить уведомления о матчах» отключает это уведомление.")
	} else {
		sb.WriteString("Уведомление о начале матча плей-офф выключено.\n")
		sb.WriteString("Кнопка «🔔 Включить уведомления о матчах» включает это уведомление.")
	}
	if empty {
		sb.WriteString("\n\nПока нет турниров с вашими командами. Подпишитесь на команды в «🔔 Подписки на команды».")
	}
	return sb.String()
}

func buildTournamentsKeyboard(tournaments []domain.TournamentInfo, notify bool) *telebot.ReplyMarkup {
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
	notifyText := "🔔 Включить уведомления о матчах"
	if notify {
		notifyText = "🔕 Выключить уведомления о матчах"
	}
	rows = append(rows, menu.Row(menu.Data(notifyText, "tsub", "notify")))
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
	case data == "notify":
		notify, err := b.storage.GetTournamentNotify(userID)
		if err != nil {
			slog.Error("Ошибка получения уведомлений турниров", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Внутренняя ошибка. Попробуйте позже."})
		}
		if err := b.storage.SetTournamentNotify(userID, !notify); err != nil {
			slog.Error("Ошибка изменения уведомлений турниров", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить."})
		}
		toast = "Уведомления о начале турнирных матчей выключены."
		if !notify {
			toast = "Уведомления о начале турнирных матчей включены."
		}
		slog.Info("Уведомления турниров переключены", slog.Int64("user_id", userID), slog.Bool("enabled", !notify))

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
