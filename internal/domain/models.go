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
