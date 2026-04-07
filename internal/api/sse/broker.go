package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Envelope struct {
	EventID     string    `json:"event_id"`
	SessionID   string    `json:"session_id,omitempty"`
	TraceID     string    `json:"trace_id,omitempty"`
	ExecutionID string    `json:"execution_id,omitempty"`
	Type        string    `json:"type"`
	Payload     any       `json:"payload,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type Broker struct {
	mu   sync.RWMutex
	subs map[string]map[chan Envelope]struct{}
}

func NewBroker() *Broker {
	return &Broker{
		subs: map[string]map[chan Envelope]struct{}{},
	}
}

func (b *Broker) Publish(sessionID string, env Envelope) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs[sessionID] {
		select {
		case ch <- env:
		default:
		}
	}
}

func (b *Broker) Stream(w http.ResponseWriter, r *http.Request, sessionID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan Envelope, 16)
	cancel := b.Subscribe(sessionID, ch)
	defer cancel()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case env := <-ch:
			raw, err := json.Marshal(env)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\n", env.Type)
			fmt.Fprintf(w, "data: %s\n\n", raw)
			flusher.Flush()
		}
	}
}

func (b *Broker) Subscribe(sessionID string, ch chan Envelope) func() {
	b.subscribe(sessionID, ch)
	return func() {
		b.unsubscribe(sessionID, ch)
	}
}

func (b *Broker) subscribe(sessionID string, ch chan Envelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = map[chan Envelope]struct{}{}
	}
	b.subs[sessionID][ch] = struct{}{}
}

func (b *Broker) unsubscribe(sessionID string, ch chan Envelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if subs := b.subs[sessionID]; subs != nil {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(b.subs, sessionID)
		}
	}
	close(ch)
}
