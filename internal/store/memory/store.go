package memory

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sahal/parmesan/internal/controlgraph"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/delivery"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/feedback"
	gatewaydomain "github.com/sahal/parmesan/internal/domain/gateway"
	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/media"
	"github.com/sahal/parmesan/internal/domain/operator"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/replay"
	responsedomain "github.com/sahal/parmesan/internal/domain/response"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
)

type Store struct {
	mu                       sync.RWMutex
	bundles                  []policy.Bundle
	policyArtifacts          []policy.GraphArtifact
	policyEdges              []policy.GraphEdge
	policySnapshots          []policy.Snapshot
	agentProfiles            []agent.Profile
	operators                []operator.Operator
	operatorTokens           []operator.APIToken
	customerPreferences      []customer.Preference
	customerPreferenceEvents []customer.PreferenceEvent
	feedbackRecords          []feedback.Record
	sessions                 []session.Session
	sessionWatches           []session.Watch
	events                   map[string][]session.Event
	bindings                 []gatewaydomain.ConversationBinding
	execs                    []execution.TurnExecution
	steps                    map[string][]execution.ExecutionStep
	journeys                 map[string][]journey.Instance
	providers                []tool.ProviderBinding
	authBindings             []tool.AuthBinding
	catalog                  []tool.CatalogEntry
	audit                    []audit.Record
	responses                []responsedomain.Response
	responseTraceSpans       []responsedomain.TraceSpan
	approvals                []approval.Session
	toolRuns                 []toolrun.Run
	deliveries               []delivery.Attempt
	evalRuns                 []replay.Run
	proposals                []rollout.Proposal
	rollouts                 []rollout.Record
	knowledgeSources         []knowledge.Source
	knowledgeSyncJobs        []knowledge.SyncJob
	knowledgePages           []knowledge.Page
	knowledgeChunks          []knowledge.Chunk
	knowledgeSnapshots       []knowledge.Snapshot
	knowledgeUpdateProposals []knowledge.UpdateProposal
	knowledgeLintFindings    []knowledge.LintFinding
	mediaAssets              []media.Asset
	derivedSignals           []media.DerivedSignal
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
	replaced := false
	for i, item := range s.bundles {
		if item.ID == bundle.ID {
			s.bundles[i] = bundle
			replaced = true
			break
		}
	}
	if !replaced {
		s.bundles = append(s.bundles, bundle)
	}
	artifacts, edges, snapshot := policy.MaterializeGraph(bundle)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	s.savePolicySnapshotLocked(snapshot)
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

func (s *Store) SavePolicyArtifacts(_ context.Context, items []policy.GraphArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savePolicyArtifactsLocked(items)
	return nil
}

func (s *Store) ListPolicyArtifacts(_ context.Context, query policy.ArtifactQuery) ([]policy.GraphArtifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []policy.GraphArtifact
	for i := len(s.policyArtifacts) - 1; i >= 0; i-- {
		item := s.policyArtifacts[i]
		if query.BundleID != "" && item.BundleID != query.BundleID {
			continue
		}
		if query.Kind != "" && item.Kind != query.Kind {
			continue
		}
		out = append(out, item)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return out, nil
}

func (s *Store) SavePolicyEdges(_ context.Context, items []policy.GraphEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savePolicyEdgesLocked(items)
	return nil
}

func (s *Store) ListPolicyEdges(_ context.Context, query policy.EdgeQuery) ([]policy.GraphEdge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []policy.GraphEdge
	for i := len(s.policyEdges) - 1; i >= 0; i-- {
		item := s.policyEdges[i]
		if query.BundleID != "" && item.BundleID != query.BundleID {
			continue
		}
		if query.SnapshotID != "" && item.SnapshotID != query.SnapshotID {
			continue
		}
		if query.SourceID != "" && item.SourceID != query.SourceID {
			continue
		}
		if query.TargetID != "" && item.TargetID != query.TargetID {
			continue
		}
		out = append(out, item)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return out, nil
}

func (s *Store) SavePolicySnapshot(_ context.Context, snapshot policy.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savePolicySnapshotLocked(snapshot)
	return nil
}

func (s *Store) GetPolicySnapshot(_ context.Context, snapshotID string) (policy.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.policySnapshots {
		if item.ID == snapshotID {
			return item, nil
		}
	}
	return policy.Snapshot{}, errors.New("policy snapshot not found")
}

func (s *Store) ListPolicySnapshots(_ context.Context, query policy.SnapshotQuery) ([]policy.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []policy.Snapshot
	for i := len(s.policySnapshots) - 1; i >= 0; i-- {
		item := s.policySnapshots[i]
		if query.BundleID != "" && item.BundleID != query.BundleID {
			continue
		}
		out = append(out, item)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return out, nil
}

func (s *Store) savePolicyArtifactsLocked(items []policy.GraphArtifact) {
	for _, item := range items {
		replaced := false
		for i, existing := range s.policyArtifacts {
			if existing.ID == item.ID {
				s.policyArtifacts[i] = item
				replaced = true
				break
			}
		}
		if !replaced {
			s.policyArtifacts = append(s.policyArtifacts, item)
		}
	}
}

func (s *Store) savePolicyEdgesLocked(items []policy.GraphEdge) {
	for _, item := range items {
		replaced := false
		for i, existing := range s.policyEdges {
			if existing.ID == item.ID {
				s.policyEdges[i] = item
				replaced = true
				break
			}
		}
		if !replaced {
			s.policyEdges = append(s.policyEdges, item)
		}
	}
}

func (s *Store) savePolicySnapshotLocked(snapshot policy.Snapshot) {
	for i, item := range s.policySnapshots {
		if item.ID == snapshot.ID {
			s.policySnapshots[i] = snapshot
			return
		}
	}
	s.policySnapshots = append(s.policySnapshots, snapshot)
}

func (s *Store) SaveAgentProfile(_ context.Context, profile agent.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.agentProfiles {
		if item.ID == profile.ID {
			s.agentProfiles[i] = profile
			return nil
		}
	}
	s.agentProfiles = append(s.agentProfiles, profile)
	return nil
}

func (s *Store) GetAgentProfile(_ context.Context, profileID string) (agent.Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.agentProfiles {
		if item.ID == profileID {
			return item, nil
		}
	}
	return agent.Profile{}, errors.New("agent profile not found")
}

func (s *Store) ListAgentProfiles(_ context.Context) ([]agent.Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]agent.Profile(nil), s.agentProfiles...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) SaveOperator(_ context.Context, item operator.Operator) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.operators {
		if existing.ID == item.ID {
			s.operators[i] = item
			return nil
		}
	}
	s.operators = append(s.operators, item)
	return nil
}

func (s *Store) GetOperator(_ context.Context, operatorID string) (operator.Operator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.operators {
		if item.ID == operatorID {
			return item, nil
		}
	}
	return operator.Operator{}, errors.New("operator not found")
}

func (s *Store) ListOperators(_ context.Context) ([]operator.Operator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]operator.Operator(nil), s.operators...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) SaveOperatorAPIToken(_ context.Context, token operator.APIToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.operatorTokens {
		if existing.ID == token.ID {
			s.operatorTokens[i] = token
			return nil
		}
	}
	s.operatorTokens = append(s.operatorTokens, token)
	return nil
}

func (s *Store) GetOperatorAPITokenByHash(_ context.Context, tokenHash string) (operator.APIToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.operatorTokens {
		if item.TokenHash == tokenHash {
			return item, nil
		}
	}
	return operator.APIToken{}, errors.New("operator api token not found")
}

func (s *Store) ListOperatorAPITokens(_ context.Context, operatorID string) ([]operator.APIToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []operator.APIToken
	for _, item := range s.operatorTokens {
		if operatorID != "" && item.OperatorID != operatorID {
			continue
		}
		item.Plaintext = ""
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) SaveCustomerPreference(_ context.Context, pref customer.Preference, event customer.PreferenceEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.customerPreferences {
		if item.AgentID == pref.AgentID && item.CustomerID == pref.CustomerID && item.Key == pref.Key {
			pref.CreatedAt = item.CreatedAt
			s.customerPreferences[i] = pref
			if event.ID != "" {
				s.customerPreferenceEvents = append(s.customerPreferenceEvents, event)
			}
			artifacts, edges := controlgraph.CustomerPreferenceRecord(pref)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.customerPreferences = append(s.customerPreferences, pref)
	if event.ID != "" {
		s.customerPreferenceEvents = append(s.customerPreferenceEvents, event)
	}
	artifacts, edges := controlgraph.CustomerPreferenceRecord(pref)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	return nil
}

func (s *Store) GetCustomerPreference(_ context.Context, agentID string, customerID string, key string) (customer.Preference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.customerPreferences {
		if item.AgentID == agentID && item.CustomerID == customerID && item.Key == key {
			return item, nil
		}
	}
	return customer.Preference{}, errors.New("customer preference not found")
}

func (s *Store) ListCustomerPreferences(_ context.Context, query customer.PreferenceQuery) ([]customer.Preference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	var out []customer.Preference
	for _, item := range s.customerPreferences {
		if query.AgentID != "" && item.AgentID != query.AgentID {
			continue
		}
		if query.CustomerID != "" && item.CustomerID != query.CustomerID {
			continue
		}
		if query.Status != "" && item.Status != query.Status {
			continue
		}
		if query.Key != "" && item.Key != query.Key {
			continue
		}
		if query.Source != "" && item.Source != query.Source {
			continue
		}
		if query.MinConfidence > 0 && item.Confidence < query.MinConfidence {
			continue
		}
		if !query.IncludeExpired && item.ExpiresAt != nil && !item.ExpiresAt.After(now) {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (s *Store) AppendCustomerPreferenceEvent(_ context.Context, event customer.PreferenceEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customerPreferenceEvents = append(s.customerPreferenceEvents, event)
	artifacts, edges := controlgraph.CustomerPreferenceEvent(event)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	return nil
}

func (s *Store) ListCustomerPreferenceEvents(_ context.Context, query customer.PreferenceQuery) ([]customer.PreferenceEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []customer.PreferenceEvent
	for _, item := range s.customerPreferenceEvents {
		if query.AgentID != "" && item.AgentID != query.AgentID {
			continue
		}
		if query.CustomerID != "" && item.CustomerID != query.CustomerID {
			continue
		}
		if query.Key != "" && item.Key != query.Key {
			continue
		}
		if query.Source != "" && item.Source != query.Source {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (s *Store) SaveFeedbackRecord(_ context.Context, record feedback.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.feedbackRecords {
		if item.ID == record.ID {
			s.feedbackRecords[i] = record
			artifacts, edges := controlgraph.FeedbackRecord(record)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.feedbackRecords = append(s.feedbackRecords, record)
	artifacts, edges := controlgraph.FeedbackRecord(record)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	return nil
}

func (s *Store) GetFeedbackRecord(_ context.Context, feedbackID string) (feedback.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.feedbackRecords {
		if item.ID == feedbackID {
			return item, nil
		}
	}
	return feedback.Record{}, errors.New("feedback record not found")
}

func (s *Store) ListFeedbackRecords(_ context.Context, query feedback.Query) ([]feedback.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []feedback.Record
	for _, item := range s.feedbackRecords {
		if query.SessionID != "" && item.SessionID != query.SessionID {
			continue
		}
		if query.OperatorID != "" && item.OperatorID != query.OperatorID {
			continue
		}
		if query.Category != "" && item.Category != query.Category {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (s *Store) CreateSession(_ context.Context, sess session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.Status == "" {
		sess.Status = session.StatusActive
	}
	if sess.LastActivityAt.IsZero() {
		sess.LastActivityAt = sess.CreatedAt
	}
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

func (s *Store) UpdateSession(_ context.Context, updated session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sess := range s.sessions {
		if sess.ID == updated.ID {
			s.sessions[i] = updated
			return nil
		}
	}
	return errors.New("session not found")
}

func (s *Store) ListSessions(_ context.Context) ([]session.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]session.Session(nil), s.sessions...)
	return out, nil
}

func (s *Store) SaveSessionWatch(_ context.Context, watch session.Watch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.sessionWatches {
		if item.ID == watch.ID {
			s.sessionWatches[i] = watch
			return nil
		}
	}
	s.sessionWatches = append(s.sessionWatches, watch)
	return nil
}

func (s *Store) GetSessionWatch(_ context.Context, watchID string) (session.Watch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.sessionWatches {
		if item.ID == watchID {
			return item, nil
		}
	}
	return session.Watch{}, errors.New("session watch not found")
}

func (s *Store) ListSessionWatches(_ context.Context, query session.WatchQuery) ([]session.Watch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []session.Watch
	for _, item := range s.sessionWatches {
		if query.SessionID != "" && item.SessionID != query.SessionID {
			continue
		}
		if query.Status != "" && string(item.Status) != query.Status {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) ListRunnableSessionWatches(_ context.Context, now time.Time) ([]session.Watch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []session.Watch
	for _, item := range s.sessionWatches {
		if item.Status != session.WatchStatusActive {
			continue
		}
		if !item.NextRunAt.IsZero() && item.NextRunAt.After(now) {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextRunAt.Before(out[j].NextRunAt) })
	return out, nil
}

func (s *Store) AppendEvent(_ context.Context, event session.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Offset == 0 {
		event.Offset = time.Now().UTC().UnixNano()
	}
	s.events[event.SessionID] = append(s.events[event.SessionID], event)
	return nil
}

func (s *Store) ReadEvent(_ context.Context, sessionID string, eventID string) (session.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, event := range s.events[sessionID] {
		if event.ID == eventID {
			return event, nil
		}
	}
	return session.Event{}, errors.New("event not found")
}

func (s *Store) UpdateEvent(_ context.Context, updated session.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.events[updated.SessionID]
	for i, event := range items {
		if event.ID == updated.ID {
			items[i] = updated
			s.events[updated.SessionID] = items
			return nil
		}
	}
	return errors.New("event not found")
}

func (s *Store) ListEvents(_ context.Context, sessionID string) ([]session.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]session.Event(nil), s.events[sessionID]...)
	return out, nil
}

func (s *Store) ListEventsFiltered(_ context.Context, query session.EventQuery) ([]session.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []session.Event
	kindSet := map[string]struct{}{}
	for _, kind := range query.Kinds {
		kindSet[kind] = struct{}{}
	}
	for _, event := range s.events[query.SessionID] {
		if query.Source != "" && event.Source != query.Source {
			continue
		}
		if query.TraceID != "" && event.TraceID != query.TraceID {
			continue
		}
		if query.MinOffset > 0 && event.Offset < query.MinOffset {
			continue
		}
		if query.ExcludeDeleted && event.Deleted {
			continue
		}
		if len(kindSet) > 0 {
			if _, ok := kindSet[event.Kind]; !ok {
				continue
			}
		}
		out = append(out, event)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return append([]session.Event(nil), out...), nil
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

func (s *Store) CreateOrCoalesceExecution(_ context.Context, exec execution.TurnExecution, steps []execution.ExecutionStep, triggerEventID string, coalesceUntil time.Time) (execution.TurnExecution, []execution.ExecutionStep, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.execs) - 1; i >= 0; i-- {
		existing := s.execs[i]
		if existing.SessionID != exec.SessionID || existing.BlockedReason != "" {
			continue
		}
		if existing.Status != execution.StatusPending && existing.Status != execution.StatusWaiting {
			continue
		}
		if !safeToCoalesceSteps(s.steps[existing.ID]) {
			continue
		}
		if hasAssistantEventForExecution(s.events[existing.SessionID], existing.ID) {
			continue
		}
		existing.TriggerEventIDs = appendUniqueExecutionID(existing.TriggerEventIDs, existing.TriggerEventID)
		existing.TriggerEventIDs = appendUniqueExecutionID(existing.TriggerEventIDs, triggerEventID)
		existing.LeaseExpiresAt = coalesceUntil
		existing.Status = execution.StatusWaiting
		existing.LeaseOwner = ""
		existing.UpdatedAt = exec.UpdatedAt
		updateFirstPendingStepForCoalesce(s.steps[existing.ID], coalesceUntil, exec.UpdatedAt)
		s.execs[i] = existing
		return existing, append([]execution.ExecutionStep(nil), s.steps[existing.ID]...), true, nil
	}
	if len(exec.TriggerEventIDs) == 0 {
		exec.TriggerEventIDs = []string{triggerEventID}
	}
	s.execs = append(s.execs, exec)
	s.steps[exec.ID] = append([]execution.ExecutionStep(nil), steps...)
	return exec, append([]execution.ExecutionStep(nil), steps...), false, nil
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
		if exec.Status == execution.StatusWaiting {
			if exec.LeaseExpiresAt.IsZero() || exec.LeaseExpiresAt.Before(now) {
				out = append(out, exec)
			}
			continue
		}
		if exec.Status == execution.StatusRunning || exec.Status == execution.StatusPending {
			if exec.LeaseExpiresAt.IsZero() || exec.LeaseExpiresAt.Before(now) || exec.LeaseOwner == "" {
				out = append(out, exec)
			}
		}
	}
	return out, nil
}

func safeToCoalesceSteps(steps []execution.ExecutionStep) bool {
	for _, step := range steps {
		if step.Name != "compose_response" {
			continue
		}
		switch step.Status {
		case execution.StatusRunning, execution.StatusSucceeded, execution.StatusBlocked, execution.StatusFailed, execution.StatusAbandoned:
			return false
		}
	}
	return true
}

func updateFirstPendingStepForCoalesce(steps []execution.ExecutionStep, coalesceUntil time.Time, updatedAt time.Time) {
	for i := range steps {
		if steps[i].Status != execution.StatusPending && steps[i].Status != execution.StatusWaiting {
			continue
		}
		steps[i].Status = execution.StatusWaiting
		steps[i].NextAttemptAt = coalesceUntil
		steps[i].LeaseOwner = ""
		steps[i].LeaseExpiresAt = coalesceUntil
		steps[i].UpdatedAt = updatedAt
		return
	}
}

func hasAssistantEventForExecution(events []session.Event, executionID string) bool {
	for _, event := range events {
		if event.ExecutionID == executionID && event.Source == "ai_agent" {
			return true
		}
	}
	return false
}

func appendUniqueExecutionID(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
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

func (s *Store) SaveResponse(_ context.Context, record responsedomain.Response) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.responses {
		if existing.ID == record.ID {
			s.responses[i] = record
			return nil
		}
	}
	s.responses = append(s.responses, record)
	return nil
}

func (s *Store) GetResponse(_ context.Context, responseID string) (responsedomain.Response, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, record := range s.responses {
		if record.ID == responseID {
			return record, nil
		}
	}
	return responsedomain.Response{}, errors.New("response not found")
}

func (s *Store) ListResponses(_ context.Context, query responsedomain.Query) ([]responsedomain.Response, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []responsedomain.Response
	for _, record := range s.responses {
		if query.SessionID != "" && record.SessionID != query.SessionID {
			continue
		}
		if query.ExecutionID != "" && record.ExecutionID != query.ExecutionID {
			continue
		}
		if query.Status != "" && string(record.Status) != query.Status {
			continue
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (s *Store) SaveResponseTraceSpan(_ context.Context, span responsedomain.TraceSpan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.responseTraceSpans {
		if existing.ID == span.ID {
			s.responseTraceSpans[i] = span
			return nil
		}
	}
	s.responseTraceSpans = append(s.responseTraceSpans, span)
	return nil
}

func (s *Store) ListResponseTraceSpans(_ context.Context, query responsedomain.TraceSpanQuery) ([]responsedomain.TraceSpan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []responsedomain.TraceSpan
	for _, span := range s.responseTraceSpans {
		if query.ResponseID != "" && span.ResponseID != query.ResponseID {
			continue
		}
		if query.SessionID != "" && span.SessionID != query.SessionID {
			continue
		}
		if query.ExecutionID != "" && span.ExecutionID != query.ExecutionID {
			continue
		}
		if query.TraceID != "" && span.TraceID != query.TraceID {
			continue
		}
		out = append(out, span)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
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
			artifacts, edges := controlgraph.RolloutProposal(proposal)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.proposals = append(s.proposals, proposal)
	artifacts, edges := controlgraph.RolloutProposal(proposal)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
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
			artifacts, edges := controlgraph.RolloutRecord(record)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.rollouts = append(s.rollouts, record)
	artifacts, edges := controlgraph.RolloutRecord(record)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
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

func (s *Store) SaveKnowledgeSource(_ context.Context, source knowledge.Source) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.knowledgeSources {
		if item.ID == source.ID {
			s.knowledgeSources[i] = source
			artifacts, edges := controlgraph.KnowledgeSource(source)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.knowledgeSources = append(s.knowledgeSources, source)
	artifacts, edges := controlgraph.KnowledgeSource(source)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	return nil
}

func (s *Store) GetKnowledgeSource(_ context.Context, sourceID string) (knowledge.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.knowledgeSources {
		if item.ID == sourceID {
			return item, nil
		}
	}
	return knowledge.Source{}, errors.New("knowledge source not found")
}

func (s *Store) ListKnowledgeSources(_ context.Context, scopeKind string, scopeID string) ([]knowledge.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []knowledge.Source
	for _, item := range s.knowledgeSources {
		if scopeKind != "" && item.ScopeKind != scopeKind {
			continue
		}
		if scopeID != "" && item.ScopeID != scopeID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Store) SaveKnowledgeSyncJob(_ context.Context, job knowledge.SyncJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.knowledgeSyncJobs {
		if item.ID == job.ID {
			s.knowledgeSyncJobs[i] = job
			artifacts, edges := controlgraph.KnowledgeSyncJob(job)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.knowledgeSyncJobs = append(s.knowledgeSyncJobs, job)
	artifacts, edges := controlgraph.KnowledgeSyncJob(job)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	return nil
}

func (s *Store) GetKnowledgeSyncJob(_ context.Context, jobID string) (knowledge.SyncJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.knowledgeSyncJobs {
		if item.ID == jobID {
			return item, nil
		}
	}
	return knowledge.SyncJob{}, errors.New("knowledge sync job not found")
}

func (s *Store) ListKnowledgeSyncJobs(_ context.Context, query knowledge.SyncJobQuery) ([]knowledge.SyncJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []knowledge.SyncJob
	for _, item := range s.knowledgeSyncJobs {
		if query.SourceID != "" && item.SourceID != query.SourceID {
			continue
		}
		if query.Status != "" && item.Status != query.Status {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (s *Store) ListRunnableKnowledgeSyncJobs(_ context.Context) ([]knowledge.SyncJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []knowledge.SyncJob
	for _, item := range s.knowledgeSyncJobs {
		if item.Status == "queued" || item.Status == "running" {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) SaveKnowledgePage(_ context.Context, page knowledge.Page, chunks []knowledge.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for i, item := range s.knowledgePages {
		if item.ID == page.ID {
			s.knowledgePages[i] = page
			replaced = true
			break
		}
	}
	if !replaced {
		s.knowledgePages = append(s.knowledgePages, page)
	}
	filtered := s.knowledgeChunks[:0]
	for _, item := range s.knowledgeChunks {
		if item.PageID != page.ID {
			filtered = append(filtered, item)
		}
	}
	s.knowledgeChunks = append(filtered, chunks...)
	return nil
}

func (s *Store) ListKnowledgePages(_ context.Context, query knowledge.PageQuery) ([]knowledge.Page, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pageIDs := map[string]struct{}{}
	if query.SnapshotID != "" {
		for _, snap := range s.knowledgeSnapshots {
			if snap.ID == query.SnapshotID {
				for _, id := range snap.PageIDs {
					pageIDs[id] = struct{}{}
				}
				break
			}
		}
	}
	var out []knowledge.Page
	for _, item := range s.knowledgePages {
		if query.ScopeKind != "" && item.ScopeKind != query.ScopeKind {
			continue
		}
		if query.ScopeID != "" && item.ScopeID != query.ScopeID {
			continue
		}
		if query.SnapshotID != "" {
			if _, ok := pageIDs[item.ID]; !ok {
				continue
			}
		}
		out = append(out, item)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return out, nil
}

func (s *Store) ListKnowledgeChunks(_ context.Context, query knowledge.ChunkQuery) ([]knowledge.Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	chunkIDs := map[string]struct{}{}
	if query.SnapshotID != "" {
		for _, snap := range s.knowledgeSnapshots {
			if snap.ID == query.SnapshotID {
				for _, id := range snap.ChunkIDs {
					chunkIDs[id] = struct{}{}
				}
				break
			}
		}
	}
	var out []knowledge.Chunk
	for _, item := range s.knowledgeChunks {
		if query.ScopeKind != "" && item.ScopeKind != query.ScopeKind {
			continue
		}
		if query.ScopeID != "" && item.ScopeID != query.ScopeID {
			continue
		}
		if query.SnapshotID != "" {
			if _, ok := chunkIDs[item.ID]; !ok {
				continue
			}
		}
		out = append(out, item)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return out, nil
}

func (s *Store) SearchKnowledgeChunks(_ context.Context, query knowledge.ChunkSearchQuery) ([]knowledge.Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	chunkIDs := map[string]struct{}{}
	if query.SnapshotID != "" {
		for _, snap := range s.knowledgeSnapshots {
			if snap.ID == query.SnapshotID {
				for _, id := range snap.ChunkIDs {
					chunkIDs[id] = struct{}{}
				}
				break
			}
		}
	}
	var chunks []knowledge.Chunk
	for _, item := range s.knowledgeChunks {
		if query.ScopeKind != "" && item.ScopeKind != query.ScopeKind {
			continue
		}
		if query.ScopeID != "" && item.ScopeID != query.ScopeID {
			continue
		}
		if query.SnapshotID != "" {
			if _, ok := chunkIDs[item.ID]; !ok {
				continue
			}
		}
		chunks = append(chunks, item)
	}
	type scored struct {
		score float64
		chunk knowledge.Chunk
	}
	var scoredChunks []scored
	for _, chunk := range chunks {
		if len(query.Vector) == 0 || len(chunk.Vector) == 0 {
			scoredChunks = append(scoredChunks, scored{score: 0, chunk: chunk})
			continue
		}
		scoredChunks = append(scoredChunks, scored{score: cosine(query.Vector, chunk.Vector), chunk: chunk})
	}
	sort.SliceStable(scoredChunks, func(i, j int) bool {
		if scoredChunks[i].score == scoredChunks[j].score {
			return scoredChunks[i].chunk.ID < scoredChunks[j].chunk.ID
		}
		return scoredChunks[i].score > scoredChunks[j].score
	})
	limit := query.Limit
	if limit <= 0 || limit > len(scoredChunks) {
		limit = len(scoredChunks)
	}
	out := make([]knowledge.Chunk, 0, limit)
	for _, item := range scoredChunks[:limit] {
		out = append(out, item.chunk)
	}
	return out, nil
}

func cosine(a, b []float32) float64 {
	size := len(a)
	if len(b) < size {
		size = len(b)
	}
	if size == 0 {
		return 0
	}
	var dot float64
	var normA float64
	var normB float64
	for i := 0; i < size; i++ {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrt(normA) * sqrt(normB))
}

func sqrt(v float64) float64 {
	if v == 0 {
		return 0
	}
	z := v
	for i := 0; i < 8; i++ {
		z -= (z*z - v) / (2 * z)
	}
	return z
}

func (s *Store) SaveKnowledgeSnapshot(_ context.Context, snapshot knowledge.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.knowledgeSnapshots {
		if item.ID == snapshot.ID {
			s.knowledgeSnapshots[i] = snapshot
			artifacts, edges := controlgraph.KnowledgeSnapshot(snapshot)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.knowledgeSnapshots = append(s.knowledgeSnapshots, snapshot)
	artifacts, edges := controlgraph.KnowledgeSnapshot(snapshot)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	return nil
}

func (s *Store) GetKnowledgeSnapshot(_ context.Context, snapshotID string) (knowledge.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.knowledgeSnapshots {
		if item.ID == snapshotID {
			return item, nil
		}
	}
	return knowledge.Snapshot{}, errors.New("knowledge snapshot not found")
}

func (s *Store) ListKnowledgeSnapshots(_ context.Context, query knowledge.SnapshotQuery) ([]knowledge.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []knowledge.Snapshot
	for i := len(s.knowledgeSnapshots) - 1; i >= 0; i-- {
		item := s.knowledgeSnapshots[i]
		if query.ScopeKind != "" && item.ScopeKind != query.ScopeKind {
			continue
		}
		if query.ScopeID != "" && item.ScopeID != query.ScopeID {
			continue
		}
		out = append(out, item)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return out, nil
}

func (s *Store) SaveKnowledgeUpdateProposal(_ context.Context, proposal knowledge.UpdateProposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.knowledgeUpdateProposals {
		if item.ID == proposal.ID {
			s.knowledgeUpdateProposals[i] = proposal
			artifacts, edges := controlgraph.KnowledgeUpdateProposal(proposal)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.knowledgeUpdateProposals = append(s.knowledgeUpdateProposals, proposal)
	artifacts, edges := controlgraph.KnowledgeUpdateProposal(proposal)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	return nil
}

func (s *Store) ListKnowledgeUpdateProposals(_ context.Context, scopeKind string, scopeID string) ([]knowledge.UpdateProposal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []knowledge.UpdateProposal
	for _, item := range s.knowledgeUpdateProposals {
		if scopeKind != "" && item.ScopeKind != scopeKind {
			continue
		}
		if scopeID != "" && item.ScopeID != scopeID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Store) GetKnowledgeUpdateProposal(_ context.Context, proposalID string) (knowledge.UpdateProposal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.knowledgeUpdateProposals {
		if item.ID == proposalID {
			return item, nil
		}
	}
	return knowledge.UpdateProposal{}, errors.New("knowledge update proposal not found")
}

func (s *Store) SaveKnowledgeLintFinding(_ context.Context, finding knowledge.LintFinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.knowledgeLintFindings {
		if item.ID == finding.ID {
			s.knowledgeLintFindings[i] = finding
			artifacts, edges := controlgraph.KnowledgeLintFinding(finding)
			s.savePolicyArtifactsLocked(artifacts)
			s.savePolicyEdgesLocked(edges)
			return nil
		}
	}
	s.knowledgeLintFindings = append(s.knowledgeLintFindings, finding)
	artifacts, edges := controlgraph.KnowledgeLintFinding(finding)
	s.savePolicyArtifactsLocked(artifacts)
	s.savePolicyEdgesLocked(edges)
	return nil
}

func (s *Store) ListKnowledgeLintFindings(_ context.Context, query knowledge.LintQuery) ([]knowledge.LintFinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []knowledge.LintFinding
	for _, item := range s.knowledgeLintFindings {
		if query.ScopeKind != "" && item.ScopeKind != query.ScopeKind {
			continue
		}
		if query.ScopeID != "" && item.ScopeID != query.ScopeID {
			continue
		}
		if query.ProposalID != "" && item.ProposalID != query.ProposalID {
			continue
		}
		if query.PageID != "" && item.PageID != query.PageID {
			continue
		}
		if query.Kind != "" && item.Kind != query.Kind {
			continue
		}
		if query.Severity != "" && item.Severity != query.Severity {
			continue
		}
		if query.Status != "" && item.Status != query.Status {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (s *Store) SaveMediaAsset(_ context.Context, asset media.Asset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.mediaAssets {
		if item.ID == asset.ID {
			s.mediaAssets[i] = asset
			return nil
		}
	}
	s.mediaAssets = append(s.mediaAssets, asset)
	return nil
}

func (s *Store) ListMediaAssets(_ context.Context, sessionID string) ([]media.Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []media.Asset
	for _, item := range s.mediaAssets {
		if sessionID == "" || item.SessionID == sessionID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Store) SaveDerivedSignal(_ context.Context, signal media.DerivedSignal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.derivedSignals {
		if item.ID == signal.ID {
			s.derivedSignals[i] = signal
			return nil
		}
	}
	s.derivedSignals = append(s.derivedSignals, signal)
	return nil
}

func (s *Store) ListDerivedSignals(_ context.Context, sessionID string) ([]media.DerivedSignal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []media.DerivedSignal
	for _, item := range s.derivedSignals {
		if sessionID == "" || item.SessionID == sessionID {
			out = append(out, item)
		}
	}
	return out, nil
}
