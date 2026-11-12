package bot

import (
	"strings"

	"cs2bot/internal/domain"
)

// matchStage показывает только распознанные стадии: неизвестный раунд
// не угадываем по BO, дате или числу матчей в расписании.
func matchStage(m domain.Match) string {
	stage := strings.ToLower(strings.TrimSpace(m.StageName))
	switch {
	case strings.Contains(stage, "group"):
		return "Группа"
	case strings.Contains(stage, "swiss"):
		return "Швейц."
	case strings.Contains(stage, "qualif"), strings.Contains(stage, "open"):
		return "Квал."
	case strings.Contains(stage, "play-in"), strings.Contains(stage, "playin"), strings.Contains(stage, "play in"):
		return "Плей-ин"
	}
	if round := shortRound(m.RoundName); round != "" {
		return round
	}
	if round := shortRound(m.StageName); round != "" {
		return round
	}
	if m.IsPlayoff {
		return "Плей-офф"
	}
	return ""
}

func shortRound(name string) string {
	n := strings.ToLower(name)
	n = strings.NewReplacer("-", " ", "_", " ").Replace(n)
	n = strings.Join(strings.Fields(n), " ")
	bracket := ""
	for _, prefix := range []struct{ text, label string }{
		{"upper bracket ", " верх."}, {"lower bracket ", " ниж."},
		{"winners bracket ", " верх."}, {"losers bracket ", " ниж."},
		{"upper ", " верх."}, {"lower ", " ниж."},
	} {
		if strings.HasPrefix(n, prefix.text) {
			n = strings.TrimPrefix(n, prefix.text)
			bracket = prefix.label
			break
		}
	}
	if n == "round of 16" || n == "round of sixteen" {
		return "1/8" + bracket
	}
	// Номер пары (например, "Semi-final 2") не нужен в подписи.
	n = strings.TrimRight(n, " 0123456789")
	switch n {
	case "grand final", "grand finals", "final", "finals":
		return "Финал" + bracket
	case "semi final", "semi finals", "semifinal", "semifinals":
		return "1/2" + bracket
	case "quarter final", "quarter finals", "quarterfinal", "quarterfinals":
		return "1/4" + bracket
	case "eighth final", "eighth finals", "eighthfinal", "eighthfinals":
		return "1/8" + bracket
	case "third place", "third place match", "3rd place", "3rd place match":
		return "За 3-е"
	}
	return ""
}
