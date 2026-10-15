package youtube

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Параметры фонового резолва имени канала YouTube.
const (
	resolveLookback = 6 * time.Hour
	resolveLeadTime = 48 * time.Hour
	resolveInterval = 5 * time.Minute
	// Пауза между запросами к oEmbed, чтобы не долбить сервис пачкой.
	resolvePause = time.Second
	// negativeTTL — повтор для видео, где oEmbed ответил «недоступно».
	negativeTTL = 30 * time.Minute
	// maxConsecutiveErrors — столько ошибок подряд → backoff цикла.
	maxConsecutiveErrors = 3
	blockedBackoff       = 30 * time.Minute
	// cleanupAge — удалять строки stream_channels старше этого срока.
	cleanupAge = 3 * 24 * time.Hour
)

// Store — узкий контракт хранилища для резолвера YouTube-каналов.
type Store interface {
	GetYouTubeVideosForResolve(from, to, retryBefore time.Time) ([]string, error)
	SaveStreamChannel(videoID, channel, channelURL string, checkedAt time.Time) error
	CleanOldStreamChannels(before time.Time) error
}

// Resolver периодически резолвит имена YouTube-каналов через oEmbed
// и сохраняет их в stream_channels. Пользовательские пути только читают кеш.
type Resolver struct {
	store  Store
	client *Client
}

// NewResolver создает воркер. client == nil — NewClient().
func NewResolver(store Store, client *Client) *Resolver {
	if client == nil {
		client = NewClient()
	}
	return &Resolver{store: store, client: client}
}

// Start — воркер: каждые resolveInterval сканирует матчи окна и резолвит
// video_id, которых еще нет в кеше (или пора повторять негативный кеш).
func (r *Resolver) Start(ctx context.Context) {
	if r == nil || r.store == nil {
		slog.Info("Резолвер YouTube-каналов выключен")
		return
	}
	slog.Info("Резолвер YouTube-каналов запущен",
		slog.Duration("lead", resolveLeadTime),
		slog.Duration("lookback", resolveLookback),
		slog.Duration("interval", resolveInterval))

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Резолвер YouTube-каналов завершил работу")
			return
		case <-timer.C:
		}

		wait := resolveInterval
		if r.runCycle(ctx, time.Now(), resolvePause) {
			wait = blockedBackoff
		}
		timer.Reset(wait)
	}
}

// runCycle резолвит video_id окна. Возвращает true, если нужен backoff
// (много ошибок подряд или 429).
func (r *Resolver) runCycle(ctx context.Context, now time.Time, pause time.Duration) (blocked bool) {
	if err := r.store.CleanOldStreamChannels(now.Add(-cleanupAge)); err != nil {
		slog.Warn("Не удалось почистить старые stream_channels", slog.Any("error", err))
	}

	todo, err := r.store.GetYouTubeVideosForResolve(
		now.Add(-resolveLookback), now.Add(resolveLeadTime), now.Add(-negativeTTL))
	if err != nil {
		slog.Error("Ошибка выборки YouTube video_id для резолва", slog.Any("error", err))
		return false
	}

	consecutiveErrors := 0
	for i, videoID := range todo {
		if i > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(pause):
			}
		}
		if ctx.Err() != nil {
			return false
		}

		info, err := r.client.Resolve(ctx, videoID)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			if errors.Is(err, ErrNotEmbeddable) {
				// Негативный кеш: channel='', повтор через negativeTTL.
				if saveErr := r.store.SaveStreamChannel(videoID, "", "", now); saveErr != nil {
					slog.Error("Не удалось сохранить негативный кеш YouTube",
						slog.String("video_id", videoID), slog.Any("error", saveErr))
				} else {
					slog.Info("YouTube oEmbed: видео недоступно",
						slog.String("video_id", videoID))
				}
				consecutiveErrors = 0
				continue
			}

			slog.Warn("Не удалось резолвить YouTube-канал",
				slog.String("video_id", videoID), slog.Any("error", err))
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveErrors {
				slog.Warn("oEmbed YouTube часто падает, пауза",
					slog.Int("errors", consecutiveErrors),
					slog.Duration("backoff", blockedBackoff))
				return true
			}
			// Сетевая/5xx: строку не пишем — video_id снова попадет в выборку
			// на следующем цикле (resolveInterval).
			continue
		}
		consecutiveErrors = 0

		if err := r.store.SaveStreamChannel(videoID, info.Name, info.URL, now); err != nil {
			slog.Error("Не удалось сохранить имя YouTube-канала",
				slog.String("video_id", videoID), slog.Any("error", err))
			continue
		}
		slog.Info("Резолв YouTube-канала",
			slog.String("video_id", videoID),
			slog.String("channel", info.Name))
	}
	return false
}
