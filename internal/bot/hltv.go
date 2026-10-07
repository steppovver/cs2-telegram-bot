package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"

	"cs2bot/internal/domain"
	"cs2bot/internal/hltv"
)

// SetHLTVResolver задает воркер точных ссылок HLTV. nil = выключен.
func (b *Bot) SetHLTVResolver(r *hltv.Resolver) {
	b.hltvResolver = r
}

// StartHLTVResolver запускает воркер поиска точных страниц матчей на HLTV.
func (b *Bot) StartHLTVResolver(ctx context.Context) {
	if b.hltvResolver == nil {
		slog.Info("Резолвер ссылок HLTV выключен, используется поиск Google")
		return
	}
	b.hltvResolver.Start(ctx)
}

// attachHLTV подставляет найденные ссылки в матчи (на месте). Нужен только
// для матчей, пришедших из API (события поллера): матчи из БД получают
// HLTVURL сразу в селектах storage. Нет ссылки — HLTVURL остается пустым
// и рендер использует поиск Google.
func (b *Bot) attachHLTV(matches []domain.Match) {
	if len(matches) == 0 {
		return
	}
	ids := make([]int, len(matches))
	for i, m := range matches {
		ids[i] = m.ID
	}
	urls, err := b.storage.GetHLTVURLs(ids)
	if err != nil {
		slog.Warn("Не удалось прочитать ссылки HLTV", slog.Any("error", err))
		return
	}
	for i := range matches {
		matches[i].HLTVURL = urls[matches[i].ID]
	}
}

// hltvMatchLine возвращает строку ссылки на профиль матча.
// TBD-команды — пустая строка, рендер ее пропускает.
func hltvMatchLine(m domain.Match) string {
	if m.TeamA == "TBD" || m.TeamB == "TBD" {
		return ""
	}
	return fmt.Sprintf(`📊 HLTV: <a href="%s">Профиль матча</a>`, html.EscapeString(hltv.MatchURL(m)))
}
