package safeguard

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// PromptMaxRunes bounds the prompt text stored with an event or a block.
const PromptMaxRunes = 4000

// systemReminder matches the <system-reminder> blocks clients such as Claude
// Code add to user messages; their content (dates, file lists, todo state)
// changes between otherwise identical requests, so it is not part of the
// prompt.
var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// Prompt extracts the normalized prompt of a Claude Messages request body:
//
//  1. take the last message whose role is "user";
//  2. its text: a string content as is, or the text of its "text" blocks
//     joined with a newline;
//  3. remove every <system-reminder>…</system-reminder> segment;
//  4. when nothing is left, use the text of its tool_result blocks instead
//     (a string content, or the text of the result's "text" blocks), with
//     system reminders removed the same way;
//  5. collapse every run of whitespace (spaces, tabs, newlines) into one
//     space and trim both ends.
//
// It returns "" when the body has no user message with text.
func Prompt(body []byte) string {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return ""
	}
	arr := msgs.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		m := arr[i]
		if m.Get("role").String() != "user" {
			continue
		}
		content := m.Get("content")
		if content.Type == gjson.String {
			return normalize(content.String())
		}
		var texts, results []string
		for _, b := range content.Array() {
			switch b.Get("type").String() {
			case "text":
				texts = append(texts, b.Get("text").String())
			case "tool_result":
				results = append(results, blockText(b.Get("content")))
			}
		}
		if t := normalize(strings.Join(texts, "\n")); t != "" {
			return t
		}
		return normalize(strings.Join(results, "\n"))
	}
	return ""
}

func blockText(c gjson.Result) string {
	if c.Type == gjson.String {
		return c.String()
	}
	var parts []string
	for _, b := range c.Array() {
		if b.Get("type").String() == "text" {
			parts = append(parts, b.Get("text").String())
		}
	}
	return strings.Join(parts, "\n")
}

func normalize(s string) string {
	s = systemReminder.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// Hash is the blocklist hash of a normalized prompt: the hex sha256 of its
// UTF-8 bytes, or "" when it has fewer than minRunes characters (short
// prompts such as "continue" are too common to block site-wide).
func Hash(prompt string, minRunes int) string {
	if prompt == "" || utf8.RuneCountInString(prompt) < minRunes {
		return ""
	}
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// Truncate cuts s to at most n runes.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
