package asyncwrite

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/delivery"
	"github.com/sahal/parmesan/internal/domain/execution"
	gatewaydomain "github.com/sahal/parmesan/internal/domain/gateway"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/replay"
	responsedomain "github.com/sahal/parmesan/internal/domain/response"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
	"github.com/sahal/parmesan/internal/observability"
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
	failed  atomic.Int64
	failMu  sync.Mutex
	lastJob string
	lastAt  time.Time
	lastOK  time.Time
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

func (q *Queue) Stats() map[string]any {
	q.failMu.Lock()
	defer q.failMu.Unlock()
	healthy := q.lastAt.IsZero() || (!q.lastOK.IsZero() && q.lastOK.After(q.lastAt))
	stats := map[string]any{
		"pending":      q.pending.Load(),
		"capacity":     int64(cap(q.ch)),
		"failed":       q.failed.Load(),
		"healthy":      healthy,
		"queued_state": "queued_not_yet_persisted",
	}
	if q.lastJob != "" {
		stats["last_failed_job"] = q.lastJob
	}
	if !q.lastAt.IsZero() {
		stats["last_failed_at"] = q.lastAt
	}
	if !q.lastOK.IsZero() {
		stats["last_succeeded_at"] = q.lastOK
	}
	return stats
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

func (q *Queue) SaveResponse(ctx context.Context, record responsedomain.Response) error {
	return q.enqueue(ctx, "save_response", func(ctx context.Context) error {
		return q.repo.SaveResponse(ctx, record)
	})
}

func (q *Queue) SaveResponseTraceSpan(ctx context.Context, span responsedomain.TraceSpan) error {
	return q.enqueue(ctx, "save_response_trace_span", func(ctx context.Context) error {
		return q.repo.SaveResponseTraceSpan(ctx, span)
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
			q.recordFailure(job.name)
			log.Printf("async write %s failed: %v", job.name, err)
		} else {
			q.recordSuccess()
		}
		q.pending.Add(-1)
	}
}

func (q *Queue) recordFailure(jobName string) {
	q.failed.Add(1)
	q.failMu.Lock()
	q.lastJob = jobName
	q.lastAt = time.Now().UTC()
	q.failMu.Unlock()
	observability.Current().RecordEvent("asyncwrite", jobName, "failed")
}

func (q *Queue) recordSuccess() {
	q.failMu.Lock()
	q.lastOK = time.Now().UTC()
	q.failMu.Unlock()
}
