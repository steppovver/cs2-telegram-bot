package hltv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"cs2bot/internal/config"
)

// SearchProvider ищет страницы в вебе и возвращает URL результатов по порядку.
type SearchProvider interface {
	Search(ctx context.Context, query string) ([]string, error)
}

// ErrSearchBlocked — поисковик отдал капчу/лимит: повторять запросы сразу
// бессмысленно, воркер уходит в backoff.
var ErrSearchBlocked = errors.New("поисковик заблокировал запрос")

const (
	searchHTTPTimeout = 15 * time.Second
	// Страница выдачи небольшая; ограничиваем чтение от аномально больших ответов.
	searchMaxBody = 2 << 20
	searchUA      = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
)

// ddgProvider — бесплатный поиск через HTML-версию DuckDuckGo (без ключа).
type ddgProvider struct {
	client   *http.Client
	endpoint string
}

func newDDGProvider() *ddgProvider {
	return &ddgProvider{
		client:   &http.Client{Timeout: searchHTTPTimeout},
		endpoint: "https://html.duckduckgo.com/html/",
	}
}

var (
	ddgResultTagRe = regexp.MustCompile(`(?is)<a\s[^>]*class="[^"]*\bresult__a\b[^"]*"[^>]*>`)
	ddgHrefRe      = regexp.MustCompile(`(?is)\shref="([^"]+)"`)
)

func (p *ddgProvider) Search(ctx context.Context, query string) ([]string, error) {
	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("создание запроса DDG: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", searchUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("запрос DDG: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, searchMaxBody))
	if err != nil {
		return nil, fmt.Errorf("чтение ответа DDG: %w", err)
	}

	// 202 у DDG — страница-проверка на бота.
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("DDG статус %d: %w", resp.StatusCode, ErrSearchBlocked)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DDG статус %d", resp.StatusCode)
	}

	urls := parseDDGResults(string(body))
	if len(urls) == 0 && strings.Contains(string(body), "anomaly") {
		return nil, fmt.Errorf("DDG показал проверку: %w", ErrSearchBlocked)
	}
	return urls, nil
}

// parseDDGResults достает целевые URL из HTML-выдачи DDG. Ссылки результатов
// обернуты редиректом //duckduckgo.com/l/?uddg=<url>, его разворачиваем.
func parseDDGResults(page string) []string {
	var out []string
	for _, tag := range ddgResultTagRe.FindAllString(page, -1) {
		m := ddgHrefRe.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		if u := unwrapDDGLink(html.UnescapeString(m[1])); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func unwrapDDGLink(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Host, "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		return u.Query().Get("uddg")
	}
	return href
}

// searxngProvider — запасной бесплатный вариант: свой инстанс SearXNG
// (в settings.yml должен быть включен формат json).
type searxngProvider struct {
	client  *http.Client
	baseURL string
}

func newSearXNGProvider(baseURL string) *searxngProvider {
	return &searxngProvider{
		client:  &http.Client{Timeout: searchHTTPTimeout},
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (p *searxngProvider) Search(ctx context.Context, query string) ([]string, error) {
	q := url.Values{"q": {query}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("создание запроса SearXNG: %w", err)
	}
	req.Header.Set("User-Agent", searchUA)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("запрос SearXNG: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("SearXNG статус %d: %w", resp.StatusCode, ErrSearchBlocked)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SearXNG статус %d", resp.StatusCode)
	}

	var payload struct {
		Results []struct {
			URL string `json:"url"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, searchMaxBody)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("разбор ответа SearXNG: %w", err)
	}
	out := make([]string, 0, len(payload.Results))
	for _, r := range payload.Results {
		if r.URL != "" {
			out = append(out, r.URL)
		}
	}
	return out, nil
}

// NewSearchProvider создает провайдера по значению конфига.
// nil, nil = резолвер выключен (none).
func NewSearchProvider(provider, baseURL string) (SearchProvider, error) {
	switch provider {
	case config.SearchProviderNone:
		return nil, nil
	case config.SearchProviderSearXNG:
		if strings.TrimSpace(baseURL) == "" {
			return nil, errors.New("search_base_url обязателен для search_provider=searxng")
		}
		return newSearXNGProvider(baseURL), nil
	default:
		return newDDGProvider(), nil
	}
}
