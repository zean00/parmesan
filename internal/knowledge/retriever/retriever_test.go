package retriever

import (
	"context"
	"strings"
	"testing"

	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/model"
)

func TestLexicalRetrieverReturnsCitedKnowledge(t *testing.T) {
	snapshot := knowledge.Snapshot{ID: "snap_1", ScopeKind: "agent", ScopeID: "agent_1"}
	result, err := (Lexical{
		ID:       "wiki",
		Snapshot: snapshot,
		Chunks: []knowledge.Chunk{{
			ID:   "chunk_1",
			Text: "Damaged orders can be refunded after inspection.",
			Citations: []knowledge.Citation{{
				SourceID: "src_1",
				Title:    "Returns",
			}},
		}},
		MaxResults: 1,
	}).Retrieve(context.Background(), Context{
		LatestCustomerText: "my order arrived damaged",
		KnowledgeSnapshot:  snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data, "Damaged orders") || result.ResultHash == "" || len(result.Citations) != 1 {
		t.Fatalf("result = %#v, want cited damaged-order snippet", result)
	}
}

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, []string) (model.EmbeddingResponse, error) {
	return model.EmbeddingResponse{Provider: "fake", Vectors: [][]float32{{1, 0}}}, nil
}

func TestHybridRetrieverUsesVectorsWhenAvailable(t *testing.T) {
	snapshot := knowledge.Snapshot{ID: "snap_1", ScopeKind: "agent", ScopeID: "agent_1"}
	result, err := (Hybrid{
		ID:       "wiki",
		Snapshot: snapshot,
		Chunks: []knowledge.Chunk{
			{ID: "chunk_a", Text: "Alpha policy", Vector: []float32{1, 0}},
			{ID: "chunk_b", Text: "Beta policy", Vector: []float32{0, 1}},
		},
		MaxResults: 1,
		Embedder:   fakeEmbedder{},
	}).Retrieve(context.Background(), Context{
		LatestCustomerText: "alpha",
		KnowledgeSnapshot:  snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Data != "Alpha policy" {
		t.Fatalf("result.Data = %q, want vector-ranked Alpha policy", result.Data)
	}
}
