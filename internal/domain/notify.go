package domain

// EventKind — какое уведомление слать по изменению матча.
type EventKind int

const (
	EventNew EventKind = iota
	EventStarted
	EventOpponent
	EventTimeChanged
)

// ClassifyMatchUpdate решает, какое уведомление слать по изменению матча.
// postponed не даёт «новый матч»: иначе связка с CleanOldMatches повторяет
// рассылку каждую минуту. finished — только в БД для кнопки счёта, без пуша.
// Смена времени или соперника уходит как обычно. ok=false — событие не нужно.
func ClassifyMatchUpdate(c MatchChange, status string) (EventKind, bool) {
	switch {
	case status == StatusFinished:
		return 0, false
	case c.IsNew && status == StatusPostponed:
		return 0, false
	case c.IsNew:
		return EventNew, true
	case status == StatusRunning && c.StatusChanged && c.OldStatus != StatusRunning:
		return EventStarted, true
	case c.TeamsChanged:
		return EventOpponent, true
	case c.TimeChanged && !c.MinorTimeShift:
		return EventTimeChanged, true
	default:
		return 0, false
	}
}

// FansForMatchEvent оставляет интересных только на старте матча.
// Любимая сторона в матче получает любое событие.
func FansForMatchEvent(kind EventKind, fans []MatchFan) []MatchFan {
	if kind == EventStarted {
		return fans
	}
	out := make([]MatchFan, 0, len(fans))
	for _, f := range fans {
		if f.HasFavorite() {
			out = append(out, f)
		}
	}
	return out
}
