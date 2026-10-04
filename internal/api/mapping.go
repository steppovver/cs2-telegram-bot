package api

import (
	"sort"
	"strings"

	"cs2bot/internal/domain"
)

// mapPandaMatch переводит ответ API в доменную модель.
// Возвращает ok=false для матчей, которые нельзя показать (canceled,
// begin_at=null, нет команд).
func mapPandaMatch(pm pandaMatch) (domain.Match, bool) {
	var zero domain.Match
	if pm.Status == "canceled" {
		return zero, false
	}
	// begin_at=null в API -> zero time: такой матч нельзя показать
	// и напомнить о нем, пропускаем до появления времени.
	if pm.BeginAt.IsZero() {
		return zero, false
	}

	teamA, teamB, teamAID, teamBID, ok := mapPandaTeams(pm.Opponents)
	if !ok {
		return zero, false
	}

	m := domain.Match{
		ID:                pm.ID,
		TeamA:             teamA,
		TeamB:             teamB,
		TeamAID:           teamAID,
		TeamBID:           teamBID,
		Time:              pm.BeginAt,
		Status:            pm.Status,
		NumberOfGames:     pm.NumberOfGames,
		Streams:           mapPandaStreams(pm),
		Tournament:        mapPandaTournament(pm),
		TournamentID:      pm.Serie.ID,
		TournamentBeginAt: pm.Serie.BeginAt,
	}
	if pm.EndAt != nil && !pm.EndAt.IsZero() {
		m.EndAt = *pm.EndAt
	}
	m.Results = mapPandaResults(pm.Results)
	m.Games = mapPandaGames(pm.Games)
	return m, true
}

// mapPandaMatches маппит страницу, молча пропуская непоказуемое.
func mapPandaMatches(pandaMatches []pandaMatch) []domain.Match {
	var out []domain.Match
	for _, pm := range pandaMatches {
		if m, ok := mapPandaMatch(pm); ok {
			out = append(out, m)
		}
	}
	return out
}

// mapPandaTournament собирает название турнира для ссылки на HLTV:
// "ESL Pro League Season 24 2026". Год отсекает прошлые розыгрыши.
// Пустые части пропускаются, совсем пусто — пустая строка.
func mapPandaTournament(pm pandaMatch) string {
	var parts []string
	if s := strings.TrimSpace(pm.League.Name); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(pm.Serie.FullName); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// mapPandaTeams достает пару команд. API не гарантирует порядок opponents,
// а пустой список или нулевые ID означают TBD — такой матч пропускаем.
func mapPandaTeams(opponents []pandaOpponent) (teamA, teamB string, teamAID, teamBID int, ok bool) {
	teamA, teamB = "TBD", "TBD"
	if len(opponents) > 0 {
		teamA, teamAID = opponents[0].Opponent.Name, opponents[0].Opponent.ID
	}
	if len(opponents) > 1 {
		teamB, teamBID = opponents[1].Opponent.Name, opponents[1].Opponent.ID
	}
	if teamAID == 0 && teamBID == 0 {
		return "", "", 0, 0, false
	}
	return teamA, teamB, teamAID, teamBID, true
}

// mapPandaResults переводит счет серии как есть.
func mapPandaResults(results []pandaResult) []domain.MatchResult {
	var out []domain.MatchResult
	for _, r := range results {
		out = append(out, domain.MatchResult{TeamID: r.TeamID, Score: r.Score})
	}
	return out
}

// mapPandaGames переводит карты, сортируя по позиции.
// Победитель null (карта не сыграна) дает WinnerID=0.
func mapPandaGames(games []pandaGame) []domain.MatchGame {
	var out []domain.MatchGame
	for _, g := range games {
		game := domain.MatchGame{
			Position: g.Position,
			Status:   g.Status,
			Finished: g.Finished,
		}
		if g.Winner.ID != nil {
			game.WinnerID = *g.Winner.ID
		}
		out = append(out, game)
	}
	// API обычно отдает по порядку, но не гарантирует.
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

// mapPandaStreams выбирает все трансляции из streams_list (без обрезки:
// лимит применяется при рендере через max_streams_per_match).
// Приоритет: official + main, затем русский, затем английский.
// URL: raw_url предпочтительнее embed_url. Дубли по URL убираем.
// Если streams_list пуст, но есть official_stream_url — возвращаем его.
func mapPandaStreams(pm pandaMatch) []domain.MatchStream {
	var out []domain.MatchStream
	seen := make(map[string]bool)
	for _, s := range pm.StreamsList {
		u := strings.TrimSpace(s.RawURL)
		if u == "" {
			u = strings.TrimSpace(s.EmbedURL)
		}
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, domain.MatchStream{
			URL:      u,
			Language: strings.ToLower(strings.TrimSpace(s.Language)),
			Official: s.Official,
			Main:     s.Main,
		})
	}
	if len(out) == 0 {
		if u := strings.TrimSpace(pm.OfficialStreamURL); u != "" {
			return []domain.MatchStream{{URL: u, Official: true, Main: true}}
		}
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return streamScore(out[i]) > streamScore(out[j]) })
	return out
}

// streamScore — вес трансляции для сортировки.
func streamScore(s domain.MatchStream) int {
	n := 0
	if s.Official {
		n += 4
	}
	if s.Main {
		n += 2
	}
	switch s.Language {
	case "ru":
		n += 3
	case "en":
		n += 1
	}
	return n
}
