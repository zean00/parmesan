package usage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/agent"
	usagedomain "github.com/sahal/parmesan/internal/domain/usage"
	"github.com/sahal/parmesan/internal/store"
)

type Context struct {
	OrgID       string
	AgentID     string
	CustomerID  string
	SessionID   string
	ExecutionID string
	ResponseID  string
	TraceID     string
}

type MeterRequest struct {
	Context   Context
	Metric    string
	Quantity  int64
	Resource  string
	Provider  string
	Model     string
	ToolID    string
	Status    string
	Error     string
	LatencyMS int64
	Estimated bool
	Metadata  map[string]any
	Now       time.Time
}

type QuotaError struct {
	Decision usagedomain.Decision
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("quota exceeded: %s/%s %s", e.Decision.ScopeKind, e.Decision.ScopeID, e.Decision.Metric)
}

type Service struct {
	repo         store.Repository
	defaultOrgID string
}

func New(repo store.Repository, defaultOrgID string) *Service {
	defaultOrgID = strings.TrimSpace(defaultOrgID)
	if defaultOrgID == "" {
		defaultOrgID = strings.TrimSpace(os.Getenv("PARMESAN_ORG_ID"))
	}
	if defaultOrgID == "" {
		defaultOrgID = strings.TrimSpace(os.Getenv("PARMESAN_OBSERVABILITY_ORG_ID"))
	}
	if defaultOrgID == "" {
		defaultOrgID = strings.TrimSpace(os.Getenv("DEFAULT_ORG_ID"))
	}
	return &Service{repo: repo, defaultOrgID: defaultOrgID}
}

func (s *Service) Meter(ctx context.Context, req MeterRequest) ([]usagedomain.Decision, error) {
	if req.Quantity < 0 {
		req.Quantity = 1
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	req.Context = s.resolveContext(ctx, req.Context)
	scopes := scopeIDs(req.Context)
	var decisions []usagedomain.Decision
	var unpolicyed []usagedomain.Decision
	var reservations []usagedomain.Reservation
	for _, scope := range scopes {
		policies, err := s.repo.ListUsageQuotaPolicies(ctx, usagedomain.PolicyQuery{
			ScopeKind: scope.kind,
			ScopeID:   scope.id,
			Metric:    req.Metric,
			Status:    usagedomain.PolicyActive,
		})
		if err != nil {
			return decisions, err
		}
		if len(policies) == 0 {
			unpolicyed = append(unpolicyed, usagedomain.Decision{
				Decision:  usagedomain.DecisionAllowed,
				Allowed:   true,
				ScopeKind: scope.kind,
				ScopeID:   scope.id,
				Metric:    req.Metric,
				Requested: req.Quantity,
			})
			continue
		}
		for _, policy := range policies {
			reservations = append(reservations, usagedomain.Reservation{
				Policy:   policy,
				ScopeID:  scope.id,
				Quantity: req.Quantity,
				Now:      now,
			})
		}
	}
	if len(scopes) == 0 {
		unpolicyed = append(unpolicyed, usagedomain.Decision{
			Decision:  usagedomain.DecisionAllowed,
			Allowed:   true,
			ScopeKind: firstNonEmptyScope(scopes).kind,
			ScopeID:   firstNonEmptyScope(scopes).id,
			Metric:    req.Metric,
			Requested: req.Quantity,
		})
	}
	if len(reservations) > 0 {
		var err error
		decisions, err = s.repo.CheckAndReserveUsageBatch(ctx, reservations)
		if err != nil {
			return decisions, err
		}
	}
	for _, decision := range append(append([]usagedomain.Decision(nil), decisions...), unpolicyed...) {
		s.appendEvent(ctx, req, decision, now)
	}
	for _, decision := range decisions {
		if !decision.Allowed {
			return decisions, &QuotaError{Decision: decision}
		}
	}
	return decisions, nil
}

func (s *Service) Record(ctx context.Context, req MeterRequest) {
	_, _ = s.Meter(ctx, req)
}

func (s *Service) resolveContext(ctx context.Context, in Context) Context {
	if strings.TrimSpace(in.OrgID) != "" {
		return in
	}
	if strings.TrimSpace(in.AgentID) != "" {
		profile, err := s.repo.GetAgentProfile(ctx, in.AgentID)
		if err == nil {
			in.OrgID = strings.TrimSpace(agentOrgID(profile))
		}
	}
	if strings.TrimSpace(in.OrgID) == "" {
		in.OrgID = s.defaultOrgID
	}
	return in
}

func agentOrgID(profile agent.Profile) string {
	for _, key := range []string{"org_id", "organization_id"} {
		if value := strings.TrimSpace(fmt.Sprint(profile.Metadata[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func (s *Service) appendEvent(ctx context.Context, req MeterRequest, decision usagedomain.Decision, now time.Time) {
	scopeKind := decision.ScopeKind
	scopeID := decision.ScopeID
	if strings.TrimSpace(scopeKind) == "" || strings.TrimSpace(scopeID) == "" {
		scope := firstNonEmptyScope(scopeIDs(req.Context))
		scopeKind = scope.kind
		scopeID = scope.id
	}
	if strings.TrimSpace(scopeKind) == "" || strings.TrimSpace(scopeID) == "" {
		return
	}
	event := usagedomain.UsageEvent{
		ID:          stableID("usage", decision.Policy.ID, decision.Decision, scopeKind, scopeID, req.Metric, req.Context.SessionID, req.Context.ExecutionID, fmt.Sprint(now.UnixNano())),
		PolicyID:    decision.Policy.ID,
		Decision:    firstNonEmpty(decision.Decision, usagedomain.DecisionAllowed),
		ScopeKind:   scopeKind,
		ScopeID:     scopeID,
		Metric:      req.Metric,
		Quantity:    req.Quantity,
		Window:      decision.Window,
		WindowStart: decision.WindowStart,
		WindowEnd:   decision.WindowEnd,
		UsedBefore:  decision.UsedBefore,
		UsedAfter:   decision.UsedAfter,
		Limit:       decision.Limit,
		Resource:    req.Resource,
		Provider:    req.Provider,
		Model:       req.Model,
		ToolID:      req.ToolID,
		SessionID:   req.Context.SessionID,
		ExecutionID: req.Context.ExecutionID,
		ResponseID:  req.Context.ResponseID,
		TraceID:     req.Context.TraceID,
		Estimated:   req.Estimated,
		Status:      req.Status,
		Error:       req.Error,
		LatencyMS:   req.LatencyMS,
		Metadata:    cloneMap(req.Metadata),
		OccurredAt:  now,
		RecordedAt:  now,
	}
	_ = s.repo.AppendUsageEvent(ctx, event)
}

func WindowBounds(window string, now time.Time) (time.Time, time.Time, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	switch strings.ToLower(strings.TrimSpace(window)) {
	case usagedomain.WindowMinute:
		start := now.Truncate(time.Minute)
		return start, start.Add(time.Minute), nil
	case usagedomain.WindowHour:
		start := now.Truncate(time.Hour)
		return start, start.Add(time.Hour), nil
	case usagedomain.WindowDay:
		y, m, d := now.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 0, 1), nil
	case usagedomain.WindowMonth:
		y, m, _ := now.Date()
		start := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0), nil
	default:
		return time.Time{}, time.Time{}, errors.New("invalid quota window")
	}
}

type scopeID struct {
	kind string
	id   string
}

func scopeIDs(ctx Context) []scopeID {
	var out []scopeID
	if strings.TrimSpace(ctx.CustomerID) != "" {
		out = append(out, scopeID{kind: usagedomain.ScopeCustomer, id: strings.TrimSpace(ctx.CustomerID)})
	}
	if strings.TrimSpace(ctx.AgentID) != "" {
		out = append(out, scopeID{kind: usagedomain.ScopeAgent, id: strings.TrimSpace(ctx.AgentID)})
	}
	if strings.TrimSpace(ctx.OrgID) != "" {
		out = append(out, scopeID{kind: usagedomain.ScopeOrganization, id: strings.TrimSpace(ctx.OrgID)})
	}
	return out
}

func firstNonEmptyScope(scopes []scopeID) scopeID {
	if len(scopes) == 0 {
		return scopeID{}
	}
	return scopes[0]
}

func stableID(prefix string, parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return prefix + "_" + hex.EncodeToString(sum[:8])
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return strings.TrimSpace(item)
		}
	}
	return ""
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
