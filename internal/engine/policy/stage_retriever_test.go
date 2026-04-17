package policyruntime

import (
	"testing"

	"github.com/sahal/parmesan/internal/domain/policy"
	knowledgeretriever "github.com/sahal/parmesan/internal/knowledge/retriever"
)

func TestRetrievalOutcomeFromResultsEvidenceAvailable(t *testing.T) {
	outcome := retrievalOutcomeFromResults([]knowledgeretriever.Result{{
		RetrieverID: "validated_corpus",
		Data:        "Pump-driven machines use an electric pump to provide brewing pressure.",
	}})
	if !outcome.Attempted || !outcome.GroundingRequired {
		t.Fatalf("outcome = %#v, want attempted grounded retrieval", outcome)
	}
	if !outcome.HasUsableEvidence || outcome.State != "evidence_available" {
		t.Fatalf("outcome = %#v, want evidence_available", outcome)
	}
}

func TestRetrievalOutcomeFromResultsInsufficient(t *testing.T) {
	outcome := retrievalOutcomeFromResults([]knowledgeretriever.Result{{
		RetrieverID: "validated_corpus",
		Error:       "snapshot unavailable",
	}})
	if outcome.HasUsableEvidence {
		t.Fatalf("outcome = %#v, want no usable evidence", outcome)
	}
	if outcome.State != "insufficient" {
		t.Fatalf("outcome = %#v, want insufficient", outcome)
	}
}

func TestRetrievalOutcomeFromResultsNoResults(t *testing.T) {
	outcome := retrievalOutcomeFromResults([]knowledgeretriever.Result{{
		RetrieverID: "validated_corpus",
	}})
	if outcome.HasUsableEvidence {
		t.Fatalf("outcome = %#v, want no usable evidence", outcome)
	}
	if outcome.State != "no_results" {
		t.Fatalf("outcome = %#v, want no_results", outcome)
	}
}

func TestRetrievalOutcomeFromResultsTransientGuidance(t *testing.T) {
	outcome := retrievalOutcomeFromResults([]knowledgeretriever.Result{{
		RetrieverID: "wiki",
		TransientGuidelines: []policy.Guideline{{
			ID:   "mention_refund",
			Then: "Mention the retrieved refund rule.",
		}},
	}})
	if !outcome.HasUsableEvidence {
		t.Fatalf("outcome = %#v, want usable guidance", outcome)
	}
	if outcome.GroundingRequired {
		t.Fatalf("outcome = %#v, want transient guidance to avoid grounded-miss flow", outcome)
	}
	if outcome.State != "guidance_available" {
		t.Fatalf("outcome = %#v, want guidance_available", outcome)
	}
}
