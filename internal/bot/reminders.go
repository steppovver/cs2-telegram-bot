package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"
)

func (b *Bot) StartMatchReminders(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Воркер напоминаний завершил работу")
			return
		case <-ticker.C:
			b.runRemindersCycle(ctx)
		}
	}
}

func (b *Bot) runRemindersCycle(ctx context.Context) {
	matches, err := b.storage.GetMatchesForReminder()
	if err != nil {
		slog.Error("Ошибка получения матчей для напоминаний", slog.Any("error", err))
		return
	}

	for _, match := range matches {
		if match.TeamA == "TBD" || match.TeamB == "TBD" {
			continue
		}

		escA, escB := html.EscapeString(match.TeamA), html.EscapeString(match.TeamB)
		build := func(off int) string {
			timeStr := formatTGTime(match.Time, "t", "15:04", off)
			return fmt.Sprintf("🔥 <b>Матч начнется с минуты на минуту!</b>\n\n🎮 <b>%s</b> vs <b>%s</b>\nНачало в %s\n%s",
				escA, escB, timeStr, streamLine(match, b.maxStreams))
		}

		if !b.broadcastToFans(ctx, match, build) {
			slog.Warn("Напоминание не поставлено в очередь, повтор на следующем цикле",
				slog.Int("match_id", match.ID))
			continue
		}

		if err := b.storage.MarkMatchAsNotified(match.ID); err != nil {
			slog.Error("Ошибка отметки матча как уведомленного", slog.Int("match_id", match.ID), slog.Any("error", err))
			continue
		}
	}
}
