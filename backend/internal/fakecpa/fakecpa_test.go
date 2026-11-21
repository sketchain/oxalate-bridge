package fakecpa

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func post(t *testing.T, c *CPA, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.URL()+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func TestRateLimitHeadersAndDrift(t *testing.T) {
	c := New("t")
	defer c.Close()
	resp := post(t, c, `{"model":"claude-opus-4-8","stream":true}`)
	if resp.Header.Get("anthropic-ratelimit-unified-5h-utilization") != "0.1000" ||
		resp.Header.Get("anthropic-ratelimit-unified-status") != "allowed" || resp.Header.Get("anthropic-ratelimit-unified-7d-reset") == "" {
		t.Fatalf("headers %v", resp.Header)
	}
	// Without Drift the reported usage does not move.
	c.Set(func(c *CPA) {})
	if c.FiveUsed != 10 {
		t.Fatalf("usage moved without drift: %v", c.FiveUsed)
	}
	c.Set(func(c *CPA) { c.Drift = true })
	post(t, c, `{"model":"claude-opus-4-8"}`)
	c.Set(func(x *CPA) {
		if x.FiveUsed <= 10 || x.WeekUsed <= 20 {
			t.Errorf("drift did not add usage: %v %v", x.FiveUsed, x.WeekUsed)
		}
		x.FiveReset = time.Now().Add(-time.Minute)
	})
	post(t, c, `{"model":"claude-opus-4-8"}`)
	c.Set(func(x *CPA) {
		if !x.FiveReset.After(time.Now()) || x.FiveUsed > 5 {
			t.Errorf("window did not roll over: reset %v used %v", x.FiveReset, x.FiveUsed)
		}
	})
}
