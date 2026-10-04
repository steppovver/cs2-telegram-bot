package api

import "time"

// Типы ответа PandaScore (поле opponents/status/streams_list/results/games).
// Именованные вместо анонимных вложенностей — так файл читается сверху вниз.

type pandaOpponent struct {
	Opponent pandaTeam `json:"opponent"`
}

type pandaTeam struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type pandaStream struct {
	EmbedURL string `json:"embed_url"`
	Language string `json:"language"`
	Main     bool   `json:"main"`
	Official bool   `json:"official"`
	RawURL   string `json:"raw_url"`
}

type pandaResult struct {
	Score  int `json:"score"`
	TeamID int `json:"team_id"`
}

type pandaGameWinner struct {
	ID *int `json:"id"`
}

type pandaGame struct {
	Position int             `json:"position"`
	Status   string          `json:"status"`
	Finished bool            `json:"finished"`
	Winner   pandaGameWinner `json:"winner"`
}

type pandaMatch struct {
	ID                int             `json:"id"`
	BeginAt           time.Time       `json:"begin_at"`
	EndAt             *time.Time      `json:"end_at"`
	Status            string          `json:"status"`
	NumberOfGames     int             `json:"number_of_games"`
	OfficialStreamURL string          `json:"official_stream_url"`
	Results           []pandaResult   `json:"results"`
	Games             []pandaGame     `json:"games"`
	StreamsList       []pandaStream   `json:"streams_list"`
	Opponents         []pandaOpponent `json:"opponents"`
	League            pandaLeague     `json:"league"`
	Serie             pandaSerie      `json:"serie"`
}

type pandaLeague struct {
	Name string `json:"name"`
}

type pandaSerie struct {
	ID       int       `json:"id"`
	FullName string    `json:"full_name"`
	BeginAt  time.Time `json:"begin_at"`
}
