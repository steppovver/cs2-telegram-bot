package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cs2bot/internal/domain"
)

type Client struct {
	apiKey     string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
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

func (c *Client) FetchMatchesByTeamIDs(ctx context.Context, teamIDs []string) ([]domain.Match, error) {
	if len(teamIDs) == 0 {
		return nil, nil
	}

	joinedIDs := strings.Join(teamIDs, ",")
	url := fmt.Sprintf("https://api.pandascore.co/csgo/matches/upcoming?filter[opponent_id]=%s&filter[status]=not_started,postponed,running&sort=begin_at&per_page=100", joinedIDs)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	var resp *http.Response
	var doErr error

	for attempt := 1; attempt <= 3; attempt++ {
		resp, doErr = c.httpClient.Do(req)
		if doErr == nil {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}

	if doErr != nil {
		return nil, fmt.Errorf("ошибка запроса после 3 попыток: %w", doErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %d", resp.StatusCode)
	}

	var pandaMatches []pandaMatch
	if err := json.NewDecoder(resp.Body).Decode(&pandaMatches); err != nil {
		return nil, err
	}

	var matches []domain.Match
	for _, pm := range pandaMatches {
		if pm.Status == "canceled" {
			continue
		}

		teamA, teamB := "TBD", "TBD"
		teamAID, teamBID := 0, 0

		if len(pm.Opponents) > 0 {
			teamA = pm.Opponents[0].Opponent.Name
			teamAID = pm.Opponents[0].Opponent.ID
		}
		if len(pm.Opponents) > 1 {
			teamB = pm.Opponents[1].Opponent.Name
			teamBID = pm.Opponents[1].Opponent.ID
		}

		if teamAID == 0 && teamBID == 0 {
			continue
		}

		matches = append(matches, domain.Match{
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
