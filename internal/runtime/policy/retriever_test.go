package policyruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/session"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
)

func TestResolveWithOptionsRunsKnowledgeRetriever(t *testing.T) {
	bundle := policy.Bundle{
		ID:      "bundle",
		Version: "v1",
		Guidelines: []policy.Guideline{{
			ID:   "damaged_return",
			When: "damaged order",
			Then: "Explain the return policy.",
		}},
		Retrievers: []policy.RetrieverBinding{{
			ID:         "wiki",
			Kind:       "knowledge",
			Scope:      "agent",
			MaxResults: 1,
		}},
	}
	snapshot := knowledge.Snapshot{ID: "snap_1", ScopeKind: "bundle", ScopeID: "bundle"}
	view, err := ResolveWithOptions(context.Background(), []session.Event{{
		ID:        "evt",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		Content:   []session.ContentPart{{Type: "text", Text: "my order is damaged"}},
	}}, []policy.Bundle{bundle}, nil, nil, ResolveOptions{
		KnowledgeSnapshot: &snapshot,
		KnowledgeChunks: []knowledge.Chunk{{
			ID:        "chunk",
			ScopeKind: "bundle",
			ScopeID:   "bundle",
			Text:      "Damaged orders are eligible for refunds.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.RetrieverStage.KnowledgeSnapshotID != "snap_1" || len(view.RetrieverStage.Results) != 1 {
		t.Fatalf("retriever stage = %#v, want one result from snapshot", view.RetrieverStage)
	}
	if !strings.Contains(view.RetrieverStage.Results[0].Data, "Damaged orders") {
		t.Fatalf("retrieved data = %q, want wiki chunk", view.RetrieverStage.Results[0].Data)
	}
}

type fakeTransientRetriever struct{}

func (fakeTransientRetriever) Retrieve(context.Context, knowledgeretriever.Context) (knowledgeretriever.Result, error) {
	return knowledgeretriever.Result{
		RetrieverID: "wiki",
		TransientGuidelines: []policy.Guideline{{
			ID:   "mention_refund",
			When: "damaged order",
			Then: "Mention the retrieved refund rule.",
		}},
	}, nil
}

func TestResolveWithOptionsPrefixesTransientRetrieverGuidelines(t *testing.T) {
	bundle := policy.Bundle{
		ID:      "bundle",
		Version: "v1",
		Retrievers: []policy.RetrieverBinding{{
			ID:    "wiki",
			Kind:  "knowledge",
			Scope: "agent",
		}},
	}
	view, err := ResolveWithOptions(context.Background(), []session.Event{{
		ID:        "evt",
		SessionID: "sess",
		Source:    "customer",
		Kind:      "message",
		Content:   []session.ContentPart{{Type: "text", Text: "my order is damaged"}},
	}}, []policy.Bundle{bundle}, nil, nil, ResolveOptions{
		RetrieverRegistry: RetrieverMap{"wiki": fakeTransientRetriever{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RetrieverStage.TransientGuidelines) != 1 {
		t.Fatalf("transient guidelines = %#v, want one", view.RetrieverStage.TransientGuidelines)
	}
	if got := view.RetrieverStage.TransientGuidelines[0].ID; got != "retriever:wiki:mention_refund" {
		t.Fatalf("transient guideline id = %q, want prefixed id", got)
	}
}
