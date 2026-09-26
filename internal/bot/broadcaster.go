package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/domain"

	"gopkg.in/telebot.v3"
)

// maxFloodWait ограничивает ожидание по FloodWait: дольше ждать в одном
// горутине рассылки бессмысленно, сообщение будет дропнуто с варном.
const maxFloodWait = 2 * time.Minute

func (b *Bot) StartBroadcaster(ctx context.Context) {
	// Telegram разрешает 30 сообщений в секунду (глобально).
	// Ограничиваем до 25 (тик каждые 40 мс) для надежности.
	limiter := time.NewTicker(40 * time.Millisecond)
	defer limiter.Stop()

	send := func(task BroadcastTask) {
		if _, err := b.telebot.Send(telebot.ChatID(task.UserID), task.Text, telebot.ModeHTML); err != nil {
			// FloodWait: Telegram просит подождать — ждем RetryAfter и
			// повторяем один раз. Сон здесь намеренный: лимит глобальный,
			// слать раньше нельзя, очередь подождет.
			var flood telebot.FloodError
			if errors.As(err, &flood) {
				wait := time.Duration(flood.RetryAfter+1) * time.Second
				if wait > maxFloodWait {
					wait = maxFloodWait
				}
				slog.Warn("FloodWait от Telegram, ждем и повторяем",
					slog.Int64("user_id", task.UserID),
					slog.Duration("wait", wait))
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
				if _, err := b.telebot.Send(telebot.ChatID(task.UserID), task.Text, telebot.ModeHTML); err != nil {
					slog.Warn("Повторная отправка после FloodWait не удалась",
						slog.Int64("user_id", task.UserID), slog.Any("error", err))
				}
				return
			}
			// Мертвый получатель (бан бота, удаленный/деактивированный
			// аккаунт): удаляем пользователя, иначе будем слать в пустоту
			// вечно. Подписки и настройки чистятся каскадом по FK.
			if isUserGoneError(err) {
				if derr := b.storage.RemoveUser(task.UserID); derr != nil {
					slog.Warn("Не удалось удалить мертвого получателя",
						slog.Int64("user_id", task.UserID), slog.Any("error", derr))
				} else {
					slog.Info("Удален мертвый получатель",
						slog.Int64("user_id", task.UserID), slog.String("reason", err.Error()))
				}
				return
			}
			slog.Warn("Ошибка отправки", slog.Int64("user_id", task.UserID), slog.Any("error", err))
		}
	}

	for {
		select {
		case <-ctx.Done():
			// Graceful drain: отправляем остатки с тем же лимитом,
			// но не дольше таймаута, чтобы не висеть на SIGTERM вечно.
			// 10000 задач * 40мс = ~400с, поэтому полная доставка
			// негарантирована — остаток логируем как дропнутый.
			slog.Info("Воркер рассылок: дренируем очередь",
				slog.Int("queue_len", len(b.broadcastCh)))
			drainTimeout := time.NewTimer(10 * time.Second)
			defer drainTimeout.Stop()
			drained, dropped := 0, 0
		drainLoop:
			for {
				select {
				case task := <-b.broadcastCh:
					select {
					case <-limiter.C:
						send(task)
						drained++
					case <-drainTimeout.C:
						dropped = len(b.broadcastCh) + 1
						break drainLoop
					}
				default:
					break drainLoop
				}
			}
			slog.Info("Воркер рассылок завершил работу",
				slog.Int("drained", drained),
				slog.Int("dropped", dropped))
			return
		case task := <-b.broadcastCh:
			<-limiter.C // Ждем разрешения от тикера перед отправкой
			send(task)
		}
	}
}

func (b *Bot) broadcastToFans(ctx context.Context, match domain.Match, build func(offset int) string) bool {
	users, err := b.storage.GetUsersByTeamIDs(match.TeamAID, match.TeamBID)
	if err != nil {
		slog.Error("Ошибка получения подписчиков матча",
			slog.String("team_a", match.TeamA),
			slog.String("team_b", match.TeamB),
			slog.Any("error", err))
		return false
	}

	if len(users) == 0 {
		return true
	}

	offsets, err := b.storage.GetUserOffsets(users)
	if err != nil {
		slog.Error("Ошибка получения часовых поясов", slog.Any("error", err))
		offsets = make(map[int64]int, len(users))
	}

	// Группируем по оффсету: один текст на группу, бизнес-логика в UTC,
	// в пояс приводим только на рендере.
	byOffset := make(map[int][]int64)
	for _, userID := range users {
		off := offsets[userID]
		byOffset[off] = append(byOffset[off], userID)
	}

	slog.Info("Добавление в очередь рассылки",
		slog.String("match", fmt.Sprintf("%s vs %s", match.TeamA, match.TeamB)),
		slog.Int("recipients", len(users)),
		slog.Int("offset_groups", len(byOffset)))

	dropped := 0
	for off, group := range byOffset {
		msg := build(off)
		for _, userID := range group {
			// Быстрая проверка отмены без блокировки поллера.
			select {
			case <-ctx.Done():
				return false
			default:
			}

			select {
			case b.broadcastCh <- BroadcastTask{UserID: userID, Text: msg}:
			default:
				dropped++
			}
		}
	}

	if dropped > 0 {
		slog.Warn("Очередь рассылки переполнена, часть уведомлений отброшена",
			slog.String("match", fmt.Sprintf("%s vs %s", match.TeamA, match.TeamB)),
			slog.Int("dropped", dropped),
			slog.Int("recipients", len(users)),
			slog.Int("queue_len", len(b.broadcastCh)),
		)
		return false
	}
	return true
}

// isUserGoneError определяет, что получатель мертв и слать ему больше
// не стоит: бан бота, деактивация, удаление чата. Сначала сверяемся с
// sentinel-ошибками telebot (возвращаются как есть из extractOk),
// затем — подстроки на случай обернутых/новых формулировок API.
func isUserGoneError(err error) bool {
	for _, sentinel := range []*telebot.Error{
		telebot.ErrBlockedByUser,
		telebot.ErrUserIsDeactivated,
		telebot.ErrNotStartedByUser,
		telebot.ErrChatNotFound,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	for _, sub := range []string{
		"bot was blocked",
		"user is deactivated",
		"chat not found",
		"peer_id_invalid",
		"bot can't initiate",
		"user not found",
	} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}
