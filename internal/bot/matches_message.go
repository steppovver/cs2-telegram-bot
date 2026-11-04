package bot

import (
	"fmt"
	"strings"

	"cs2bot/internal/domain"
)

// formatMatchCard — общая карточка live / старта / напоминания / счёта.
// teamA/teamB уже готовые HTML-фрагменты (highlight или <b>).
// withTournament=false, если турнир уже в заголовке группы (завершённые в счёте).
// middle — строки между турниром и стримами (серия, длительность, дата).
func formatMatchCard(m domain.Match, teamA, teamB string, withTournament bool, middle []string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s vs %s%s\n", teamA, teamB, boSuffix(m)))
	if withTournament {
		if line := tournamentLine(m); line != "" {
			sb.WriteString(line + "\n")
		}
	}
	for _, line := range middle {
		if line == "" {
			continue
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString(streamLine(m))
	if line := hltvMatchLine(m); line != "" {
		sb.WriteString("\n" + line)
	}
	return sb.String()
}

// formatMatchesMessage собирает текст расписания/дайджеста.
// header — опциональная шапка (у дайджеста); тело одинаковое: live, затем upcoming.
func formatMatchesMessage(header string, live, upcoming []domain.Match, subs []domain.TeamInfo, utcOffset, maxStreams int) string {
	_ = maxStreams // конфиг остаётся, на рендер больше не влияет
	var sb strings.Builder
	if header != "" {
		sb.WriteString(header)
		sb.WriteString("\n\n")
	}

	if len(live) > 0 {
		sb.WriteString("🔴 <b>Сейчас играют:</b>\n\n")
		for i, match := range live {
			teamA := highlightTeam(match.TeamA, match.TeamAID, subs)
			teamB := highlightTeam(match.TeamB, match.TeamBID, subs)
			if i > 0 {
				sb.WriteString("➖➖➖➖➖➖➖\n")
			}
			sb.WriteString(formatMatchCard(match, teamA, teamB, true, nil))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if len(upcoming) > 0 {
		sb.WriteString("📅 <b>Матчи:</b>\n\n")
		for gi, group := range groupMatchesByTournament(upcoming) {
			if gi > 0 {
				sb.WriteString("➖➖➖➖➖➖➖\n")
			}
			sb.WriteString(tournamentHeader(group) + "\n")
			for _, match := range group.Matches {
				timeStr := formatTGTime(match.Time, "dt", "02.01 15:04", utcOffset)
				teamA := highlightTeam(match.TeamA, match.TeamAID, subs)
				teamB := highlightTeam(match.TeamB, match.TeamBID, subs)
				sb.WriteString(fmt.Sprintf("%s | %s vs %s%s\n", timeStr, teamA, teamB, boSuffix(match)))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
