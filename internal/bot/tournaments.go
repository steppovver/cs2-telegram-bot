package bot

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"cs2bot/internal/domain"
)

// tournamentGroup — матчи одного турнира (ключ — serie.id).
// ID 0 и пустое имя — корзина "Прочие" (serie не пришла из API).
type tournamentGroup struct {
	ID      int
	Name    string
	BeginAt time.Time
	Matches []domain.Match
}

// groupMatchesByTournament режет матчи на группы по турниру.
// Группы — по старту турнира (неизвестный старт — по первому матчу),
// матчи внутри — как пришли (селекты уже отдают по времени).
func groupMatchesByTournament(matches []domain.Match) []tournamentGroup {
	var order []tournamentGroup
	index := make(map[int]int)
	unknownIndex := make(map[string]int)
	for _, m := range matches {
		if m.TournamentID != 0 {
			if i, ok := index[m.TournamentID]; ok {
				order[i].Matches = append(order[i].Matches, m)
				continue
			}
			index[m.TournamentID] = len(order)
			order = append(order, tournamentGroup{
				ID:      m.TournamentID,
				Name:    m.Tournament,
				BeginAt: m.TournamentBeginAt,
				Matches: []domain.Match{m},
			})
			continue
		}
		name := m.Tournament
		if i, ok := unknownIndex[name]; ok {
			order[i].Matches = append(order[i].Matches, m)
			continue
		}
		unknownIndex[name] = len(order)
		order = append(order, tournamentGroup{
			Matches: []domain.Match{m},
		})
	}
	for i := range order {
		if order[i].Name == "" {
			order[i].Name = "Прочие"
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		bi, bj := groupSortKey(order[i]), groupSortKey(order[j])
		if !bi.Equal(bj) {
			return bi.Before(bj)
		}
		return order[i].Name < order[j].Name
	})
	return order
}

// groupSortKey — старт турнира, при неизвестном — первый матч группы.
func groupSortKey(g tournamentGroup) time.Time {
	if !g.BeginAt.IsZero() {
		return g.BeginAt
	}
	if len(g.Matches) > 0 && !g.Matches[0].Time.IsZero() {
		return g.Matches[0].Time
	}
	return time.Time{}
}

// hltvEventURL строит ссылку на страницу турнира через Google "Мне повезёт".
func hltvEventURL(tournament string) string {
	return googleLuckyURL("site:hltv.org/events " + strings.TrimSpace(tournament))
}

// tournamentHeader — заголовок группы: кликабельное имя турнира.
// Корзина "Прочие" — plain-текст без ссылки.
func tournamentHeader(g tournamentGroup) string {
	if g.ID == 0 || strings.TrimSpace(g.Name) == "" || g.Name == "Прочие" {
		return "🗂 Прочие"
	}
	return fmt.Sprintf(`🏆 <a href="%s">%s</a>`,
		html.EscapeString(hltvEventURL(g.Name)), html.EscapeString(g.Name))
}

// tournamentLine — строка турнира для карточек со стримами.
// Пустой турнир — пустая строка, рендер ее пропускает.
func tournamentLine(m domain.Match) string {
	if strings.TrimSpace(m.Tournament) == "" {
		return ""
	}
	return fmt.Sprintf(`🏟 Турнир: <a href="%s">%s</a>`,
		html.EscapeString(hltvEventURL(m.Tournament)), html.EscapeString(m.Tournament))
}
