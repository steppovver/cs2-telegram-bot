package bot

import (
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/domain"
	"cs2bot/internal/hltv"

	"gopkg.in/telebot.v3"
)

// handleScore показывает счет по явному запросу (кнопка 📊): идущие сейчас
// и завершенные матчи команд пользователя. Только из БД, без API.
// Счет нигде не пушится сам — защита от спойлеров.
func (b *Bot) handleScore(c telebot.Context) error {
	if c.Sender() == nil {
		return nil
	}
	userID := c.Sender().ID
	showAll := b.tournamentViewDefault(userID)
	text, extra, err := b.buildScoreView(userID, showAll)
	if err != nil {
		slog.Error("Ошибка получения матчей со счетом", slog.Int64("user_id", userID), slog.Any("error", err))
		return c.Send("Ошибка получения счета.", telebot.NoPreview)
	}
	return b.sendChunked(c, text, viewToggleMarkup(viewScore, showAll, extra))
}

// buildScoreView собирает текст счета; extra — как в buildScheduleView.
func (b *Bot) buildScoreView(userID int64, showAll bool) (string, int, error) {
	subs, err := b.storage.GetUserSubscriptions(userID)
	if err != nil {
		return "", 0, fmt.Errorf("подписки на команды: %w", err)
	}
	hasTournaments, err := b.storage.HasTournamentSubscriptions(userID)
	if err != nil {
		return "", 0, err
	}
	if len(subs) == 0 && !hasTournaments {
		return noSubscriptionsText, 0, nil
	}

	matches, err := b.storage.GetScoreMatches(userID, hasTournaments)
	if err != nil {
		return "", 0, fmt.Errorf("матчи со счетом: %w", err)
	}
	extra := countExtraMatches(matches, subs)
	if !showAll {
		matches = fanMatchesOnly(matches, subs)
	}
	utcOffset, _ := b.storage.GetUserOffset(userID)
	return formatScoreMessage(matches, subs, utcOffset, showAll && hasTournaments), extra, nil
}

// formatScoreMessage рендерит идущие и завершенные матчи. withTournaments
// влияет только на текст пустого ответа.
func formatScoreMessage(matches []domain.Match, subs []domain.TeamInfo, utcOffset int, withTournaments bool) string {
	var running, finished []domain.Match
	for _, m := range matches {
		switch m.Status {
		case "running":
			running = append(running, m)
		case "finished", "post_match":
			finished = append(finished, m)
		}
	}
	// Обе группы — по возрастанию времени начала, как отдает селект.

	if len(running) == 0 && len(finished) == 0 {
		if withTournaments {
			return "Пока нет матчей со счетом для ваших команд и турниров."
		}
		return "Пока нет матчей со счетом для ваших команд."
	}

	var sb strings.Builder
	writeGroup := func(title string, group []domain.Match, showDate bool, needDivider *bool) {
		if len(group) == 0 {
			return
		}
		if *needDivider {
			sb.WriteString("➖➖➖➖➖➖➖\n")
		}
		sb.WriteString(title + "\n\n")
		for i, m := range group {
			if i > 0 {
				sb.WriteString("➖➖➖➖➖➖➖\n")
			}
			sb.WriteString(buildScoreBlock(m, subs, utcOffset, showDate))
		}
		sb.WriteString("\n")
		*needDivider = true
	}

	// Завершенные режем по турнирам: заголовок группы + матчи.
	writeFinished := func(group []tournamentGroup, needDivider *bool) {
		if len(group) == 0 {
			return
		}
		if *needDivider {
			sb.WriteString("➖➖➖➖➖➖➖\n")
		}
		sb.WriteString("✅ <b>Завершенные:</b>\n\n")
		for gi, g := range group {
			if gi > 0 {
				sb.WriteString("➖➖➖➖➖➖➖\n")
			}
			sb.WriteString(tournamentHeader(g) + "\n")
			for mi, m := range g.Matches {
				if mi > 0 {
					sb.WriteString("➖➖➖➖➖➖➖\n")
				}
				sb.WriteString(buildScoreBlock(m, subs, utcOffset, true))
			}
		}
		sb.WriteString("\n")
		*needDivider = true
	}

	needDivider := false
	writeGroup("🔴 <b>Сейчас идут:</b>", running, false, &needDivider)
	writeFinished(groupMatchesByTournament(finished), &needDivider)

	return strings.TrimSuffix(sb.String(), "\n")
}

// buildScoreBlock форматирует один матч со счетом.
// showDate=true добавляет строку даты проведения (для завершенных).
// Без иконок в строках: карточек много, иконки рябят.
func buildScoreBlock(m domain.Match, subs []domain.TeamInfo, utcOffset int, showDate bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s vs %s\n", highlightTeam(m.TeamA, m.TeamAID, subs), highlightTeam(m.TeamB, m.TeamBID, subs)))
	if showDate && !m.Time.IsZero() {
		sb.WriteString(fmt.Sprintf("Сыгран: %s\n", formatTGTime(m.Time, "dt", "02.01 15:04", utcOffset)))
	}
	if line := seriesLine(m); line != "" {
		sb.WriteString(line + "\n")
	}
	if line := durationLine(m); line != "" {
		sb.WriteString(line + "\n")
	}
	if m.TeamA != "TBD" && m.TeamB != "TBD" {
		sb.WriteString(fmt.Sprintf(`HLTV: <a href="%s">Профиль матча</a>`+"\n", html.EscapeString(hltv.MatchURL(m))))
	}
	return sb.String()
}

// highlightTeam подсвечивает команды пользователя, как в расписании.
func highlightTeam(name string, id int, subs []domain.TeamInfo) string {
	esc := html.EscapeString(name)
	for _, sub := range subs {
		if sub.ID == id {
			return "<b>" + esc + "</b>"
		}
	}
	return esc
}

// boSuffix возвращает " (BO3)" для заголовка матча. 0 = неизвестно, без суффикса.
func boSuffix(m domain.Match) string {
	if m.NumberOfGames <= 0 {
		return ""
	}
	return fmt.Sprintf(" (BO%d)", m.NumberOfGames)
}

// seriesLine строит строку счета серии в порядке команд заголовка.
// Нет данных — пустая строка (блок пропускается).
func seriesLine(m domain.Match) string {
	if len(m.Results) == 0 {
		return ""
	}
	scores := make(map[int]int, len(m.Results))
	for _, r := range m.Results {
		scores[r.TeamID] = r.Score
	}
	a, okA := scores[m.TeamAID]
	bb, okB := scores[m.TeamBID]
	if !okA || !okB {
		return ""
	}
	format := "Серия"
	if m.NumberOfGames > 0 {
		format = fmt.Sprintf("Серия (BO%d)", m.NumberOfGames)
	}
	return fmt.Sprintf("%s: %s %d — %d %s",
		format, html.EscapeString(m.TeamA), a, bb, html.EscapeString(m.TeamB))
}

// durationLine строит строку длительности: для идущих — от начала до сейчас,
// для завершенных — от начала до конца. Нет данных — пустая строка.
func durationLine(m domain.Match) string {
	if m.Time.IsZero() {
		return ""
	}
	if m.Status == "running" {
		if d := time.Since(m.Time); d > 0 {
			return "Идет: " + formatDuration(d)
		}
		return ""
	}
	if !m.EndAt.IsZero() && m.EndAt.After(m.Time) {
		return "Длился: " + formatDuration(m.EndAt.Sub(m.Time))
	}
	return ""
}

// formatDuration показывает длительность как "47 мин" или "1 ч 23 мин".
func formatDuration(d time.Duration) string {
	total := int(d.Minutes())
	if total < 1 {
		return "меньше минуты"
	}
	if total < 60 {
		return fmt.Sprintf("%d мин", total)
	}
	return fmt.Sprintf("%d ч %02d мин", total/60, total%60)
}
