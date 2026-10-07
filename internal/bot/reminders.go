package bot

import (
	"context"
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
	queued := 0
	for _, match := range matches {
		if match.TeamA == "TBD" || match.TeamB == "TBD" {
			continue
		}

		escA := "<b>" + html.EscapeString(match.TeamA) + "</b>"
		escB := "<b>" + html.EscapeString(match.TeamB) + "</b>"
		build := func(off int) string {
			return "🔥 <b>Матч начнется с минуты на минуту!</b>\n\n" +
				formatMatchCard(match, escA, escB, off, b.maxStreams)
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
		queued++
	}

	if queued > 0 {
		slog.Info("Цикл напоминаний", slog.Int("queued", queued))
	}
}
