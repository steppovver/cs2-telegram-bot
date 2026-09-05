package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Match struct {
	ID      int
	TeamA   string
	TeamB   string
	TeamAID int
	TeamBID int
	Time    time.Time
}

type pandaMatch struct {
	ID        int       `json:"id"`
	BeginAt   time.Time `json:"begin_at"`
	Status    string    `json:"status"`
	Opponents []struct {
		Opponent struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"opponent"`
	} `json:"opponents"`
}

// FetchMatchesByTeamIDs принимает массив ID команд и делает один общий запрос
func FetchMatchesByTeamIDs(apiKey string, teamIDs []string) ([]Match, error) {
	if len(teamIDs) == 0 {
		return nil, nil
	}

	// Склеиваем массив в строку через запятую: "124523,130564,135177"
	joinedIDs := strings.Join(teamIDs, ",")

	// Обязательно увеличиваем per_page, так как матчей для 10 команд будет намного больше
	url := fmt.Sprintf("https://api.pandascore.co/csgo/matches/upcoming?filter[opponent_id]=%s&filter[status]=not_started,postponed,running&sort=begin_at&per_page=100", joinedIDs)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %d", resp.StatusCode)
	}

	var pandaMatches []pandaMatch
	if err := json.NewDecoder(resp.Body).Decode(&pandaMatches); err != nil {
		return nil, err
	}

	var matches []Match
	for _, pm := range pandaMatches {
		if pm.Status == "canceled" {
			continue
		}

		// Задаем значения по умолчанию
		teamA := "TBD"
		teamB := "TBD"
		teamAID := 0
		teamBID := 0

		// Безопасно достаем первую команду, если она есть
		if len(pm.Opponents) > 0 {
			teamA = pm.Opponents[0].Opponent.Name
			teamAID = pm.Opponents[0].Opponent.ID
		}

		// Безопасно достаем вторую команду, если она есть
		if len(pm.Opponents) > 1 {
			teamB = pm.Opponents[1].Opponent.Name
			teamBID = pm.Opponents[1].Opponent.ID
		}

		// Пропускаем матч, только если вообще ни одной команды не известно
		// (хотя при поиске по ID такое вряд ли придет, но лучше перестраховаться)
		if teamAID == 0 && teamBID == 0 {
			continue
		}

		matches = append(matches, Match{
			ID:      pm.ID,
			TeamA:   teamA,
			TeamB:   teamB,
			TeamAID: teamAID,
			TeamBID: teamBID,
			Time:    pm.BeginAt,
		})
	}
	return matches, nil
}
