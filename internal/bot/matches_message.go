package bot

import (
	"fmt"
	"strings"

	"cs2bot/internal/domain"
)

// formatMatchesMessage собирает текст расписания/дайджеста.
// header — опциональная шапка (у дайджеста); тело одинаковое: live, затем upcoming.
func formatMatchesMessage(header string, live, upcoming []domain.Match, subs []domain.TeamInfo, utcOffset, maxStreams int) string {
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
			card := fmt.Sprintf("🎮 %s vs %s%s\n", teamA, teamB, boSuffix(match))
			if line := tournamentLine(match); line != "" {
				card += line + "\n"
			}
			sb.WriteString(card + fmt.Sprintf("%s\n", streamLine(match, maxStreams)))
			if line := hltvMatchLine(match); line != "" {
				sb.WriteString(line + "\n")
			}
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
