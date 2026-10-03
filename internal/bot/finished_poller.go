package bot

import (
	"context"
	"log/slog"
	"time"

	"cs2bot/internal/config"
)

// finishedPollWindow — глубина окна finished-фетча. Совпадает с горизонтом
// CleanOldMatches (24 часа): старше в БД все равно ничего не лежит.
const finishedPollWindow = 24 * time.Hour

// StartFinishedPoller фоново подтягивает завершенные матчи подписанных
// команд для кнопки счета. Апсерты молчаливые: смена счета — не событие
// для рассылки (спойлеры только по явному запросу через 📊).
func (b *Bot) StartFinishedPoller(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Duration(config.DefaultFinishedPollIntervalSec) * time.Second
	}
	slog.Info("Воркер завершенных матчей запущен", slog.Duration("interval", interval))
	b.runFinishedCycle(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Воркер завершенных матчей завершил работу")
			return
		case <-ticker.C:
			b.runFinishedCycle(ctx)
		}
	}
}

func (b *Bot) runFinishedCycle(ctx context.Context) {
	slog.Debug("Запуск цикла завершенных матчей")

	idsToFetch, err := b.storage.GetSubscribedTeamIDs()
	if err != nil {
		slog.Error("Ошибка получения ID команд из БД", slog.Any("error", err))
		return
	}
	if len(idsToFetch) == 0 {
		return
	}

	matches, err := b.panda.FetchFinishedMatchesByTeamIDs(ctx, idsToFetch, time.Now().Add(-finishedPollWindow))
	if err != nil {
		slog.Error("Ошибка запроса завершенных матчей из API", slog.Any("error", err))
		return
	}

	for _, match := range matches {
		if _, _, _, _, _, _, _, _, err := b.storage.ProcessMatch(match); err != nil {
			slog.Error("Ошибка сохранения завершенного матча", slog.Int("match_id", match.ID), slog.Any("error", err))
		}
	}
	slog.Info("Цикл завершенных матчей", slog.Int("matches", len(matches)))
}
