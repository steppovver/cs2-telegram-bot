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

// maxFloodWait — дольше этого FloodWait сообщение снимается с очереди.
// Лимит Telegram глобальный, поэтому ждать нужно весь retry_after,
// но не дольше этого порога и не дольше остановки процесса.
const maxFloodWait = 15 * time.Minute

func (b *Bot) StartBroadcaster(ctx context.Context) {
	// Telegram разрешает 30 сообщений в секунду (глобально).
	// Ограничиваем до 25 (тик каждые 40 мс) для надежности.
	limiter := time.NewTicker(40 * time.Millisecond)
	defer limiter.Stop()

	send := func(task BroadcastTask) {
		// NoPreview глобально: ссылки на стримы не должны тянуть превью видео.
		if _, err := b.telebot.Send(telebot.ChatID(task.UserID), task.Text, telebot.ModeHTML, telebot.NoPreview); err != nil {
			// FloodWait: Telegram просит подождать — ждем RetryAfter и
			// повторяем один раз. Сон здесь намеренный: лимит глобальный,
			// слать раньше нельзя, очередь подождет.
			var flood telebot.FloodError
			if errors.As(err, &flood) {
				wait := time.Duration(flood.RetryAfter+1) * time.Second
				if wait > maxFloodWait {
					slog.Warn("FloodWait слишком длинный, сообщение снято",
						slog.Int64("user_id", task.UserID),
						slog.Duration("retry_after", wait))
					return
				}
				slog.Warn("FloodWait от Telegram, ждем и повторяем",
					slog.Int64("user_id", task.UserID),
					slog.Duration("wait", wait))
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if _, err := b.telebot.Send(telebot.ChatID(task.UserID), task.Text, telebot.ModeHTML, telebot.NoPreview); err != nil {
					if isUserGoneError(err) {
						b.dropGoneUser(task.UserID, err)
						return
					}
					slog.Warn("Повторная отправка после FloodWait не удалась",
						slog.Int64("user_id", task.UserID),
						slog.String("error", b.redactSendErr(err)))
				}
				return
			}
			// Мертвый получатель (бан бота, удаленный/деактивированный
			// аккаунт): удаляем пользователя, иначе будем слать в пустоту
			// вечно. Подписки и настройки чистятся каскадом по FK.
			if isUserGoneError(err) {
				b.dropGoneUser(task.UserID, err)
				return
			}
			slog.Warn("Ошибка отправки", slog.Int64("user_id", task.UserID), slog.String("error", b.redactSendErr(err)))
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

func (b *Bot) dropGoneUser(userID int64, err error) {
	if derr := b.storage.RemoveUser(userID); derr != nil {
		slog.Warn("Не удалось удалить мертвого получателя",
			slog.Int64("user_id", userID), slog.String("error", b.redactSendErr(derr)))
		return
	}
	slog.Info("Удален мертвый получатель",
		slog.Int64("user_id", userID), slog.String("reason", b.redactSendErr(err)))
}

func (b *Bot) redactSendErr(err error) string {
	token := ""
	if b.telebot != nil {
		token = b.telebot.Token
	}
	return redactToken(err, token)
}

// enqueueToUser неблокирующе кладет тексты одному юзеру в очередь рассылки,
// нарезая каждый через chunkLines. Возвращает ok=false при отмене контекста
// и число отброшенных чанков (очередь полна). Лог с контекстом
// (какой матч/дайджест) — задача вызывающего.
func (b *Bot) enqueueToUser(ctx context.Context, userID int64, texts ...string) (ok bool, dropped int) {
	for _, text := range texts {
		for _, chunk := range chunkLines(text, tgChunkLimit) {
			// Быстрая проверка отмены без блокировки воркера.
			select {
			case <-ctx.Done():
				return false, dropped
			default:
			}

			select {
			case b.broadcastCh <- BroadcastTask{UserID: userID, Text: chunk}:
			default:
				dropped++
			}
		}
	}
	return true, dropped
}

type fanSend int

const (
	fanSendNone fanSend = iota // любимых нет, флаг notified не ставим
	fanSendOK
	fanSendRetry
)

// broadcastToFans ставит напоминание тем, у кого в матче есть любимая команда.
// Интересная подписка напоминание не получает.
// skip пропускает уже поставленных в очередь. note запоминает успешную постановку.
func (b *Bot) broadcastToFans(ctx context.Context, match domain.Match, build func(offset int, favA, favB bool) string, skip func(int64) bool, note func(int64)) fanSend {
	fans, err := b.storage.GetMatchFans(match.TeamAID, match.TeamBID)
	if err != nil {
		slog.Error("Ошибка получения подписчиков матча",
			slog.String("team_a", match.TeamA),
			slog.String("team_b", match.TeamB),
			slog.Any("error", err))
		return fanSendRetry
	}

	favs := make([]domain.MatchFan, 0, len(fans))
	for _, f := range fans {
		if f.HasFavorite() {
			favs = append(favs, f)
		}
	}
	if len(favs) == 0 {
		return fanSendNone
	}

	ids := make([]int64, len(favs))
	for i, f := range favs {
		ids[i] = f.UserID
	}
	offsets, err := b.storage.GetUserOffsets(ids)
	if err != nil {
		slog.Error("Ошибка получения часовых поясов", slog.Any("error", err))
		if offsets == nil {
			offsets = make(map[int64]int, len(ids))
		}
	}

	type fanKey struct {
		off  int
		favA bool
		favB bool
	}
	groups := make(map[fanKey][]int64)
	for _, f := range favs {
		k := fanKey{off: offsets[f.UserID], favA: f.FavoriteA, favB: f.FavoriteB}
		groups[k] = append(groups[k], f.UserID)
	}

	slog.Info("Добавление в очередь рассылки",
		slog.String("match", fmt.Sprintf("%s vs %s", match.TeamA, match.TeamB)),
		slog.Int("recipients", len(favs)),
		slog.Int("offset_groups", len(groups)))

	pending := 0
	for k, group := range groups {
		msg := build(k.off, k.favA, k.favB)
		for _, userID := range group {
			if skip != nil && skip(userID) {
				continue
			}
			pending++
			ok, d := b.enqueueToUser(ctx, userID, msg)
			if !ok || d > 0 {
				slog.Warn("Очередь рассылки переполнена, часть уведомлений отброшена",
					slog.String("match", fmt.Sprintf("%s vs %s", match.TeamA, match.TeamB)),
					slog.Int("dropped", d),
					slog.Int("recipients", len(favs)),
					slog.Int("queue_len", len(b.broadcastCh)),
				)
				return fanSendRetry
			}
			if note != nil {
				note(userID)
			}
		}
	}
	if pending == 0 {
		return fanSendOK
	}
	return fanSendOK
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
