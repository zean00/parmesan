package sessionsvc

import (
	"context"
	"time"

	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/store"
)

type Listener struct {
	repo     store.Repository
	interval time.Duration
}

func NewListener(repo store.Repository) *Listener {
	return &Listener{repo: repo, interval: 250 * time.Millisecond}
}

func (l *Listener) WaitForMoreEvents(ctx context.Context, query session.EventQuery) (bool, error) {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		events, err := l.repo.ListEventsFiltered(ctx, query)
		if err != nil {
			return false, err
		}
		if len(events) > 0 {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *Listener) WaitForEventCompletion(ctx context.Context, sessionID, eventID string) (bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		event, err := l.repo.ReadEvent(ctx, sessionID, eventID)
		if err != nil {
			return false, err
		}
		chunks, ok := event.Data["chunks"].([]any)
		if !ok || len(chunks) == 0 {
			return true, nil
		}
		if chunks[len(chunks)-1] == nil {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *Listener) WaitForNewStreamingChunks(ctx context.Context, sessionID, eventID string, lastKnownChunkCount int) (bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		event, err := l.repo.ReadEvent(ctx, sessionID, eventID)
		if err != nil {
			return false, err
		}
		chunks, ok := event.Data["chunks"].([]any)
		if !ok {
			return true, nil
		}
		if len(chunks) > lastKnownChunkCount || (len(chunks) > 0 && chunks[len(chunks)-1] == nil) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}
