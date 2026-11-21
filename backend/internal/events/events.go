// Package events fans Redis pub/sub messages out to this replica: cache
// invalidations, settings and routing reloads, and admin SSE events.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
)

// Event is one admin SSE event.
type Event struct {
	Type string          `json:"type"` // account_status | request_settled
	Data json.RawMessage `json:"data"`
}

// Hub subscribes to the bridge channels once per replica.
type Hub struct {
	St  *rstate.State
	Log *slog.Logger
	// Handlers; nil ones are skipped.
	OnInvalidate func(hashes []string)
	OnSettings   func()
	OnRouting    func()
	// OnResync runs after a reconnect, when messages may have been missed.
	OnResync func()

	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewHub returns a hub.
func NewHub(st *rstate.State, log *slog.Logger) *Hub {
	return &Hub{St: st, Log: log, subs: map[chan Event]struct{}{}}
}

// Publish broadcasts an admin event to every replica.
func (h *Hub) Publish(ctx context.Context, typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg, _ := json.Marshal(Event{Type: typ, Data: raw})
	if err := h.St.R.Publish(ctx, h.St.ChanEvents(), msg).Err(); err != nil {
		h.Log.Debug("publish event", "err", err)
	}
}

// Subscribe registers an SSE client.
func (h *Hub) Subscribe() (chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *Hub) fanout(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // slow client: drop
		}
	}
}

// Run listens until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	ps := h.St.R.Subscribe(ctx, h.St.ChanInvalidate(), h.St.ChanSettings(), h.St.ChanRouting(), h.St.ChanEvents())
	defer ps.Close()
	ch := ps.ChannelWithSubscriptions(redis.WithChannelHealthCheckInterval(10 * time.Second))
	subscribed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			switch msg := m.(type) {
			case *redis.Subscription:
				if msg.Kind == "subscribe" {
					subscribed++
					// Four channels on the first connection; any later
					// subscribe is a reconnect.
					if subscribed > 4 && h.OnResync != nil {
						h.OnResync()
					}
				}
			case *redis.Message:
				h.handle(msg)
			}
		}
	}
}

func (h *Hub) handle(msg *redis.Message) {
	switch msg.Channel {
	case h.St.ChanInvalidate():
		var inv struct {
			Hashes []string `json:"hashes"`
		}
		if json.Unmarshal([]byte(msg.Payload), &inv) == nil && h.OnInvalidate != nil {
			h.OnInvalidate(inv.Hashes)
		}
	case h.St.ChanSettings():
		if h.OnSettings != nil {
			h.OnSettings()
		}
	case h.St.ChanRouting():
		if h.OnRouting != nil {
			h.OnRouting()
		}
	case h.St.ChanEvents():
		var ev Event
		if json.Unmarshal([]byte(msg.Payload), &ev) == nil {
			h.fanout(ev)
		}
	}
}
