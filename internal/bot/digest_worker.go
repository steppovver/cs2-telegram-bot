package bot

import (
	"context"
	"log/slog"
	"time"
)

func (b *Bot) StartDailyDigest(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Воркер дайджеста завершил работу")
			return
		case <-ticker.C:
			b.runDigestCycle(ctx)
		}
	}
}

// runDigestCycle работает полностью в UTC: user_digest.hour хранит UTC-час,
// конвертация wall<->UTC — задача хендлеров. В пояс приводим только на рендере.
func (b *Bot) runDigestCycle(ctx context.Context) {
	nowUTC := time.Now().UTC()
	slot := nowUTC.Format("2006-01-02-15")
	slog.Debug("Старт цикла дайджеста",
		slog.String("utc", nowUTC.Format("2006-01-02 15:04")),
		slog.String("slot", slot))

	users, err := b.storage.GetDigestDueUsers(nowUTC.Hour(), slot)
	if err != nil {
		slog.Error("Ошибка получения получателей дайджеста", slog.Any("error", err))
		return
	}
	if len(users) == 0 {
		slog.Debug("Нет получателей дайджеста на этот UTC-час")
		return
	}

	until := time.Now().Add(24 * time.Hour)
	sent, empty := 0, 0
	for _, u := range users {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if b.processDigestUser(ctx, u.UserID, u.UtcOffset, until, slot) {
			sent++
		} else {
			empty++
		}
	}
	slog.Info("Цикл дайджеста",
		slog.Int("due", len(users)),
		slog.Int("sent", sent),
		slog.Int("empty", empty))
}

// processDigestUser готовит и ставит в очередь персональный дайджест одного юзера.
// Пустой результат тоже фиксирует как отправленный, чтобы не дергать БД весь час.
// Возвращает true, если дайджест с матчами поставлен в очередь.
func (b *Bot) processDigestUser(ctx context.Context, userID int64, utcOffset int, until time.Time, slot string) bool {
	live, upcoming, err := b.storage.GetScheduleMatches(userID, until.Unix())
	if err != nil {
		slog.Error("Ошибка получения матчей для дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
		return false
	}
	slog.Debug("Матчи для дайджеста",
		slog.Int64("user_id", userID),
		slog.Int("live", len(live)),
		slog.Int("upcoming", len(upcoming)))

	if len(live) == 0 && len(upcoming) == 0 {
		slog.Debug("Дайджест пуст, матчей на 24 часа нет",
			slog.Int64("user_id", userID))
		b.markDigestSent(userID, slot)
		return false
	}

	subs, _ := b.storage.GetUserSubscriptions(userID)
	text := formatMatchesMessage(
		"⏰ <b>Дайджест на 24 часа:</b>",
		live, upcoming, subs, utcOffset, b.maxStreams,
	)
	ok, dropped := b.enqueueToUser(ctx, userID, text)
	if !ok || dropped > 0 {
		slog.Warn("Дайджест не поставлен в очередь (переполнение), повтор на следующем тике",
			slog.Int64("user_id", userID))
		return false
	}
	b.markDigestSent(userID, slot)
	slog.Info("Дайджест поставлен в очередь",
		slog.Int64("user_id", userID),
		slog.Int("live", len(live)),
		slog.Int("upcoming", len(upcoming)))
	return true
}

func (b *Bot) markDigestSent(userID int64, slot string) {
	if err := b.storage.MarkDigestSent(userID, slot); err != nil {
		slog.Error("Ошибка отметки дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
	}
}
