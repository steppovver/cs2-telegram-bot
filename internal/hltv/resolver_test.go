package hltv

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

// fakeResolveStorage подменяет методы, нужные резолверу, и
// повторяет контракт реального хранилища (окно, фильтр по ссылке и TTL).
type fakeResolveStorage struct {
	matches   []domain.Match
	urls      map[int]string
	checkedAt map[int]time.Time
}

func newFakeResolveStorage(matches ...domain.Match) *fakeResolveStorage {
	return &fakeResolveStorage{
		matches:   matches,
		urls:      map[int]string{},
		checkedAt: map[int]time.Time{},
	}
}

func (f *fakeResolveStorage) GetMatchesForHLTVResolve(from, to, retryBefore time.Time) ([]domain.Match, error) {
	var out []domain.Match
	for _, m := range f.matches {
		if m.Time.Before(from) || m.Time.After(to) || f.urls[m.ID] != "" {
			continue
		}
		if at, ok := f.checkedAt[m.ID]; ok && !at.Before(retryBefore) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (f *fakeResolveStorage) SetMatchHLTV(matchID int, url string, checkedAt time.Time) error {
	f.urls[matchID] = url
	f.checkedAt[matchID] = checkedAt
	return nil
}

func TestRunCycle(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	good := "https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24"

	st := newFakeResolveStorage(
		domain.Match{ID: 1, TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League Season 24 2026", Time: now.Add(2 * time.Hour)},
		domain.Match{ID: 2, TeamA: "G2", TeamB: "NAVI", Time: now.Add(3 * time.Hour)},
		domain.Match{ID: 3, TeamA: "A", TeamB: "B", Time: now.Add(3 * time.Hour)},
		domain.Match{ID: 4, TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League Season 24 2026", Time: now.Add(4 * time.Hour)},
		domain.Match{ID: 5, TeamA: "C", TeamB: "D", Time: now.Add(13 * time.Hour)}, // за окном 12 часов
	)
	// 2: уже есть ссылка, 3: свежий негатив, 4: просроченный негатив
	st.urls[2] = "https://www.hltv.org/matches/1/a-vs-b"
	st.checkedAt[3] = now.Add(-10 * time.Minute)
	st.checkedAt[4] = now.Add(-2 * time.Hour)

	sp := &fakeSearch{results: []string{good}}
	r := NewResolver(st, sp)

	if blocked := r.runCycle(context.Background(), now, 0); blocked {
		t.Fatal("blocked = true")
	}
	if len(sp.queries) != 2 {
		t.Fatalf("запросов к поиску = %d (%v), want 2 (матчи 1 и 4)", len(sp.queries), sp.queries)
	}
	if st.urls[1] != good || st.urls[4] != good {
		t.Errorf("ссылки не сохранены: %v", st.urls)
	}
	if _, ok := st.checkedAt[5]; ok {
		t.Error("матч за окном 12 часов не должен искаться")
	}

	// Второй цикл ничего не ищет: ссылки найдены или проверены недавно.
	sp.queries = nil
	r.runCycle(context.Background(), now, 0)
	if len(sp.queries) != 0 {
		t.Errorf("повторный цикл сделал запросы: %v", sp.queries)
	}
}

func TestRunCycleNotFoundRetry(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	st := newFakeResolveStorage(domain.Match{ID: 1, TeamA: "G2", TeamB: "NAVI", Time: now.Add(time.Hour)})
	sp := &fakeSearch{} // пустая выдача
	r := NewResolver(st, sp)

	r.runCycle(context.Background(), now, 0)
	if st.urls[1] != "" || !st.checkedAt[1].Equal(now) {
		t.Fatalf("ожидалась отметка 'не найдено': url=%q checkedAt=%v", st.urls[1], st.checkedAt[1])
	}

	// В пределах TTL повторного поиска нет, после TTL — есть.
	r.runCycle(context.Background(), now.Add(10*time.Minute), 0)
	if len(sp.queries) != 1 {
		t.Errorf("запросов = %d, want 1", len(sp.queries))
	}
	r.runCycle(context.Background(), now.Add(negativeTTL+time.Minute), 0)
	if len(sp.queries) != 2 {
		t.Errorf("запросов после TTL = %d, want 2", len(sp.queries))
	}
}

func TestRunCycleBlocked(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	st := newFakeResolveStorage(
		domain.Match{ID: 1, TeamA: "G2", TeamB: "NAVI", Time: now.Add(time.Hour)},
		domain.Match{ID: 2, TeamA: "A", TeamB: "B", Time: now.Add(2 * time.Hour)},
	)
	sp := &fakeSearch{err: fmt.Errorf("DDG: %w", ErrSearchBlocked)}
	r := NewResolver(st, sp)

	if blocked := r.runCycle(context.Background(), now, 0); !blocked {
		t.Fatal("blocked = false, want true")
	}
	if len(sp.queries) != 1 {
		t.Errorf("после блокировки цикл должен остановиться, запросов = %d", len(sp.queries))
	}
	if len(st.checkedAt) != 0 {
		t.Errorf("блокировка не должна помечать матчи проверенными: %v", st.checkedAt)
	}
}

func TestRunCycleTransientError(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	st := newFakeResolveStorage(domain.Match{ID: 1, TeamA: "G2", TeamB: "NAVI", Time: now.Add(time.Hour)})
	sp := &fakeSearch{err: errors.New("timeout")}
	r := NewResolver(st, sp)

	if blocked := r.runCycle(context.Background(), now, 0); blocked {
		t.Fatal("одна обычная ошибка не должна давать backoff")
	}
	if st.urls[1] != "" {
		t.Errorf("при ошибке ссылка не пишется: %q", st.urls[1])
	}

	// Следующие циклы до errorRetry матч не трогают, после — повторяют.
	r.runCycle(context.Background(), now.Add(5*time.Minute), 0)
	r.runCycle(context.Background(), now.Add(errorRetry-time.Minute), 0)
	if len(sp.queries) != 1 {
		t.Errorf("запросов до errorRetry = %d, want 1", len(sp.queries))
	}
	r.runCycle(context.Background(), now.Add(errorRetry+time.Minute), 0)
	if len(sp.queries) != 2 {
		t.Errorf("запросов после errorRetry = %d, want 2", len(sp.queries))
	}
}

func TestRunCycleConsecutiveErrorsBackoff(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var ms []domain.Match
	for i := 1; i <= 5; i++ {
		ms = append(ms, domain.Match{ID: i, TeamA: "A", TeamB: "B", Time: now.Add(time.Duration(i) * time.Hour)})
	}
	st := newFakeResolveStorage(ms...)
	sp := &fakeSearch{err: errors.New("timeout")}
	r := NewResolver(st, sp)

	if blocked := r.runCycle(context.Background(), now, 0); !blocked {
		t.Fatal("серия ошибок подряд должна давать backoff")
	}
	if len(sp.queries) != maxConsecutiveErrors {
		t.Errorf("запросов = %d, want %d", len(sp.queries), maxConsecutiveErrors)
	}
}
