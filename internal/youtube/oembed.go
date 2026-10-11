package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotEmbeddable — видео недоступно через oEmbed (приватное / встраивание
// запрещено). Воркер пишет негативный кеш и повторяет позже.
var ErrNotEmbeddable = errors.New("youtube oembed: video not embeddable")

const (
	oembedEndpoint  = "https://www.youtube.com/oembed"
	oembedTimeout   = 10 * time.Second
	oembedMaxBody   = 1 << 20
	oembedUserAgent = "cs2bot/1.0"
)

// ChannelInfo — результат резолва имени канала через oEmbed.
type ChannelInfo struct {
	Name string // author_name
	URL  string // author_url
}

type oembedResponse struct {
	AuthorName string `json:"author_name"`
	AuthorURL  string `json:"author_url"`
}

// Client ходит в YouTube oEmbed. endpoint переопределяется в тестах.
type Client struct {
	http     *http.Client
	endpoint string
}

// NewClient создает клиент с таймаутом oembedTimeout.
func NewClient() *Client {
	return &Client{
		http:     &http.Client{Timeout: oembedTimeout},
		endpoint: oembedEndpoint,
	}
}

// Resolve запрашивает oEmbed по videoID и возвращает имя/URL канала.
// 401/403/404 → ErrNotEmbeddable; прочие статусы и сеть — обычная ошибка.
func (c *Client) Resolve(ctx context.Context, videoID string) (ChannelInfo, error) {
	if c == nil {
		return ChannelInfo{}, fmt.Errorf("youtube oembed: nil client")
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return ChannelInfo{}, fmt.Errorf("youtube oembed: пустой video_id")
	}

	watchURL := "https://www.youtube.com/watch?v=" + videoID
	reqURL := c.endpoint + "?format=json&url=" + url.QueryEscape(watchURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return ChannelInfo{}, fmt.Errorf("youtube oembed: создание запроса: %w", err)
	}
	req.Header.Set("User-Agent", oembedUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return ChannelInfo{}, fmt.Errorf("youtube oembed: запрос: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, oembedMaxBody))
	if err != nil {
		return ChannelInfo{}, fmt.Errorf("youtube oembed: чтение ответа: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// ok
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return ChannelInfo{}, fmt.Errorf("youtube oembed: статус %d: %w", resp.StatusCode, ErrNotEmbeddable)
	default:
		return ChannelInfo{}, fmt.Errorf("youtube oembed: статус %d", resp.StatusCode)
	}

	var parsed oembedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ChannelInfo{}, fmt.Errorf("youtube oembed: разбор JSON: %w", err)
	}
	name := strings.TrimSpace(parsed.AuthorName)
	if name == "" {
		return ChannelInfo{}, fmt.Errorf("youtube oembed: пустой author_name: %w", ErrNotEmbeddable)
	}
	return ChannelInfo{
		Name: name,
		URL:  strings.TrimSpace(parsed.AuthorURL),
	}, nil
}
