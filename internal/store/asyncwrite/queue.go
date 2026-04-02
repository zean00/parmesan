package asyncwrite

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/delivery"
	"github.com/sahal/parmesan/internal/domain/execution"
	gatewaydomain "github.com/sahal/parmesan/internal/domain/gateway"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
	"github.com/sahal/parmesan/internal/store"
)

var ErrQueueFull = errors.New("async write queue is full")

type job struct {
	name string
	run  func() error
}

type Queue struct {
	repo    store.Repository
	ch      chan job
	wg      sync.WaitGroup
	closed  atomic.Bool
	pending atomic.Int64
}

func New(repo store.Repository, size int) *Queue {
	if size <= 0 {
		size = 256
	}
	return &Queue{
		repo: repo,
		ch:   make(chan job, size),
	}
}

func (q *Queue) Start(ctx context.Context, workers int) {
	if workers <= 0 {
		workers = 1
	}
	for range workers {
		q.wg.Add(1)
		go q.worker(ctx)
	}
}

func (q *Queue) Stop() {
	if q.closed.CompareAndSwap(false, true) {
		close(q.ch)
	}
	q.wg.Wait()
}

func (q *Queue) Stats() map[string]int64 {
	return map[string]int64{
		"pending":  q.pending.Load(),
		"capacity": int64(cap(q.ch)),
	}
}

func (q *Queue) SaveBundle(ctx context.Context, bundle policy.Bundle) error {
	return q.enqueue(ctx, "save_bundle", func(ctx context.Context) error {
		return q.repo.SaveBundle(ctx, bundle)
	})
}

func (q *Queue) AppendEvent(ctx context.Context, event session.Event) error {
	return q.enqueue(ctx, "append_event", func(ctx context.Context) error {
		return q.repo.AppendEvent(ctx, event)
	})
}

func (q *Queue) UpsertConversationBinding(ctx context.Context, binding gatewaydomain.ConversationBinding) error {
	return q.enqueue(ctx, "upsert_conversation_binding", func(ctx context.Context) error {
		return q.repo.UpsertConversationBinding(ctx, binding)
	})
}

func (q *Queue) CreateExecution(ctx context.Context, exec execution.TurnExecution, steps []execution.ExecutionStep) error {
	return q.enqueue(ctx, "create_execution", func(ctx context.Context) error {
		return q.repo.CreateExecution(ctx, exec, steps)
	})
}

func (q *Queue) SaveApprovalSession(ctx context.Context, session approval.Session) error {
	return q.enqueue(ctx, "save_approval_session", func(ctx context.Context) error {
		return q.repo.SaveApprovalSession(ctx, session)
	})
}

func (q *Queue) RegisterProvider(ctx context.Context, binding tool.ProviderBinding) error {
	return q.enqueue(ctx, "register_provider", func(ctx context.Context) error {
		return q.repo.RegisterProvider(ctx, binding)
	})
}

func (q *Queue) SaveProviderAuthBinding(ctx context.Context, binding tool.AuthBinding) error {
	return q.enqueue(ctx, "save_provider_auth_binding", func(ctx context.Context) error {
		return q.repo.SaveProviderAuthBinding(ctx, binding)
	})
}

func (q *Queue) SaveCatalogEntries(ctx context.Context, entries []tool.CatalogEntry) error {
	return q.enqueue(ctx, "save_catalog_entries", func(ctx context.Context) error {
		return q.repo.SaveCatalogEntries(ctx, entries)
	})
}

func (q *Queue) AppendAuditRecord(ctx context.Context, record audit.Record) error {
	return q.enqueue(ctx, "append_audit_record", func(ctx context.Context) error {
		return q.repo.AppendAuditRecord(ctx, record)
	})
}

func (q *Queue) SaveToolRun(ctx context.Context, run toolrun.Run) error {
	return q.enqueue(ctx, "save_tool_run", func(ctx context.Context) error {
		return q.repo.SaveToolRun(ctx, run)
	})
}

func (q *Queue) SaveDeliveryAttempt(ctx context.Context, attempt delivery.Attempt) error {
	return q.enqueue(ctx, "save_delivery_attempt", func(ctx context.Context) error {
		return q.repo.SaveDeliveryAttempt(ctx, attempt)
	})
}

func (q *Queue) CreateEvalRun(ctx context.Context, run replay.Run) error {
	return q.enqueue(ctx, "create_eval_run", func(ctx context.Context) error {
		return q.repo.CreateEvalRun(ctx, run)
	})
}

func (q *Queue) SaveProposal(ctx context.Context, proposal rollout.Proposal) error {
	return q.enqueue(ctx, "save_proposal", func(ctx context.Context) error {
		return q.repo.SaveProposal(ctx, proposal)
	})
}

func (q *Queue) SaveRollout(ctx context.Context, record rollout.Record) error {
	return q.enqueue(ctx, "save_rollout", func(ctx context.Context) error {
		return q.repo.SaveRollout(ctx, record)
	})
}

func (q *Queue) enqueue(ctx context.Context, name string, fn func(context.Context) error) error {
	if q.closed.Load() {
		return errors.New("async write queue is closed")
	}
	detached := context.WithoutCancel(ctx)
	job := job{name: name, run: func() error { return fn(detached) }}
	select {
	case q.ch <- job:
		q.pending.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("%w: %s", ErrQueueFull, name)
	}
}

func (q *Queue) worker(_ context.Context) {
	defer q.wg.Done()
	for job := range q.ch {
		if err := job.run(); err != nil {
			log.Printf("async write %s failed: %v", job.name, err)
		}
		q.pending.Add(-1)
	}
}
