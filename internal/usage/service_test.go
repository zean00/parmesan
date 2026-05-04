package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	usagedomain "github.com/sahal/parmesan/internal/domain/usage"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestMeterBlocksHardFixedWindowQuota(t *testing.T) {
	repo := memory.New()
	now := time.Date(2026, 5, 4, 10, 30, 0, 0, time.UTC)
	policy := usagedomain.QuotaPolicy{
		ID:          "quota_1",
		ScopeKind:   usagedomain.ScopeCustomer,
		ScopeID:     "cust_1",
		Metric:      usagedomain.MetricCustomerTurns,
		Window:      usagedomain.WindowMinute,
		Limit:       1,
		Enforcement: usagedomain.EnforcementBlock,
		Status:      usagedomain.PolicyActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := repo.SaveUsageQuotaPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	svc := New(repo, "org_1")
	req := MeterRequest{
		Context:  Context{CustomerID: "cust_1"},
		Metric:   usagedomain.MetricCustomerTurns,
		Quantity: 1,
		Now:      now,
	}
	if _, err := svc.Meter(context.Background(), req); err != nil {
		t.Fatalf("first Meter() error = %v", err)
	}
	_, err := svc.Meter(context.Background(), req)
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("second Meter() error = %v, want QuotaError", err)
	}
	if quotaErr.Decision.UsedBefore != 1 || quotaErr.Decision.UsedAfter != 2 || quotaErr.Decision.ResetAt != now.Truncate(time.Minute).Add(time.Minute) {
		t.Fatalf("decision = %#v, want used 1->2 and minute reset", quotaErr.Decision)
	}
	events, err := repo.ListUsageEvents(context.Background(), usagedomain.EventQuery{ScopeKind: usagedomain.ScopeCustomer, ScopeID: "cust_1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Decision != usagedomain.DecisionBlocked {
		t.Fatalf("events = %#v, want blocked ledger event plus first allowed", events)
	}
}

func TestMeterWarnModeAllowsOverLimit(t *testing.T) {
	repo := memory.New()
	now := time.Now().UTC()
	if err := repo.SaveUsageQuotaPolicy(context.Background(), usagedomain.QuotaPolicy{
		ID:          "quota_warn",
		ScopeKind:   usagedomain.ScopeAgent,
		ScopeID:     "agent_1",
		Metric:      usagedomain.MetricToolCalls,
		Window:      usagedomain.WindowHour,
		Limit:       1,
		Enforcement: usagedomain.EnforcementWarn,
		Status:      usagedomain.PolicyActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(repo, "org_1")
	req := MeterRequest{Context: Context{AgentID: "agent_1"}, Metric: usagedomain.MetricToolCalls, Quantity: 2, Now: now}
	decisions, err := svc.Meter(context.Background(), req)
	if err != nil {
		t.Fatalf("Meter() error = %v", err)
	}
	if len(decisions) != 1 || decisions[0].Decision != usagedomain.DecisionWarned || !decisions[0].Allowed {
		t.Fatalf("decisions = %#v, want allowed warning", decisions)
	}
}

func TestMeterDoesNotReserveEarlierBucketsWhenLaterPolicyBlocks(t *testing.T) {
	repo := memory.New()
	now := time.Date(2026, 5, 4, 10, 45, 0, 0, time.UTC)
	for _, policy := range []usagedomain.QuotaPolicy{
		{
			ID:          "quota_customer",
			ScopeKind:   usagedomain.ScopeCustomer,
			ScopeID:     "cust_1",
			Metric:      usagedomain.MetricCustomerTurns,
			Window:      usagedomain.WindowMinute,
			Limit:       10,
			Enforcement: usagedomain.EnforcementBlock,
			Status:      usagedomain.PolicyActive,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "quota_org",
			ScopeKind:   usagedomain.ScopeOrganization,
			ScopeID:     "org_1",
			Metric:      usagedomain.MetricCustomerTurns,
			Window:      usagedomain.WindowMinute,
			Limit:       1,
			Enforcement: usagedomain.EnforcementBlock,
			Status:      usagedomain.PolicyActive,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	} {
		if err := repo.SaveUsageQuotaPolicy(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(repo, "org_1")
	req := MeterRequest{Context: Context{CustomerID: "cust_1"}, Metric: usagedomain.MetricCustomerTurns, Quantity: 1, Now: now}
	if _, err := svc.Meter(context.Background(), req); err != nil {
		t.Fatalf("first Meter() error = %v", err)
	}
	var quotaErr *QuotaError
	if _, err := svc.Meter(context.Background(), req); !errors.As(err, &quotaErr) {
		t.Fatalf("second Meter() error = %v, want QuotaError", err)
	}
	buckets, err := repo.ListUsageBuckets(context.Background(), usagedomain.SummaryQuery{Metric: usagedomain.MetricCustomerTurns})
	if err != nil {
		t.Fatal(err)
	}
	quantities := map[string]int64{}
	for _, bucket := range buckets {
		quantities[bucket.PolicyID] = bucket.Quantity
	}
	if quantities["quota_customer"] != 1 || quantities["quota_org"] != 1 {
		t.Fatalf("bucket quantities = %#v, want blocked request to leave both at 1", quantities)
	}
}

func TestMeterWritesDistinctEventsForMatchingPoliciesAndUnpolicyedScopes(t *testing.T) {
	repo := memory.New()
	now := time.Date(2026, 5, 4, 11, 0, 0, 0, time.UTC)
	for _, policy := range []usagedomain.QuotaPolicy{
		{
			ID:          "quota_all_customers",
			ScopeKind:   usagedomain.ScopeCustomer,
			Metric:      usagedomain.MetricCustomerTurns,
			Window:      usagedomain.WindowMinute,
			Limit:       10,
			Enforcement: usagedomain.EnforcementBlock,
			Status:      usagedomain.PolicyActive,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "quota_specific_customer",
			ScopeKind:   usagedomain.ScopeCustomer,
			ScopeID:     "cust_1",
			Metric:      usagedomain.MetricCustomerTurns,
			Window:      usagedomain.WindowMinute,
			Limit:       10,
			Enforcement: usagedomain.EnforcementBlock,
			Status:      usagedomain.PolicyActive,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	} {
		if err := repo.SaveUsageQuotaPolicy(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(repo, "org_1")
	_, err := svc.Meter(context.Background(), MeterRequest{
		Context:  Context{CustomerID: "cust_1", AgentID: "agent_1"},
		Metric:   usagedomain.MetricCustomerTurns,
		Quantity: 1,
		Now:      now,
	})
	if err != nil {
		t.Fatalf("Meter() error = %v", err)
	}
	customerEvents, err := repo.ListUsageEvents(context.Background(), usagedomain.EventQuery{ScopeKind: usagedomain.ScopeCustomer, ScopeID: "cust_1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(customerEvents) != 2 {
		t.Fatalf("customer events = %#v, want one event per matching policy", customerEvents)
	}
	for _, query := range []usagedomain.EventQuery{
		{ScopeKind: usagedomain.ScopeAgent, ScopeID: "agent_1"},
		{ScopeKind: usagedomain.ScopeOrganization, ScopeID: "org_1"},
	} {
		events, err := repo.ListUsageEvents(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].PolicyID != "" {
			t.Fatalf("events for %#v = %#v, want unpolicyed ledger event", query, events)
		}
	}
}
