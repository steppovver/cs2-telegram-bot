package hltv

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"cs2bot/internal/domain"
)

// Параметры резолвера точных ссылок HLTV.
const (
	// resolveLeadTime — за сколько до старта начинаем искать страницу матча.
	resolveLeadTime = 12 * time.Hour
	// resolveLookback — как долго после старта еще добираем ссылку
	// (идущие и только что сыгранные матчи показываются в «Счете»).
	resolveLookback = 6 * time.Hour
	resolveInterval = 5 * time.Minute
	// Пауза между запросами к поисковику: частые запросы приводят к капче.
	resolvePause = 7 * time.Second
	// negativeTTL — через сколько повторять поиск, если страница не найдена.
	negativeTTL = time.Hour
	// errorRetry — через сколько повторять поиск после сетевой ошибки
	// (короче negativeTTL, но не в каждом цикле).
	errorRetry = 15 * time.Minute
	// maxConsecutiveErrors — столько ошибок подряд в одном цикле
	// считаем признаком проблем у поисковика и уходим в backoff.
	maxConsecutiveErrors = 3
	blockedBackoff       = 30 * time.Minute
)

// Store — узкий контракт хранилища для резолвера ссылок HLTV.
type Store interface {
	GetMatchesForHLTVResolve(from, to, retryBefore time.Time) ([]domain.Match, error)
	SetMatchHLTV(matchID int, url string, checkedAt time.Time) error
}

// Resolver периодически ищет точные страницы матчей на HLTV и сохраняет URL.
type Resolver struct {
	store  Store
	search SearchProvider
}

// NewResolver создает резолвер. search == nil — резолвер выключен.
func NewResolver(store Store, search SearchProvider) *Resolver {
	return &Resolver{store: store, search: search}
}

// Start — воркер, который за resolveLeadTime до старта ищет точную
// страницу матча на HLTV и сохраняет ее в matches.hltv_url.
// При search == nil сразу завершается.
func (r *Resolver) Start(ctx context.Context) {
	if r == nil || r.search == nil {
		slog.Info("Резолвер ссылок HLTV выключен, используется поиск Google")
		return
	}
	slog.Info("Резолвер ссылок HLTV запущен",
		slog.Duration("lead", resolveLeadTime), slog.Duration("interval", resolveInterval))

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Резолвер ссылок HLTV завершил работу")
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

// runCycle ищет страницы для матчей окна, у которых ссылки еще нет
// и прошлая попытка была давно (negativeTTL). Фильтр делает БД.
// Возвращает true, если поисковик заблокировал запрос или ошибается
// подряд (нужен backoff).
func (r *Resolver) runCycle(ctx context.Context, now time.Time, pause time.Duration) (blocked bool) {
	consecutiveErrors := 0
	todo, err := r.store.GetMatchesForHLTVResolve(
		now.Add(-resolveLookback), now.Add(resolveLeadTime), now.Add(-negativeTTL))
	if err != nil {
		slog.Error("Ошибка выборки матчей для поиска HLTV", slog.Any("error", err))
		return false
	}

	for i, m := range todo {
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

		found, err := ResolveMatch(ctx, r.search, m)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			if errors.Is(err, ErrSearchBlocked) {
				slog.Warn("Поисковик заблокировал запросы HLTV, пауза",
					slog.Duration("backoff", blockedBackoff), slog.Any("error", err))
				return true
			}
			slog.Warn("Не удалось найти страницу матча на HLTV",
				slog.Int("match_id", m.ID), slog.Any("error", err))

			// Отметка попытки сдвинута так, чтобы матч вернулся в выборку
			// через errorRetry, а не в каждом цикле.
			retryMark := now.Add(-(negativeTTL - errorRetry))
			if err := r.store.SetMatchHLTV(m.ID, "", retryMark); err != nil {
				slog.Error("Не удалось сохранить отметку попытки HLTV", slog.Int("match_id", m.ID), slog.Any("error", err))
			}
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveErrors {
				slog.Warn("Поиск HLTV часто падает, пауза",
					slog.Int("errors", consecutiveErrors), slog.Duration("backoff", blockedBackoff))
				return true
			}
			continue
		}
		consecutiveErrors = 0

		// Пустая found с checkedAt = "не нашли", повтор через negativeTTL.
		if err := r.store.SetMatchHLTV(m.ID, found, now); err != nil {
			slog.Error("Не удалось сохранить ссылку HLTV", slog.Int("match_id", m.ID), slog.Any("error", err))
			continue
		}
		slog.Info("Поиск страницы матча HLTV",
			slog.Int("match_id", m.ID),
			slog.String("teams", m.TeamA+" vs "+m.TeamB),
			slog.Bool("found", found != ""))
	}
	return false
}
