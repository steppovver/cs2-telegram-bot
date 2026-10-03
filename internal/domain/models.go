package domain

import "time"

type Match struct {
	ID      int
	TeamA   string
	TeamB   string
	TeamAID int
	TeamBID int
	Time    time.Time
	EndAt   time.Time
	Status  string
	Streams []MatchStream
	Results []MatchResult
	Games   []MatchGame
}

// MatchResult — счет серии: очки команды с team_id.
type MatchResult struct {
	TeamID int `json:"team_id"`
	Score  int `json:"score"`
}

// MatchGame — одна карта серии. WinnerID = 0, пока победитель неизвестен.
type MatchGame struct {
	Position int    `json:"position"`
	Status   string `json:"status"`
	Finished bool   `json:"finished"`
	WinnerID int    `json:"winner_id"`
}

// MatchStream — одна трансляция матча из PandaScore (streams_list).
// URL — прямая ссылка (raw_url, fallback на embed_url).
// JSON-теги в нижнем регистре — так строка выглядит в колонке streams_json.
type MatchStream struct {
	URL      string `json:"url"`
	Language string `json:"language"`
	Official bool   `json:"official"`
	Main     bool   `json:"main"`
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
