package youtube

import "testing"

func TestVideoID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://www.youtube.com/watch?v=Im3OHRzxkeg", "Im3OHRzxkeg"},
		{"https://youtube.com/watch?v=Im3OHRzxkeg&t=10s", "Im3OHRzxkeg"},
		{"https://m.youtube.com/watch?v=Im3OHRzxkeg", "Im3OHRzxkeg"},
		{"https://youtu.be/Im3OHRzxkeg", "Im3OHRzxkeg"},
		{"https://youtu.be/Im3OHRzxkeg?t=12", "Im3OHRzxkeg"},
		{"https://www.youtube.com/embed/Im3OHRzxkeg", "Im3OHRzxkeg"},
		{"https://www.youtube-nocookie.com/embed/Im3OHRzxkeg", "Im3OHRzxkeg"},
		{"https://www.youtube.com/live/Im3OHRzxkeg", "Im3OHRzxkeg"},
		{"https://www.youtube.com/v/Im3OHRzxkeg", "Im3OHRzxkeg"},
		{"https://www.youtube.com/shorts/Im3OHRzxkeg", "Im3OHRzxkeg"},
		{"https://www.twitch.tv/navi", ""},
		{"https://www.youtube.com/results?search_query=cs2", ""},
		{"", ""},
		{"not a url", ""},
		{"https://www.youtube.com/watch?v=", ""},
		{"https://www.youtube.com/watch?v=bad id!", ""},
	}
	for _, tc := range cases {
		if got := VideoID(tc.in); got != tc.want {
			t.Errorf("VideoID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
