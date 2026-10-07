package hltv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

const ddgSamplePage = `<html><body>
<div class="result results_links results_links_deep web-result">
<h2 class="result__title">
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.hltv.org%2Fmatches%2F2380123%2Ffalcons-vs-tyloo-esl-pro-league-season-24&amp;rut=abc">Falcons vs TYLOO</a>
</h2></div>
<div class="result"><h2><a class="result__a" rel="nofollow" href="https://www.hltv.org/matches/2371000/falcons-vs-tyloo-iem-cologne-2025">Old</a></h2></div>
<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fignored.example">snippet</a>
</body></html>`

func TestParseDDGResults(t *testing.T) {
	got := parseDDGResults(ddgSamplePage)
	want := []string{
		"https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
		"https://www.hltv.org/matches/2371000/falcons-vs-tyloo-iem-cologne-2025",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDDGResults = %v, want %v", got, want)
	}
}

func newTestDDG(srv *httptest.Server) *ddgProvider {
	p := newDDGProvider()
	p.endpoint = srv.URL
	return p
}

func TestDDGSearch(t *testing.T) {
	var gotQuery, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = r.ParseForm()
		gotQuery = r.PostForm.Get("q")
		_, _ = w.Write([]byte(ddgSamplePage))
	}))
	defer srv.Close()

	urls, err := newTestDDG(srv).Search(context.Background(), "site:hltv.org/matches A vs B")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(urls) != 2 {
		t.Errorf("urls = %v", urls)
	}
	if gotMethod != http.MethodPost || gotQuery != "site:hltv.org/matches A vs B" {
		t.Errorf("method=%s q=%q", gotMethod, gotQuery)
	}
}

func TestDDGSearchBlocked(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"202": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) },
		"403": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"anomaly": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<div class="anomaly-modal__title">Unfortunately, bots use DuckDuckGo too.</div>`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			_, err := newTestDDG(srv).Search(context.Background(), "q")
			if !errors.Is(err, ErrSearchBlocked) {
				t.Errorf("err = %v, want ErrSearchBlocked", err)
			}
		})
	}
}

func TestDDGSearchEmptyAndServerError(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><div class="no-results">No results.</div></html>`))
	}))
	defer empty.Close()
	urls, err := newTestDDG(empty).Search(context.Background(), "q")
	if err != nil || len(urls) != 0 {
		t.Errorf("пустая выдача: urls=%v err=%v", urls, err)
	}

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()
	_, err = newTestDDG(fail).Search(context.Background(), "q")
	if err == nil || errors.Is(err, ErrSearchBlocked) {
		t.Errorf("500 должен быть обычной ошибкой, got %v", err)
	}
}

func TestSearXNGSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "x" {
			t.Errorf("неожиданный запрос: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"results":[{"url":"https://www.hltv.org/matches/1/a-vs-b"},{"url":""}]}`))
	}))
	defer srv.Close()

	urls, err := newSearXNGProvider(srv.URL+"/").Search(context.Background(), "x")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !reflect.DeepEqual(urls, []string{"https://www.hltv.org/matches/1/a-vs-b"}) {
		t.Errorf("urls = %v", urls)
	}
}

func TestNewSearchProvider(t *testing.T) {
	if p, err := NewSearchProvider("none", ""); p != nil || err != nil {
		t.Errorf("none: (%v, %v)", p, err)
	}
	if _, err := NewSearchProvider("searxng", ""); err == nil {
		t.Error("searxng без base_url должен вернуть ошибку")
	}
	if p, err := NewSearchProvider("ddg", ""); p == nil || err != nil {
		t.Errorf("ddg: (%v, %v)", p, err)
	}
}
