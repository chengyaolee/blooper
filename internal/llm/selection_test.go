package llm

import "testing"

func TestSelectModel(t *testing.T) {
	for _, c := range []struct{ preferred, fallback, want string }{
		{"local", "small", "local"}, {"missing", "small", "small"}, {"missing", "", ""},
	} {
		got, err := SelectModel([]string{"local", "small"}, c.preferred, c.fallback)
		if got != c.want || (err != nil) != (c.want == "") {
			t.Fatalf("%+v: %q %v", c, got, err)
		}
	}
}
