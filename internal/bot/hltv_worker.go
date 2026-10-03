package bot

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"cs2bot/internal/domain"
	"cs2bot/internal/hltv"
)

const (
	// hltvScheduleInterval — период прохода по матчам без ссылки.
	hltvScheduleInterval = time.Hour
	// hltvDueLimit — матчей за один проход, больше в окне +-12ч не бывает.
	hltvDueLimit = 100
	// hltvWindowPast/Future — окно begin_at вокруг now для резолва.
	hltvWindowPast   = 12 * time.Hour
	hltvWindowFuture = 12 * time.Hour
	// hltvRequestDelay — пауза между запросами к HLTV, вежливость.
	hltvRequestDelay = 6 * time.Second
	// hltvQueueSize — буфер очереди, с запасом под окно.
	hltvQueueSize = 500
)

type hltvStorage interface {
	EnsureHltvLinkRow(matchID int) error
	GetHltvDueIDs(nowUnix, fromUnix, toUnix int64, limit int) ([]int, error)
	HltvAttempts(matchID int) (int, error)
	ClaimHltvAttempt(matchID int, nextTryUnix int64) (int, error)
	SetHltvURL(matchID int, url string) error
	HltvURLByIDs(ids []int) (map[int]string, error)
	GetMatchByID(matchID int) (domain.Match, error)
}

// StartHltvResolver запускает резолвер ссылок HLTV: планировщик раз в час
// (и раз при старте) кладет матчи без ссылки в очередь, скрапер разбирает
// ее последовательно с паузой. В БД при постановке ничего не пишется —
// клейм (attempts+1, next_try) ставится только при взятии в работу,
// поэтому частые рестарты ничего не залипают.
func (b *Bot) StartHltvResolver(ctx context.Context) {
	slog.Info("Воркер HLTV-ссылок запущен")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b.hltvSchedulerLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		b.hltvScrapeLoop(ctx)
	}()
	wg.Wait()
	slog.Info("Воркер HLTV-ссылок завершил работу")
}

func (b *Bot) hltvSchedulerLoop(ctx context.Context) {
	b.hltvScheduleOnce(ctx)
	t := time.NewTicker(hltvScheduleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.hltvScheduleOnce(ctx)
		}
	}
}

func (b *Bot) hltvScheduleOnce(ctx context.Context) {
	now := time.Now()
	ids, err := b.storage.GetHltvDueIDs(
		now.Unix(),
		now.Add(-hltvWindowPast).Unix(),
		now.Add(hltvWindowFuture).Unix(),
		hltvDueLimit,
	)
	if err != nil {
		slog.Error("HLTV: ошибка выборки матчей без ссылки", slog.Any("error", err))
		return
	}
	// Выборка уже в порядке приоритета (ближайшие к now сверху).
	enqueued := 0
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if b.enqueueHltv(id) {
			enqueued++
		}
	}
	if enqueued > 0 {
		slog.Info("HLTV: поставлены в очередь", slog.Int("queued", enqueued), slog.Int("due", len(ids)))
	}
}

func (b *Bot) hltvScrapeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-b.hltvQ:
			b.processHltv(ctx, id)
			select {
			case <-ctx.Done():
				return
			case <-time.After(hltvRequestDelay):
			}
		}
	}
}

func (b *Bot) processHltv(ctx context.Context, matchID int) {
	defer b.doneHltv(matchID)

	m, err := b.storage.GetMatchByID(matchID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("HLTV: не удалось прочитать матч", slog.Int("match_id", matchID), slog.Any("error", err))
		}
		return
	}
	if m.TeamA == "TBD" || m.TeamB == "TBD" || m.Time.IsZero() {
		return
	}

	// Клейм до скрапинга: упавшая попытка тоже откладывается.
	prevAttempts, err := b.storage.HltvAttempts(matchID)
	if err != nil {
		slog.Warn("HLTV: не удалось прочитать attempts", slog.Int("match_id", matchID), slog.Any("error", err))
		return
	}
	if _, err := b.storage.ClaimHltvAttempt(matchID, time.Now().Add(hltv.Backoff(prevAttempts+1)).Unix()); err != nil {
		slog.Warn("HLTV: не удалось заклеймить попытку", slog.Int("match_id", matchID), slog.Any("error", err))
		return
	}

	url, err := hltv.Resolve(ctx, m.TeamA, m.TeamB, m.Time, m.Status)
	if err != nil {
		if !errors.Is(err, hltv.ErrNotFound) {
			slog.Warn("HLTV: ошибка скрапинга", slog.Int("match_id", matchID), slog.Any("error", err))
		}
		return
	}
	if err := b.storage.SetHltvURL(matchID, url); err != nil {
		slog.Warn("HLTV: не удалось сохранить ссылку", slog.Int("match_id", matchID), slog.Any("error", err))
		return
	}
	slog.Info("HLTV: ссылка найдена", slog.Int("match_id", matchID), slog.String("url", url))
}

// ensureAndEnqueueHltv вызывается сразу при появлении матча в PandaScore.
// Неблокирующе: заводит строку-маркер и кладет ID в очередь.
func (b *Bot) ensureAndEnqueueHltv(m domain.Match) {
	if m.ID == 0 || m.TeamA == "TBD" || m.TeamB == "TBD" || m.Time.IsZero() {
		return
	}
	if err := b.storage.EnsureHltvLinkRow(m.ID); err != nil {
		slog.Warn("HLTV: не удалось завести маркер", slog.Int("match_id", m.ID), slog.Any("error", err))
		return
	}
	b.enqueueHltv(m.ID)
}

// enqueueHltv кладет ID в очередь без блокировки. false = дубль или
// очередь полна (подберет следующий часовой проход).
func (b *Bot) enqueueHltv(matchID int) bool {
	b.hltvMu.Lock()
	defer b.hltvMu.Unlock()
	if b.hltvInflight[matchID] {
		return false
	}
	select {
	case b.hltvQ <- matchID:
		b.hltvInflight[matchID] = true
		return true
	default:
		return false
	}
}

func (b *Bot) doneHltv(matchID int) {
	b.hltvMu.Lock()
	defer b.hltvMu.Unlock()
	delete(b.hltvInflight, matchID)
}
