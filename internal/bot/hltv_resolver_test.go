package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestValidateMatchURL(t *testing.T) {
	falcons := domain.Match{TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League Season 24 2026"}

	tests := []struct {
		name  string
		m     domain.Match
		url   string
		want  string
		valid bool
	}{
		{
			name:  "точное совпадение",
			m:     falcons,
			url:   "https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
			want:  "https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
			valid: true,
		},
		{
			name:  "обратный порядок команд и хост без www",
			m:     falcons,
			url:   "https://hltv.org/matches/2380123/tyloo-vs-falcons-esl-pro-league-season-24/",
			want:  "https://www.hltv.org/matches/2380123/tyloo-vs-falcons-esl-pro-league-season-24",
			valid: true,
		},
		{
			name: "матч тех же команд на другом турнире",
			m:    falcons,
			url:  "https://www.hltv.org/matches/2371000/falcons-vs-tyloo-iem-cologne-2025",
		},
		{
			name: "другая команда",
			m:    falcons,
			url:  "https://www.hltv.org/matches/2380123/falcons-vs-navi-esl-pro-league-season-24",
		},
		{
			name: "чужой хост",
			m:    falcons,
			url:  "https://example.com/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
		},
		{
			name: "не страница матча",
			m:    falcons,
			url:  "https://www.hltv.org/team/8297/falcons",
		},
		{
			name:  "без турнира требований к событию нет",
			m:     domain.Match{TeamA: "G2", TeamB: "Natus Vincere"},
			url:   "https://www.hltv.org/matches/2380001/g2-vs-natus-vincere-blast-premier-fall-2026",
			want:  "https://www.hltv.org/matches/2380001/g2-vs-natus-vincere-blast-premier-fall-2026",
			valid: true,
		},
		{
			name:  "название без разделителей и точек",
			m:     domain.Match{TeamA: "Virtus.pro", TeamB: "FaZe Clan"},
			url:   "https://www.hltv.org/matches/2380002/virtuspro-vs-faze-iem-katowice",
			want:  "https://www.hltv.org/matches/2380002/virtuspro-vs-faze-iem-katowice",
			valid: true,
		},
		{
			name: "MOUZ NXT не подходит под MOUZ",
			m:    domain.Match{TeamA: "MOUZ", TeamB: "G2"},
			url:  "https://www.hltv.org/matches/2380003/mouz-nxt-vs-g2-esl-challenger",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := validateMatchURL(tt.url, tt.m)
			if ok != tt.valid || got != tt.want {
				t.Errorf("validateMatchURL(%q) = (%q, %v), want (%q, %v)", tt.url, got, ok, tt.want, tt.valid)
			}
		})
	}
}

// fakeSearch отдает заготовленные результаты и считает вызовы.
type fakeSearch struct {
	results []string
	err     error
	queries []string
}

func (f *fakeSearch) Search(_ context.Context, query string) ([]string, error) {
	f.queries = append(f.queries, query)
	return f.results, f.err
}

func TestResolveHLTVMatchSkipsWrongCandidates(t *testing.T) {
	m := domain.Match{ID: 1, TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League Season 24 2026"}
	sp := &fakeSearch{results: []string{
		"https://www.hltv.org/matches/2371000/falcons-vs-tyloo-iem-cologne-2025",
		"https://www.hltv.org/team/8297/falcons",
		"https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
	}}
	got, err := resolveHLTVMatch(context.Background(), sp, m)
	if err != nil {
		t.Fatalf("resolveHLTVMatch: %v", err)
	}
	if want := "https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(sp.queries) != 1 || !strings.HasPrefix(sp.queries[0], "site:hltv.org/matches Team Falcons vs TYLOO") {
		t.Errorf("queries = %v", sp.queries)
	}
}

func TestResolveHLTVMatchNotFound(t *testing.T) {
	m := domain.Match{ID: 1, TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League Season 24 2026"}
	sp := &fakeSearch{results: []string{"https://www.hltv.org/matches/2371000/falcons-vs-tyloo-iem-cologne-2025"}}
	got, err := resolveHLTVMatch(context.Background(), sp, m)
	if err != nil || got != "" {
		t.Errorf("got (%q, %v), want пусто без ошибки", got, err)
	}
}

// fakeResolveStorage подменяет только методы, нужные резолверу, и
// повторяет контракт реального хранилища (окно, фильтр по ссылке и TTL).
type fakeResolveStorage struct {
	Storage
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

func (f *fakeResolveStorage) GetHLTVURLs(ids []int) (map[int]string, error) {
	out := make(map[int]string)
	for _, id := range ids {
		if u := f.urls[id]; u != "" {
			out[id] = u
		}
	}
	return out, nil
}

func TestRunHLTVResolveCycle(t *testing.T) {
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
	b := &Bot{storage: st, hltvSearch: sp}

	if blocked := b.runHLTVResolveCycle(context.Background(), now, 0); blocked {
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
	b.runHLTVResolveCycle(context.Background(), now, 0)
	if len(sp.queries) != 0 {
		t.Errorf("повторный цикл сделал запросы: %v", sp.queries)
	}
}

func TestRunHLTVResolveCycleNotFoundRetry(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	st := newFakeResolveStorage(domain.Match{ID: 1, TeamA: "G2", TeamB: "NAVI", Time: now.Add(time.Hour)})
	sp := &fakeSearch{} // пустая выдача
	b := &Bot{storage: st, hltvSearch: sp}

	b.runHLTVResolveCycle(context.Background(), now, 0)
	if st.urls[1] != "" || !st.checkedAt[1].Equal(now) {
		t.Fatalf("ожидалась отметка 'не найдено': url=%q checkedAt=%v", st.urls[1], st.checkedAt[1])
	}

	// В пределах TTL повторного поиска нет, после TTL — есть.
	b.runHLTVResolveCycle(context.Background(), now.Add(10*time.Minute), 0)
	if len(sp.queries) != 1 {
		t.Errorf("запросов = %d, want 1", len(sp.queries))
	}
	b.runHLTVResolveCycle(context.Background(), now.Add(hltvNegativeTTL+time.Minute), 0)
	if len(sp.queries) != 2 {
		t.Errorf("запросов после TTL = %d, want 2", len(sp.queries))
	}
}

func TestRunHLTVResolveCycleBlocked(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	st := newFakeResolveStorage(
		domain.Match{ID: 1, TeamA: "G2", TeamB: "NAVI", Time: now.Add(time.Hour)},
		domain.Match{ID: 2, TeamA: "A", TeamB: "B", Time: now.Add(2 * time.Hour)},
	)
	sp := &fakeSearch{err: fmt.Errorf("DDG: %w", ErrSearchBlocked)}
	b := &Bot{storage: st, hltvSearch: sp}

	if blocked := b.runHLTVResolveCycle(context.Background(), now, 0); !blocked {
		t.Fatal("blocked = false, want true")
	}
	if len(sp.queries) != 1 {
		t.Errorf("после блокировки цикл должен остановиться, запросов = %d", len(sp.queries))
	}
	if len(st.checkedAt) != 0 {
		t.Errorf("блокировка не должна помечать матчи проверенными: %v", st.checkedAt)
	}
}

func TestRunHLTVResolveCycleTransientError(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	st := newFakeResolveStorage(domain.Match{ID: 1, TeamA: "G2", TeamB: "NAVI", Time: now.Add(time.Hour)})
	sp := &fakeSearch{err: errors.New("timeout")}
	b := &Bot{storage: st, hltvSearch: sp}

	if blocked := b.runHLTVResolveCycle(context.Background(), now, 0); blocked {
		t.Fatal("одна обычная ошибка не должна давать backoff")
	}
	if st.urls[1] != "" {
		t.Errorf("при ошибке ссылка не пишется: %q", st.urls[1])
	}

	// Следующие циклы до hltvErrorRetry матч не трогают, после — повторяют.
	b.runHLTVResolveCycle(context.Background(), now.Add(5*time.Minute), 0)
	b.runHLTVResolveCycle(context.Background(), now.Add(hltvErrorRetry-time.Minute), 0)
	if len(sp.queries) != 1 {
		t.Errorf("запросов до hltvErrorRetry = %d, want 1", len(sp.queries))
	}
	b.runHLTVResolveCycle(context.Background(), now.Add(hltvErrorRetry+time.Minute), 0)
	if len(sp.queries) != 2 {
		t.Errorf("запросов после hltvErrorRetry = %d, want 2", len(sp.queries))
	}
}

func TestRunHLTVResolveCycleConsecutiveErrorsBackoff(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var ms []domain.Match
	for i := 1; i <= 5; i++ {
		ms = append(ms, domain.Match{ID: i, TeamA: "A", TeamB: "B", Time: now.Add(time.Duration(i) * time.Hour)})
	}
	st := newFakeResolveStorage(ms...)
	sp := &fakeSearch{err: errors.New("timeout")}
	b := &Bot{storage: st, hltvSearch: sp}

	if blocked := b.runHLTVResolveCycle(context.Background(), now, 0); !blocked {
		t.Fatal("серия ошибок подряд должна давать backoff")
	}
	if len(sp.queries) != hltvMaxConsecutiveErrors {
		t.Errorf("запросов = %d, want %d", len(sp.queries), hltvMaxConsecutiveErrors)
	}
}

func TestAttachHLTV(t *testing.T) {
	st := newFakeResolveStorage()
	st.urls[1] = "https://www.hltv.org/matches/1/a-vs-b"
	st.checkedAt[2] = time.Now() // искали, не нашли
	b := &Bot{storage: st}
	ms := []domain.Match{{ID: 1}, {ID: 2}, {ID: 3}}
	b.attachHLTV(ms)

	if ms[0].HLTVURL != "https://www.hltv.org/matches/1/a-vs-b" {
		t.Errorf("match 1 HLTVURL = %q", ms[0].HLTVURL)
	}
	if ms[1].HLTVURL != "" || ms[2].HLTVURL != "" {
		t.Errorf("не найденные матчи не должны получать ссылку: %q %q", ms[1].HLTVURL, ms[2].HLTVURL)
	}
}

func TestHltvMatchURLPrefersCached(t *testing.T) {
	m := domain.Match{TeamA: "G2", TeamB: "NAVI", HLTVURL: "https://www.hltv.org/matches/1/g2-vs-navi"}
	if got := hltvMatchURL(m); got != m.HLTVURL {
		t.Errorf("hltvMatchURL = %q, want закешированный", got)
	}
	m.HLTVURL = ""
	if got := hltvMatchURL(m); !strings.HasPrefix(got, "https://www.google.com/search?") {
		t.Errorf("без кеша ожидался Google fallback, got %q", got)
	}
}
