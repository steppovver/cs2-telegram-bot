package bot

import "time"

// subLevelGrace — пока подписка свежая, следующий клик повышает интересную
// до любимой. Старше этого окна клик снимает подписку и уровень не меняет.
const subLevelGrace = 2 * time.Minute

type subClick int

const (
	subClickInterest subClick = iota
	subClickFavorite
	subClickRemove
)

// nextSubClick решает, что делает клик по команде.
// Нет подписки → интересная. Свежая интересная → любимая.
// Давняя интересная и любая любимая → снятие подписки.
func nextSubClick(subscribed, favorite bool, subscribedAt, now time.Time) subClick {
	if !subscribed {
		return subClickInterest
	}
	if favorite {
		return subClickRemove
	}
	if !subscribedAt.IsZero() && now.Sub(subscribedAt) < subLevelGrace {
		return subClickFavorite
	}
	return subClickRemove
}

func teamButtonLabel(name string, subscribed, favorite bool) string {
	switch {
	case subscribed && favorite:
		return "⭐ " + name
	case subscribed:
		return "✅ " + name
	default:
		return name
	}
}
