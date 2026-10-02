package domain

import "time"

type Match struct {
	ID      int
	TeamA   string
	TeamB   string
	TeamAID int
	TeamBID int
	Time    time.Time
	Status  string
	Streams []MatchStream
}

// MatchStream — одна трансляция матча из PandaScore (streams_list).
// URL — прямая ссылка (raw_url, fallback на embed_url).
type MatchStream struct {
	URL      string
	Language string
	Official bool
	Main     bool
}

type TeamInfo struct {
	ID   int
	Name string
}

type SearchedTeam struct {
	ID      int
	Name    string
	Players string
}

type DigestSettings struct {
	Enabled   bool
	HourUTC   int
	UtcOffset int
}

type BotStats struct {
	Users         int
	Subscriptions int
	Matches       int
}

type DigestDueUser struct {
	UserID    int64
	HourUTC   int
	UtcOffset int
	LastSent  string
}
