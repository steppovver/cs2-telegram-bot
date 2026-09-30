package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/domain"
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
	for _, u := range users {
		select {
		case <-ctx.Done():
			return
		default:
		}
		b.processDigestUser(ctx, u.UserID, u.UtcOffset, time.Now(), until, slot)
	}
	slog.Debug("Цикл дайджеста завершен", slog.Int("due", len(users)))
}

// processDigestUser готовит и ставит в очередь персональный дайджест одного юзера.
// Пустой результат тоже фиксирует как отправленный, чтобы не дергать БД весь час.
func (b *Bot) processDigestUser(ctx context.Context, userID int64, utcOffset int, now, until time.Time, slot string) {
	matches, err := b.storage.GetDigestMatches(userID, now.Unix(), until.Unix())
	if err != nil {
		slog.Error("Ошибка получения матчей для дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
		return
	}
	slog.Debug("Матчи для дайджеста",
		slog.Int64("user_id", userID),
		slog.Int("found", len(matches)))

	subs, _ := b.storage.GetUserSubscriptions(userID)
	lines := buildDigestLines(matches, subs, utcOffset)

	if len(lines) == 0 {
		slog.Debug("Дайджест пуст, матчей на 24 часа нет",
			slog.Int64("user_id", userID))
		b.markDigestSent(userID, slot)
		return
	}

	text := "⏰ <b>Матчи ваших команд на 24 часа:</b>\n\n" + strings.Join(lines, "\n") + "\n"
	ok, dropped := b.enqueueToUser(ctx, userID, text)
	if !ok || dropped > 0 {
		slog.Warn("Дайджест не поставлен в очередь (переполнение), повтор на следующем тике",
			slog.Int64("user_id", userID))
		return
	}
	b.markDigestSent(userID, slot)
	slog.Debug("Дайджест поставлен в очередь",
		slog.Int64("user_id", userID),
		slog.Int("lines", len(lines)))
}

// buildDigestLines фильтрует матчи без соперника/времени и форматирует
// их одной строкой на матч, подсвечивая команды пользователя.
// Время приводим в пояс юзера только на рендере, бизнес-логика в UTC.
func buildDigestLines(matches []domain.Match, subs []domain.TeamInfo, utcOffset int) []string {
	var lines []string
	for _, m := range matches {
		if m.TeamA == "TBD" || m.TeamB == "TBD" || m.Time.IsZero() {
			continue
		}
		timeStr := formatTGTime(m.Time, "dt", "02.01 15:04", utcOffset)
		teamA, teamB := html.EscapeString(m.TeamA), html.EscapeString(m.TeamB)
		for _, sub := range subs {
			if sub.ID == m.TeamAID {
				teamA = "<b>" + teamA + "</b>"
			}
			if sub.ID == m.TeamBID {
				teamB = "<b>" + teamB + "</b>"
			}
		}
		lines = append(lines, fmt.Sprintf("%s | %s vs %s", timeStr, teamA, teamB))
	}
	return lines
}

func (b *Bot) markDigestSent(userID int64, slot string) {
	if err := b.storage.MarkDigestSent(userID, slot); err != nil {
		slog.Error("Ошибка отметки дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
	}
}
