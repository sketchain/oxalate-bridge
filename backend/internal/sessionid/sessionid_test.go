package sessionid

import (
	"net/http"
	"testing"
)

// Expected values were produced by CLIProxyAPI v8.0.80's
// session.ExtractSessionInfo for the same inputs.
func TestExtractMatchesCLIProxyAPI(t *testing.T) {
	for i, tc := range []struct {
		headers map[string]string
		body    string
		want    string
	}{
		{map[string]string{"X-Claude-Code-Session-Id": "s1"}, "{}", "claude:s1"},
		{map[string]string{"X-Claude-Code-Agent-Id": "a1", "X-Claude-Code-Session-Id": "s1"}, "{}", "claude:s1:agent:a1"},
		{map[string]string{"X-Claude-Code-Session-Id": "s1"}, "{\"metadata\":{\"agent_id\":\"rev\"}}", "claude:s1:agent:rev"},
		{map[string]string(nil), "{\"metadata\":{\"user_id\":\"user_abc_account__session_1234abcd-0000\"}}", "claude:1234abcd-0000"},
		{map[string]string(nil), "{\"metadata\":{\"user_id\":\"{\\\"session_id\\\":\\\"sx\\\",\\\"agent_id\\\":\\\"ag\\\"}\"}}", "claude:sx:agent:ag"},
		{map[string]string(nil), "{\"metadata\":{\"user_id\":\"{\\\"session_id\\\":\\\"sx\\\"}\"}}", "claude:sx"},
		{map[string]string{"Session_id": "cs1"}, "{}", "codex:cs1"},
		{map[string]string{"Session_id": "cs1", "Thread_id": "t2"}, "{}", "codex:t2"},
		{map[string]string{"Session_id": "cs1", "X-Codex-Turn-Metadata": "{\"agent_name\":\"/root/check\",\"subagent_kind\":\"thread_spawn\"}"}, "{}", "codex:cs1:agent:check"},
		{map[string]string{"Session_id": "cs1", "X-Codex-Turn-Metadata": "{\"forked_from_thread_id\":\"p\"}"}, "{}", "codex:cs1"},
		{map[string]string{"X-Codex-Turn-Metadata": "{\"thread_id\":\"tt\"}"}, "{}", "codex:tt"},
		{map[string]string{"X-Http-Session-Id": "agy1"}, "{}", "agy:agy1"},
		{map[string]string{"X-Session-ID": "g1"}, "{}", "header:g1"},
		{map[string]string{"X-Session-Affinity": "o1"}, "{}", "affinity:o1"},
		{map[string]string{"X-Slot-Session-Id": "p1"}, "{}", "slot:p1"},
		{map[string]string{"X-Task-Id": "tk"}, "{}", "task:tk"},
		{map[string]string{"X-Conversation-Id": "cv"}, "{}", "conv:cv"},
		{map[string]string{"X-Thread-Id": "th"}, "{}", "thread:th"},
		{map[string]string{"X-Client-Request-Id": "cr"}, "{}", "clientreq:cr"},
		{map[string]string(nil), "{\"cachedContent\":\"cc\"}", "geminicache:cc"},
		{map[string]string(nil), "{\"thread_id\":\"tp\"}", "thread:tp"},
		{map[string]string(nil), "{\"session_id\":\"sp\",\"metadata\":{\"agent_id\":\"w\"}}", "session:sp:agent:w"},
		{map[string]string(nil), "{\"session_id\":\"sp\"}", "session:sp"},
		{map[string]string(nil), "{\"task_id\":\"tq\"}", "task:tq"},
		{map[string]string(nil), "{\"prompt_cache_key\":\"pk\",\"conversation\":{\"id\":\"cv\"}}", "pck:pk"},
		{map[string]string(nil), "{\"conversation\":\"cvs\"}", "conv:cvs"},
		{map[string]string(nil), "{\"metadata\":{\"user_id\":\"plainuser\"}}", "user:plainuser"},
		{map[string]string(nil), "{\"chat_id\":\"ch\"}", "conv:ch"},
		{map[string]string(nil), "{\"request\":{\"session_id\":\"nested\"}}", "session:nested"},
		{map[string]string(nil), "{\"model\":\"x\",\"messages\":[]}", ""},
		{map[string]string(nil), "not json", ""},
	} {
		h := http.Header{}
		for k, v := range tc.headers {
			h.Set(k, v)
		}
		if got := Extract(h, []byte(tc.body)); got != tc.want {
			t.Errorf("case %d (%v %s): got %q, want %q", i, tc.headers, tc.body, got, tc.want)
		}
	}
}
