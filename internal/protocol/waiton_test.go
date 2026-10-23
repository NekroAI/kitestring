package protocol

import "testing"

func TestParseWaitOn(t *testing.T) {
	for in, want := range map[string]string{
		"review,decision":           "decision,review",
		" Blocked , review,blocked": "review,blocked",
		"stage":                     "stage",
		"none":                      "none",
		"default":                   "default",
	} {
		got, err := ParseWaitOn(in)
		if err != nil || got != want {
			t.Fatalf("ParseWaitOn(%q) = %q, %v；想要 %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", ",", "deploy", "none,review", "default,stage"} {
		if _, err := ParseWaitOn(in); err == nil {
			t.Fatalf("ParseWaitOn(%q) 应报错", in)
		}
	}
}
