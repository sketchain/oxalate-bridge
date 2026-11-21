// Package sessionid derives the session identity used for account affinity.
//
// It ports the SessionID derivation of CLIProxyAPI v8's
// sdk/cliproxy/session.ExtractSessionInfo (same sources, same priority, same
// prefixes) without importing that module, whose dependency tree would add
// ~15 MB to the binary. Parent/agent metadata that does not influence the
// session id is not reproduced.
package sessionid

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

var legacyClaudeSession = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// normalize mirrors session.NormalizeExplicitID.
func normalize(raw string) string {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 256 {
		return ""
	}
	return raw
}

func header(h http.Header, name string) string {
	if h == nil {
		return ""
	}
	if v := normalize(h.Get(name)); v != "" {
		return v
	}
	for key, values := range h {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, v := range values {
			if n := normalize(v); n != "" {
				return n
			}
		}
	}
	return ""
}

type body struct {
	root      gjson.Result
	req       gjson.Result
	hasNested bool
}

func (b body) get(path string) string {
	if v := normalize(b.root.Get(path).String()); v != "" {
		return v
	}
	if b.hasNested {
		return normalize(b.req.Get(path).String())
	}
	return ""
}

func (b body) first(paths ...string) string {
	for _, p := range paths {
		if v := b.get(p); v != "" {
			return v
		}
	}
	return ""
}

// claudeMetadata mirrors session.ClaudeMetadataIdentities (session and agent).
func claudeMetadata(b body) (sid, agent string) {
	userID := strings.TrimSpace(b.root.Get("metadata.user_id").String())
	if userID == "" && b.hasNested {
		userID = strings.TrimSpace(b.req.Get("metadata.user_id").String())
	}
	if userID == "" {
		return "", ""
	}
	if strings.HasPrefix(userID, "{") {
		parsed := gjson.Parse(userID)
		sid = normalize(parsed.Get("session_id").String())
		agent = normalize(parsed.Get("agent_id").String())
		if agent == "" {
			agent = normalize(parsed.Get("subagent_id").String())
		}
		return sid, agent
	}
	if m := legacyClaudeSession.FindStringSubmatch(userID); len(m) >= 2 {
		sid = normalize(m[1])
		agent = normalize(b.root.Get("metadata.agent_id").String())
		if agent == "" {
			agent = normalize(b.root.Get("metadata.subagent_id").String())
		}
	}
	return sid, agent
}

func bodyAgent(b body) string {
	return b.first("metadata.agent_id", "metadata.subagent_id")
}

// Extract returns the session identity of a request, or "" when the request
// carries none (the caller then falls back to the API key id).
func Extract(h http.Header, payload []byte) string {
	var b body
	if len(payload) > 0 && gjson.ValidBytes(payload) {
		b.root = gjson.ParseBytes(payload)
		req := b.root.Get("request")
		b.hasNested = req.Exists() && !b.root.Get("contents").Exists()
		if b.hasNested {
			b.req = req
		}
	}
	id := extract(h, b, len(payload) > 0 && b.root.Exists())
	return bound(id)
}

func extract(h http.Header, b body, hasBody bool) string {
	// 1. Claude Code session header.
	if sid := header(h, "X-Claude-Code-Session-Id"); sid != "" {
		agent := header(h, "X-Claude-Code-Agent-Id")
		if agent == "" && hasBody {
			agent = bodyAgent(b)
		}
		if agent == "" && hasBody {
			_, agent = claudeMetadata(b)
		}
		if agent != "" && agent != "main" {
			return "claude:" + sid + ":agent:" + agent
		}
		return "claude:" + sid
	}
	// 2. Claude Code metadata.user_id.
	if hasBody {
		if sid, agent := claudeMetadata(b); sid != "" {
			if agent == "" {
				agent = header(h, "X-Claude-Code-Agent-Id")
			}
			if agent == "" {
				agent = bodyAgent(b)
			}
			if agent != "" && agent != "main" {
				return "claude:" + sid + ":agent:" + agent
			}
			return "claude:" + sid
		}
	}
	// 3. Codex / OpenAI session and thread headers.
	if id := codex(h, b, hasBody); id != "" {
		return id
	}
	// 4. Antigravity CLI.
	if sid := header(h, "X-Http-Session-Id"); sid != "" {
		return "agy:" + sid
	}
	// 5. Generic headers.
	for _, c := range []struct{ name, prefix string }{
		{"X-Session-ID", "header:"}, {"X-Session-Affinity", "affinity:"}, {"X-Slot-Session-Id", "slot:"},
	} {
		if sid := header(h, c.name); sid != "" {
			return c.prefix + sid
		}
	}
	for _, name := range []string{"X-Task-ID", "X-Task-Id", "X-Task_ID"} {
		if sid := header(h, name); sid != "" {
			return "task:" + sid
		}
	}
	for _, c := range []struct{ name, prefix string }{
		{"X-Conversation-Id", "conv:"}, {"X-Thread-Id", "thread:"}, {"X-Client-Request-Id", "clientreq:"},
	} {
		if sid := header(h, c.name); sid != "" {
			return c.prefix + sid
		}
	}
	if !hasBody {
		return ""
	}
	// 6. Payload.
	for _, p := range []string{"cachedContent", "cached_content"} {
		if v := b.get(p); v != "" {
			return "geminicache:" + v
		}
	}
	if v := b.first("thread_id", "threadId", "metadata.thread_id"); v != "" {
		return "thread:" + v
	}
	agent := bodyAgent(b)
	if agent == "" {
		agent = header(h, "X-Claude-Code-Agent-Id")
	}
	if agent == "" {
		agent = header(h, "x-agent-id")
	}
	if sid := b.first("session_id", "sessionId", "sessionID", "child_session_id", "childSessionId",
		"metadata.session_id", "metadata.sessionId", "metadata.sessionID", "metadata.child_session_id",
		"extra_body.session_id", "extra_body.sessionId", "extra_body.sessionID"); sid != "" {
		if agent != "" && agent != "main" {
			return "session:" + sid + ":agent:" + agent
		}
		return "session:" + sid
	}
	if tid := b.first("task_id", "taskId", "taskID", "action_id", "actionId", "actionID",
		"metadata.task_id", "metadata.taskId", "metadata.taskID", "metadata.action_id", "metadata.actionId", "metadata.actionID",
		"extra_body.task_id", "extra_body.taskId", "extra_body.taskID"); tid != "" {
		return "task:" + tid
	}
	conversation := b.root.Get("conversation")
	if !conversation.Exists() && b.hasNested {
		conversation = b.req.Get("conversation")
	}
	conversationID := ""
	if v := normalize(conversation.Get("id").String()); v != "" {
		conversationID = "conv:" + v
	} else if conversation.Type == gjson.String {
		if v := normalize(conversation.String()); v != "" {
			conversationID = "conv:" + v
		}
	}
	if pck := b.first("prompt_cache_key", "promptCacheKey"); pck != "" {
		return "pck:" + pck
	}
	if conversationID != "" {
		return conversationID
	}
	if uid := b.get("metadata.user_id"); uid != "" {
		return "user:" + uid
	}
	if cid := b.first("conversation_id", "conversationId", "chat_id", "chatId", "metadata.conversation_id", "extra_body.conversation_id"); cid != "" {
		return "conv:" + cid
	}
	return ""
}

func codex(h http.Header, b body, hasBody bool) string {
	sid := header(h, "Session-Id")
	if sid == "" {
		sid = header(h, "Session_id")
	}
	tid := header(h, "Thread-Id")
	if tid == "" {
		tid = header(h, "Thread_id")
	}
	var meta gjson.Result
	for k, v := range h {
		if strings.EqualFold(k, "X-Codex-Turn-Metadata") && len(v) > 0 {
			meta = gjson.Parse(strings.TrimSpace(v[0]))
			break
		}
	}
	if sid == "" && meta.Exists() {
		sid = normalize(meta.Get("session_id").String())
	}
	if tid == "" && meta.Exists() {
		tid = normalize(meta.Get("thread_id").String())
	}
	if tid == "" && sid != "" && hasBody {
		tid = b.first("thread_id", "threadId", "metadata.thread_id")
	}
	if sid == "" && tid == "" {
		return ""
	}
	parentThread := header(h, "x-codex-parent-thread-id")
	if parentThread == "" && meta.Exists() {
		parentThread = normalize(meta.Get("parent_thread_id").String())
	}
	forkedFrom := ""
	if meta.Exists() {
		forkedFrom = normalize(meta.Get("forked_from_thread_id").String())
		if forkedFrom == "" {
			forkedFrom = normalize(meta.Get("forked_from_id").String())
		}
	}
	if forkedFrom == "" && hasBody {
		forkedFrom = b.first("forked_from_thread_id", "forked_from_id", "metadata.forked_from_thread_id", "metadata.forked_from_id",
			"extra_body.forked_from_thread_id", "extra_body.forked_from_id")
	}
	agentName := ""
	if meta.Exists() {
		raw := meta.Get("agent_name").String()
		raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(raw, "/root/"), "/"))
		if raw = normalize(raw); raw != "" && raw != "root" && raw != "main" {
			agentName = raw
		}
	}
	sub := header(h, "X-Openai-Subagent")
	subagent := sub != "" && !strings.EqualFold(sub, "false") && sub != "0"
	if meta.Exists() && meta.Get("subagent_kind").String() == "thread_spawn" {
		subagent = true
	}
	if forkedFrom != "" {
		id := tid
		if id == "" {
			id = sid
		}
		if id == forkedFrom && sid != "" && sid != forkedFrom {
			id = sid
		}
		return "codex:" + id
	}
	if subagent || (tid != "" && sid != "" && tid != sid) || (parentThread != "" && parentThread != tid && parentThread != sid) {
		if agentName != "" && sid != "" {
			return "codex:" + sid + ":agent:" + agentName
		}
		if tid != "" {
			return "codex:" + tid
		}
		return "codex:" + sid
	}
	if sid != "" {
		return "codex:" + sid
	}
	return "codex:" + tid
}

// bound mirrors session.BoundSessionIdentity.
func bound(id string) string {
	if len(id) <= 256 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	hashHex := hex.EncodeToString(sum[:])
	prefix := id[:255-1-len(hashHex)]
	for !utf8.ValidString(prefix) && len(prefix) > 0 {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + "#" + hashHex
}
