package relaymsg

import (
	"regexp"
	"slices"
	"testing"
)

var ph = regexp.MustCompile(`\{[a-z_]+\}`)

// Every message has both languages with the same placeholders.
func TestBothLocales(t *testing.T) {
	for _, id := range IDs() {
		raw, _ := Raw(id)
		if raw[0] == "" || raw[1] == "" {
			t.Errorf("%s: missing text %q", id, raw)
		}
		zh, en := ph.FindAllString(raw[0], -1), ph.FindAllString(raw[1], -1)
		slices.Sort(zh)
		slices.Sort(en)
		if !slices.Equal(zh, en) {
			t.Errorf("%s: placeholders differ %v %v", id, zh, en)
		}
	}
	if len(IDs()) < 40 {
		t.Fatalf("only %d messages", len(IDs()))
	}
}

func TestText(t *testing.T) {
	if got := Text("", ModelNotAllowed, map[string]string{"model": "x"}); got != `该 API key 不允许使用模型 "x"` {
		t.Fatal(got)
	}
	if got := Text("en", ModelNotAllowed, map[string]string{"model": "x"}); got != `model "x" is not allowed for this API key` {
		t.Fatal(got)
	}
	if Norm("en-US") != EN || Norm("zh-CN") != ZH || Norm("") != ZH {
		t.Fatal("Norm")
	}
	if Text("en", "nope", nil) != "nope" {
		t.Fatal("unknown id")
	}
}
