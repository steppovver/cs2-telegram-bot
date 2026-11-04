package bot

import (
	"context"
	"log/slog"
	"time"
)

type reminderKey struct {
	id    int
	begin int64
}

func (b *Bot) reminderQueued(key reminderKey, userID int64) bool {
	b.reminderMu.Lock()
	defer b.reminderMu.Unlock()
	_, ok := b.reminderSent[key][userID]
	return ok
}

func (b *Bot) noteReminder(key reminderKey, userID int64) {
	b.reminderMu.Lock()
	defer b.reminderMu.Unlock()
	if b.reminderSent == nil {
		b.reminderSent = make(map[reminderKey]map[int64]struct{})
	}
	if b.reminderSent[key] == nil {
		b.reminderSent[key] = make(map[int64]struct{})
	}
	b.reminderSent[key][userID] = struct{}{}
}

func (b *Bot) clearReminder(key reminderKey) {
	b.reminderMu.Lock()
	delete(b.reminderSent, key)
	b.reminderMu.Unlock()
}

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

		build := func(_ int, favA, favB bool) string {
			return "🔥 <b>Матч начнется с минуты на минуту!</b>\n\n" +
				formatMatchCard(match, match.TeamA, match.TeamB, pushMark(favA), pushMark(favB), true, nil)
		}

		key := reminderKey{id: match.ID, begin: match.Time.Unix()}
		switch b.broadcastToFans(ctx, match, build, func(userID int64) bool {
			return b.reminderQueued(key, userID)
		}, func(userID int64) {
			b.noteReminder(key, userID)
		}) {
		case fanSendNone:
			continue
		case fanSendRetry:
			slog.Warn("Напоминание не поставлено в очередь, повтор на следующем цикле",
				slog.Int("match_id", match.ID))
			continue
		}

		marked, err := b.storage.MarkMatchAsNotified(match.ID, match.Time)
		if err != nil {
			slog.Error("Ошибка отметки матча как уведомленного", slog.Int("match_id", match.ID), slog.Any("error", err))
			continue
		}
		if !marked {
			continue
		}
		b.clearReminder(key)
		queued++
	}

	if queued > 0 {
		slog.Info("Цикл напоминаний", slog.Int("queued", queued))
	}
}
