package database

import "testing"

func TestLikeContainsPattern(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "admin", want: "%admin%"},
		{name: "percent", input: "50%", want: "%50!%%"},
		{name: "underscore", input: "a_b", want: "%a!_b%"},
		{name: "escape", input: "a!b", want: "%a!!b%"},
		{name: "all metacharacters", input: "!%_", want: "%!!!%!_%"},
		{name: "empty", input: "", want: "%%"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := LikeContainsPattern(test.input); got != test.want {
				t.Fatalf("LikeContainsPattern(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}
