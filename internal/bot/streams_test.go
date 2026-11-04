package bot

import (
	"strings"
	"testing"

	"cs2bot/internal/domain"
)

func TestStreamLinkTextYouTubeChannel(t *testing.T) {
	got := streamLinkText(domain.MatchStream{
		URL:     "https://www.youtube.com/watch?v=Im3OHRzxkeg",
		Channel: "NAVI",
	})
	if got != "NAVI (YouTube)" {
		t.Errorf("got %q", got)
	}
}

func TestStreamLinkTextYouTubeNoChannel(t *testing.T) {
	got := streamLinkText(domain.MatchStream{
		URL: "https://www.youtube.com/watch?v=Im3OHRzxkeg",
	})
	if got != "YouTube" {
		t.Errorf("got %q", got)
	}
}

func TestStreamLinkTextTwitchFromURL(t *testing.T) {
	got := streamLinkText(domain.MatchStream{
		URL:     "https://www.twitch.tv/navi",
		Channel: "ignored", // Twitch берет канал из URL
	})
	if got != "navi (Twitch)" {
		t.Errorf("got %q", got)
	}
}

func TestStreamLinesUsesYouTubeChannel(t *testing.T) {
	m := domain.Match{
		TeamA: "A", TeamB: "B",
		Streams: []domain.MatchStream{
			{URL: "https://www.youtube.com/watch?v=Im3OHRzxkeg", Official: true, Channel: "ESL"},
		},
	}
	joined := strings.Join(streamLines(m), "\n")
	if !strings.Contains(joined, "ESL (YouTube)") {
		t.Errorf("lines missing channel label: %s", joined)
	}
	if strings.Contains(joined, "⭐") || strings.Contains(joined, "📺") || strings.Contains(joined, "🔍") {
		t.Errorf("stream lines must have no emoji: %s", joined)
	}
}

func TestChannelsRelated(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"ESLCS", "ESL Counter-Strike", true},
		{"ESLCSA", "ESL Counter-Strike", true},
		{"ESLCSB", "ESL Counter-Strike", true},
		{"ESLCSD", "ESL Counter-Strike", true},
		{"BLAST", "BLAST Premier", true},
		{"ESLCS", "bysl4m", false},
		{"navi", "NAVI", true},
		{"", "ESL Counter-Strike", false},
		{"ab", "abc", false},
	}
	for _, tc := range cases {
		if got := channelsRelated(tc.a, tc.b); got != tc.want {
			t.Errorf("channelsRelated(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := channelsRelated(tc.b, tc.a); got != tc.want {
			t.Errorf("channelsRelated(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestChannelBaseFormParallelSuffix(t *testing.T) {
	if got := channelBaseForm("eslcsa"); got != "esl" {
		t.Errorf("eslcsa base = %q, want esl", got)
	}
	if got := channelBaseForm("eslcse"); got != "eslcse" {
		t.Errorf("eslcse must keep trailing e, got %q", got)
	}
	if got := channelBaseForm("navi"); got != "navi" {
		t.Errorf("navi must not strip letter, got %q", got)
	}
}

func TestStreamLinesPromoteRelatedYouTube(t *testing.T) {
	m := domain.Match{
		TeamA: "Aurora Gaming", TeamB: "Spirit",
		Streams: []domain.MatchStream{
			{URL: "https://www.twitch.tv/ESLCS", Official: true},
			{URL: "https://www.youtube.com/watch?v=82y98I__G4o", Channel: "ESL Counter-Strike"},
			{URL: "https://www.twitch.tv/bysl4m"},
		},
	}
	joined := strings.Join(streamLines(m), "\n")
	official := lineWith(joined, "Официальная трансляция:")
	if !strings.Contains(official, "ESL Counter-Strike (YouTube)") {
		t.Errorf("YouTube must be in official line: %s", official)
	}
	if strings.Contains(joined, "bysl4m") {
		t.Errorf("unofficial stream must not appear: %s", joined)
	}
	if strings.Contains(joined, "📺") || strings.Contains(joined, "⭐") {
		t.Errorf("no watch/star emoji: %s", joined)
	}
	if !strings.Contains(joined, "Найти трансляцию:") {
		t.Errorf("fallback search missing: %s", joined)
	}
}

func TestStreamLinesPromoteESLCSA(t *testing.T) {
	m := domain.Match{
		TeamA: "A", TeamB: "B",
		Streams: []domain.MatchStream{
			{URL: "https://www.twitch.tv/ESLCSA", Official: true},
			{URL: "https://www.youtube.com/watch?v=abcdEFGHijk", Channel: "ESL Counter-Strike"},
		},
	}
	joined := strings.Join(streamLines(m), "\n")
	if !strings.Contains(lineWith(joined, "Официальная трансляция:"), "ESL Counter-Strike (YouTube)") {
		t.Errorf("ESLCSA must promote YouTube: %s", joined)
	}
}

func TestStreamLinesSkipPromoteWhenYouTubeAlreadyOfficial(t *testing.T) {
	m := domain.Match{
		TeamA: "A", TeamB: "B",
		Streams: []domain.MatchStream{
			{URL: "https://www.youtube.com/watch?v=officialVID1", Official: true, Channel: "ESL Counter-Strike"},
			{URL: "https://www.twitch.tv/ESLCS", Official: true},
			{URL: "https://www.youtube.com/watch?v=unofficialVID", Channel: "ESL Counter-Strike"},
		},
	}
	joined := strings.Join(streamLines(m), "\n")
	official := lineWith(joined, "Официальная трансляция:")
	if strings.Contains(official, "unofficialVID") {
		t.Errorf("must not promote when YT already official: %s", official)
	}
	if strings.Contains(joined, "unofficialVID") {
		t.Errorf("unofficial YT must not appear: %s", joined)
	}
}

func TestStreamLinesNoPromoteWithoutChannel(t *testing.T) {
	m := domain.Match{
		TeamA: "A", TeamB: "B",
		Streams: []domain.MatchStream{
			{URL: "https://www.twitch.tv/ESLCS", Official: true},
			{URL: "https://www.youtube.com/watch?v=82y98I__G4o"},
		},
	}
	joined := strings.Join(streamLines(m), "\n")
	if strings.Contains(lineWith(joined, "Официальная трансляция:"), "youtube.com") {
		t.Errorf("YouTube without Channel must not promote: %s", joined)
	}
	if strings.Contains(joined, "82y98I__G4o") {
		t.Errorf("unofficial YouTube must not appear: %s", joined)
	}
}

func TestStreamLinesNoOfficialOnlyFallback(t *testing.T) {
	m := domain.Match{
		TeamA: "A", TeamB: "B",
		Streams: []domain.MatchStream{
			{URL: "https://www.twitch.tv/bysl4m"},
		},
	}
	joined := strings.Join(streamLines(m), "\n")
	if strings.Contains(joined, "Официальная трансляция:") {
		t.Errorf("no official line without official streams: %s", joined)
	}
	if !strings.HasPrefix(joined, "Найти трансляцию:") {
		t.Errorf("only fallback expected: %s", joined)
	}
}

func lineWith(joined, prefix string) string {
	for _, line := range strings.Split(joined, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}
