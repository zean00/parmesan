package store

import (
	"context"
	"time"

	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/delivery"
	"github.com/sahal/parmesan/internal/domain/execution"
	gatewaydomain "github.com/sahal/parmesan/internal/domain/gateway"
	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
)

type Repository interface {
	SaveBundle(ctx context.Context, bundle policy.Bundle) error
	ListBundles(ctx context.Context) ([]policy.Bundle, error)
	CreateSession(ctx context.Context, sess session.Session) error
	GetSession(ctx context.Context, sessionID string) (session.Session, error)
	ListSessions(ctx context.Context) ([]session.Session, error)
	AppendEvent(ctx context.Context, event session.Event) error
	ListEvents(ctx context.Context, sessionID string) ([]session.Event, error)
	UpsertConversationBinding(ctx context.Context, binding gatewaydomain.ConversationBinding) error
	GetConversationBinding(ctx context.Context, channel string, externalConversationID string) (gatewaydomain.ConversationBinding, error)
	ListConversationBindings(ctx context.Context) ([]gatewaydomain.ConversationBinding, error)
	CreateExecution(ctx context.Context, exec execution.TurnExecution, steps []execution.ExecutionStep) error
	ListExecutions(ctx context.Context) ([]execution.TurnExecution, error)
	GetExecution(ctx context.Context, executionID string) (execution.TurnExecution, []execution.ExecutionStep, error)
	UpdateExecution(ctx context.Context, exec execution.TurnExecution) error
	UpdateExecutionStep(ctx context.Context, step execution.ExecutionStep) error
	ListRunnableExecutions(ctx context.Context, now time.Time) ([]execution.TurnExecution, error)
	UpsertJourneyInstance(ctx context.Context, instance journey.Instance) error
	ListJourneyInstances(ctx context.Context, sessionID string) ([]journey.Instance, error)
	RegisterProvider(ctx context.Context, binding tool.ProviderBinding) error
	GetProvider(ctx context.Context, providerID string) (tool.ProviderBinding, error)
	ListProviders(ctx context.Context) ([]tool.ProviderBinding, error)
	SaveProviderAuthBinding(ctx context.Context, binding tool.AuthBinding) error
	GetProviderAuthBinding(ctx context.Context, providerID string) (tool.AuthBinding, error)
	SaveCatalogEntries(ctx context.Context, entries []tool.CatalogEntry) error
	ListCatalogEntries(ctx context.Context) ([]tool.CatalogEntry, error)
	AppendAuditRecord(ctx context.Context, record audit.Record) error
	ListAuditRecords(ctx context.Context) ([]audit.Record, error)
	SaveApprovalSession(ctx context.Context, session approval.Session) error
	GetApprovalSession(ctx context.Context, approvalID string) (approval.Session, error)
	ListApprovalSessions(ctx context.Context, sessionID string) ([]approval.Session, error)
	SaveToolRun(ctx context.Context, run toolrun.Run) error
	ListToolRuns(ctx context.Context, executionID string) ([]toolrun.Run, error)
	SaveDeliveryAttempt(ctx context.Context, attempt delivery.Attempt) error
	ListDeliveryAttempts(ctx context.Context, executionID string) ([]delivery.Attempt, error)
	CreateEvalRun(ctx context.Context, run replay.Run) error
	UpdateEvalRun(ctx context.Context, run replay.Run) error
	GetEvalRun(ctx context.Context, runID string) (replay.Run, error)
	ListEvalRuns(ctx context.Context) ([]replay.Run, error)
	ListRunnableEvalRuns(ctx context.Context, now time.Time) ([]replay.Run, error)
	SaveProposal(ctx context.Context, proposal rollout.Proposal) error
	GetProposal(ctx context.Context, proposalID string) (rollout.Proposal, error)
	ListProposals(ctx context.Context) ([]rollout.Proposal, error)
	SaveRollout(ctx context.Context, record rollout.Record) error
	GetRollout(ctx context.Context, rolloutID string) (rollout.Record, error)
	ListRollouts(ctx context.Context) ([]rollout.Record, error)
}
