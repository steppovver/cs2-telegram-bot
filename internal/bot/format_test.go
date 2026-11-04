package bot

import "testing"

func TestChunkLinesSplitsLongLine(t *testing.T) {
	got := chunkLines("abcdef", 2)
	want := []string{"ab", "cd", "ef"}
	if len(got) != len(want) {
		t.Fatalf("chunks = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunks = %q, want %q", got, want)
		}
	}
	joined := chunkLines("aa\nbbb\n", 4)
	if len(joined) != 2 || joined[0] != "aa\n" || joined[1] != "bbb\n" {
		t.Fatalf("newline chunks = %q", joined)
	}
}
