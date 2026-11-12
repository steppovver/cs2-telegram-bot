package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchMatchesByIDsBatches(t *testing.T) {
	const found = `{"id":1789260,"status":"not_started","begin_at":"2026-11-24T09:00:00Z","opponents":[{"opponent":{"id":3213,"name":"Liquid"}},{"opponent":{"id":139562,"name":"GOSH"}}],"tournament":{"name":"Stage 1"}}`
	calls := 0
	c := NewClient("test-token", 1000)
	c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/csgo/matches" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("неверный пакетный запрос матчей")
		}
		ids := strings.Split(r.URL.Query().Get("filter[id]"), ",")
		if (calls == 1 && len(ids) != filterIDsPerRequest) || (calls == 2 && len(ids) != 1) {
			t.Fatalf("размер пачки #%d = %d", calls, len(ids))
		}
		body := "[]"
		if calls == 1 {
			body = "[" + found + `,{"id":1789261,"status":"canceled"}]`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	ids := make([]int, filterIDsPerRequest+1)
	for i := range ids {
		ids[i] = 1789260 + i
	}
	matches, err := c.FetchMatchesByIDs(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("запросов = %d, want 2", calls)
	}
	if len(matches) != 1 || matches[1789260].TeamBID != 139562 || matches[1789260].StageName != "Stage 1" {
		t.Fatalf("пакетный ответ = %+v", matches)
	}
	if used, _, _ := c.APIUsage(); used != 2 {
		t.Errorf("учёт запросов: used=%d", used)
	}
}

func TestMatchQueryFilters(t *testing.T) {
	for _, filter := range []string{filterOpponent, filterSerie, filterMatch} {
		q := matchesQuery{Filter: filter, IDs: []string{"1", "2"}}
		if filter != filterMatch {
			q.Statuses, q.Sort = matchStatuses, "-begin_at"
		}
		req, err := http.NewRequest(http.MethodGet, q.url(1), nil)
		if err != nil {
			t.Fatal(err)
		}
		params := req.URL.Query()
		if params.Get("filter["+filter+"]") != "1,2" || params.Get("sort") != q.Sort || params.Get("filter[status]") != strings.Join(q.Statuses, ",") {
			t.Fatalf("неверные фильтры: %s", req.URL)
		}
		if filter == filterMatch && params.Has("filter[status]") {
			t.Fatal("сверка по ID не должна ограничивать статусы")
		}
	}
}

func TestFetchMatchesByIDsRejectsPartialResult(t *testing.T) {
	for _, body := range []string{`[{"id":1,"status":"not_started"}]`, `[{"id":999,"status":"canceled"}]`, `{`} {
		c := NewClient("test-token", 1000)
		c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
		})
		if matches, err := c.FetchMatchesByIDs(context.Background(), []int{1, 2}); err == nil || matches != nil {
			t.Fatalf("неполный/неверный ответ %s: matches=%v error=%v", body, matches, err)
		}
	}
}

func TestFetchMatchesByIDsFollowsPagination(t *testing.T) {
	c := NewClient("test-token", 1000)
	calls := 0
	c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, headers := `[{"id":1,"status":"canceled"}]`, http.Header{}
		if calls == 1 {
			headers.Set("Link", `<https://api.pandascore.co/csgo/matches?filter[id]=1,2&page=2>; rel="next"`)
		} else {
			if calls != 2 || r.URL.Query().Get("page") != "2" {
				t.Fatalf("неверная пагинация: %s", r.URL)
			}
			body = `[{"id":2,"status":"not_started"}]` // ошибка второй страницы отменяет весь результат
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: headers}, nil
	})
	if matches, err := c.FetchMatchesByIDs(context.Background(), []int{1, 2}); err == nil || matches != nil || calls != 2 {
		t.Fatalf("matches=%v error=%v calls=%d", matches, err, calls)
	}
}

func TestFetchMatchesByIDsKeepsDataOnError(t *testing.T) {
	c := NewClient("test-token", 1000)
	c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})
	if matches, err := c.FetchMatchesByIDs(context.Background(), []int{1, 2}); err == nil || matches != nil {
		t.Fatalf("matches=%v error=%v, want nil и ошибку", matches, err)
	}
}
