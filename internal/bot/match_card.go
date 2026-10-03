package bot

import (
	"fmt"
	"html"
	"strings"

	"cs2bot/internal/domain"
)

// formatMatchCard — единая карточка матча со стримами для всех мест:
// расписание (live), напоминания, уведомления о старте.
// Всегда: bold обеих команд, boSuffix, строка времени dt, streamLines.
// Строка HLTV добавляется только когда ссылка уже найдена.
func formatMatchCard(m domain.Match, utcOffset, maxStreams int) string {
	title := fmt.Sprintf("🎮 <b>%s</b> vs <b>%s</b>%s",
		html.EscapeString(m.TeamA), html.EscapeString(m.TeamB), boSuffix(m))
	timeStr := formatTGTime(m.Time, "dt", "15:04 02.01", utcOffset)
	card := title + "\n⏰ Время: " + timeStr + "\n" + streamLine(m, maxStreams)
	if line := hltvLine(m); line != "" {
		card += "\n" + line
	}
	return card
}

// hltvLine возвращает строку ссылки на профиль матча.
// Пустая ссылка — пустая строка, рендер ее пропускает.
func hltvLine(m domain.Match) string {
	if strings.TrimSpace(m.HltvURL) == "" {
		return ""
	}
	return fmt.Sprintf(`📊 HLTV: <a href="%s">Статистика матча</a>`, html.EscapeString(m.HltvURL))
}
