package memory

import (
	"context"
	"errors"
	"sync"
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

type Store struct {
	mu           sync.RWMutex
	bundles      []policy.Bundle
	sessions     []session.Session
	events       map[string][]session.Event
	bindings     []gatewaydomain.ConversationBinding
	execs        []execution.TurnExecution
	steps        map[string][]execution.ExecutionStep
	journeys     map[string][]journey.Instance
	providers    []tool.ProviderBinding
	authBindings []tool.AuthBinding
	catalog      []tool.CatalogEntry
	audit        []audit.Record
	approvals    []approval.Session
	toolRuns     []toolrun.Run
	deliveries   []delivery.Attempt
	evalRuns     []replay.Run
	proposals    []rollout.Proposal
	rollouts     []rollout.Record
}

func New() *Store {
	return &Store{
		events:   make(map[string][]session.Event),
		steps:    make(map[string][]execution.ExecutionStep),
		journeys: make(map[string][]journey.Instance),
	}
}

func (s *Store) SaveBundle(_ context.Context, bundle policy.Bundle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundles = append(s.bundles, bundle)
	return nil
}

func (s *Store) ListBundles(_ context.Context) ([]policy.Bundle, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]policy.Bundle, 0, len(s.bundles))
	for i := len(s.bundles) - 1; i >= 0; i-- {
		out = append(out, s.bundles[i])
	}
	return out, nil
}

func (s *Store) CreateSession(_ context.Context, sess session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, sess)
	return nil
}

func (s *Store) GetSession(_ context.Context, sessionID string) (session.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		if sess.ID == sessionID {
			return sess, nil
		}
	}
	return session.Session{}, errors.New("session not found")
}

func (s *Store) ListSessions(_ context.Context) ([]session.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]session.Session(nil), s.sessions...)
	return out, nil
}

func (s *Store) AppendEvent(_ context.Context, event session.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[event.SessionID] = append(s.events[event.SessionID], event)
	return nil
}

func (s *Store) ListEvents(_ context.Context, sessionID string) ([]session.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]session.Event(nil), s.events[sessionID]...)
	return out, nil
}

func (s *Store) UpsertConversationBinding(_ context.Context, binding gatewaydomain.ConversationBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.bindings {
		if existing.ID == binding.ID || (existing.Channel == binding.Channel && existing.ExternalConversationID == binding.ExternalConversationID) {
			s.bindings[i] = binding
			return nil
		}
	}
	s.bindings = append(s.bindings, binding)
	return nil
}

func (s *Store) GetConversationBinding(_ context.Context, channel string, externalConversationID string) (gatewaydomain.ConversationBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, binding := range s.bindings {
		if binding.Channel == channel && binding.ExternalConversationID == externalConversationID {
			return binding, nil
		}
	}
	return gatewaydomain.ConversationBinding{}, errors.New("conversation binding not found")
}

func (s *Store) ListConversationBindings(_ context.Context) ([]gatewaydomain.ConversationBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]gatewaydomain.ConversationBinding(nil), s.bindings...)
	return out, nil
}

func (s *Store) CreateExecution(_ context.Context, exec execution.TurnExecution, steps []execution.ExecutionStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs = append(s.execs, exec)
	s.steps[exec.ID] = append([]execution.ExecutionStep(nil), steps...)
	return nil
}

func (s *Store) UpdateExecution(_ context.Context, updated execution.TurnExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, exec := range s.execs {
		if exec.ID == updated.ID {
			s.execs[i] = updated
			return nil
		}
	}
	return errors.New("execution not found")
}

func (s *Store) UpdateExecutionStep(_ context.Context, updated execution.ExecutionStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	steps := s.steps[updated.ExecutionID]
	for i, step := range steps {
		if step.ID == updated.ID {
			steps[i] = updated
			s.steps[updated.ExecutionID] = steps
			return nil
		}
	}
	return errors.New("step not found")
}

func (s *Store) ListExecutions(_ context.Context) ([]execution.TurnExecution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]execution.TurnExecution(nil), s.execs...)
	return out, nil
}

func (s *Store) GetExecution(_ context.Context, executionID string) (execution.TurnExecution, []execution.ExecutionStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, exec := range s.execs {
		if exec.ID == executionID {
			return exec, append([]execution.ExecutionStep(nil), s.steps[executionID]...), nil
		}
	}
	return execution.TurnExecution{}, nil, errors.New("execution not found")
}

func (s *Store) ListRunnableExecutions(_ context.Context, now time.Time) ([]execution.TurnExecution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []execution.TurnExecution
	for _, exec := range s.execs {
		if exec.Status == execution.StatusRunning || exec.Status == execution.StatusPending {
			if exec.LeaseExpiresAt.IsZero() || exec.LeaseExpiresAt.Before(now) || exec.LeaseOwner == "" {
				out = append(out, exec)
			}
		}
	}
	return out, nil
}

func (s *Store) UpsertJourneyInstance(_ context.Context, instance journey.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.journeys[instance.SessionID]
	for i, item := range items {
		if item.ID == instance.ID {
			items[i] = instance
			s.journeys[instance.SessionID] = items
			return nil
		}
	}
	s.journeys[instance.SessionID] = append(items, instance)
	return nil
}

func (s *Store) ListJourneyInstances(_ context.Context, sessionID string) ([]journey.Instance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]journey.Instance(nil), s.journeys[sessionID]...)
	return out, nil
}

func (s *Store) RegisterProvider(_ context.Context, binding tool.ProviderBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.providers {
		if existing.ID == binding.ID {
			s.providers[i] = binding
			return nil
		}
	}
	s.providers = append(s.providers, binding)
	return nil
}

func (s *Store) GetProvider(_ context.Context, providerID string) (tool.ProviderBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, binding := range s.providers {
		if binding.ID == providerID {
			return binding, nil
		}
	}
	return tool.ProviderBinding{}, errors.New("provider not found")
}

func (s *Store) ListProviders(_ context.Context) ([]tool.ProviderBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]tool.ProviderBinding(nil), s.providers...)
	return out, nil
}

func (s *Store) SaveProviderAuthBinding(_ context.Context, binding tool.AuthBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.authBindings {
		if item.ProviderID == binding.ProviderID {
			s.authBindings[i] = binding
			return nil
		}
	}
	s.authBindings = append(s.authBindings, binding)
	return nil
}

func (s *Store) GetProviderAuthBinding(_ context.Context, providerID string) (tool.AuthBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.authBindings {
		if item.ProviderID == providerID {
			return item, nil
		}
	}
	return tool.AuthBinding{}, errors.New("provider auth binding not found")
}

func (s *Store) SaveCatalogEntries(_ context.Context, entries []tool.CatalogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	providerID := entries[0].ProviderID
	filtered := s.catalog[:0]
	for _, entry := range s.catalog {
		if entry.ProviderID != providerID {
			filtered = append(filtered, entry)
		}
	}
	s.catalog = append(filtered, entries...)
	return nil
}

func (s *Store) ListCatalogEntries(_ context.Context) ([]tool.CatalogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]tool.CatalogEntry(nil), s.catalog...)
	return out, nil
}

func (s *Store) AppendAuditRecord(_ context.Context, record audit.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, record)
	return nil
}

func (s *Store) ListAuditRecords(_ context.Context) ([]audit.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]audit.Record(nil), s.audit...)
	return out, nil
}

func (s *Store) SaveApprovalSession(_ context.Context, session approval.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.approvals {
		if existing.ID == session.ID {
			s.approvals[i] = session
			return nil
		}
	}
	s.approvals = append(s.approvals, session)
	return nil
}

func (s *Store) GetApprovalSession(_ context.Context, approvalID string) (approval.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.approvals {
		if item.ID == approvalID {
			return item, nil
		}
	}
	return approval.Session{}, errors.New("approval session not found")
}

func (s *Store) ListApprovalSessions(_ context.Context, sessionID string) ([]approval.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []approval.Session
	for _, item := range s.approvals {
		if item.SessionID == sessionID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Store) SaveToolRun(_ context.Context, run toolrun.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.toolRuns {
		if existing.ID == run.ID {
			s.toolRuns[i] = run
			return nil
		}
	}
	s.toolRuns = append(s.toolRuns, run)
	return nil
}

func (s *Store) ListToolRuns(_ context.Context, executionID string) ([]toolrun.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []toolrun.Run
	for _, run := range s.toolRuns {
		if run.ExecutionID == executionID {
			out = append(out, run)
		}
	}
	return out, nil
}

func (s *Store) SaveDeliveryAttempt(_ context.Context, attempt delivery.Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.deliveries {
		if existing.ID == attempt.ID {
			s.deliveries[i] = attempt
			return nil
		}
	}
	s.deliveries = append(s.deliveries, attempt)
	return nil
}

func (s *Store) ListDeliveryAttempts(_ context.Context, executionID string) ([]delivery.Attempt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []delivery.Attempt
	for _, attempt := range s.deliveries {
		if attempt.ExecutionID == executionID {
			out = append(out, attempt)
		}
	}
	return out, nil
}

func (s *Store) CreateEvalRun(_ context.Context, run replay.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evalRuns = append(s.evalRuns, run)
	return nil
}

func (s *Store) UpdateEvalRun(_ context.Context, run replay.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.evalRuns {
		if item.ID == run.ID {
			s.evalRuns[i] = run
			return nil
		}
	}
	return errors.New("eval run not found")
}

func (s *Store) GetEvalRun(_ context.Context, runID string) (replay.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.evalRuns {
		if item.ID == runID {
			return item, nil
		}
	}
	return replay.Run{}, errors.New("eval run not found")
}

func (s *Store) ListEvalRuns(_ context.Context) ([]replay.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]replay.Run(nil), s.evalRuns...)
	return out, nil
}

func (s *Store) ListRunnableEvalRuns(_ context.Context, _ time.Time) ([]replay.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []replay.Run
	for _, run := range s.evalRuns {
		if run.Status == replay.StatusPending || run.Status == replay.StatusRunning {
			out = append(out, run)
		}
	}
	return out, nil
}

func (s *Store) SaveProposal(_ context.Context, proposal rollout.Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.proposals {
		if item.ID == proposal.ID {
			s.proposals[i] = proposal
			return nil
		}
	}
	s.proposals = append(s.proposals, proposal)
	return nil
}

func (s *Store) GetProposal(_ context.Context, proposalID string) (rollout.Proposal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.proposals {
		if item.ID == proposalID {
			return item, nil
		}
	}
	return rollout.Proposal{}, errors.New("proposal not found")
}

func (s *Store) ListProposals(_ context.Context) ([]rollout.Proposal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]rollout.Proposal(nil), s.proposals...), nil
}

func (s *Store) SaveRollout(_ context.Context, record rollout.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.rollouts {
		if item.ID == record.ID {
			s.rollouts[i] = record
			return nil
		}
	}
	s.rollouts = append(s.rollouts, record)
	return nil
}

func (s *Store) GetRollout(_ context.Context, rolloutID string) (rollout.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.rollouts {
		if item.ID == rolloutID {
			return item, nil
		}
	}
	return rollout.Record{}, errors.New("rollout not found")
}

func (s *Store) ListRollouts(_ context.Context) ([]rollout.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]rollout.Record(nil), s.rollouts...), nil
}
