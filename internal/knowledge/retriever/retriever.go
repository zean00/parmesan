package retriever

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/model"
)

type Context struct {
	SessionID           string
	LatestCustomerText  string
	ConversationText    string
	MatchedGuidelineIDs []string
	ActiveJourneyID     string
	ActiveStateID       string
	DerivedSignals      []string
	KnowledgeSnapshot   knowledge.Snapshot
}

type Result struct {
	RetrieverID         string               `json:"retriever_id"`
	KnowledgeSnapshotID string               `json:"knowledge_snapshot_id,omitempty"`
	Data                string               `json:"data,omitempty"`
	Citations           []knowledge.Citation `json:"citations,omitempty"`
	TransientGuidelines []policy.Guideline   `json:"transient_guidelines,omitempty"`
	ResultHash          string               `json:"result_hash,omitempty"`
	Error               string               `json:"error,omitempty"`
}

type Interface interface {
	Retrieve(ctx context.Context, in Context) (Result, error)
}

type Embedder interface {
	Embed(context.Context, []string) (model.EmbeddingResponse, error)
}

type Searcher interface {
	SearchKnowledgeChunks(context.Context, knowledge.ChunkSearchQuery) ([]knowledge.Chunk, error)
}

type Lexical struct {
	ID         string
	Snapshot   knowledge.Snapshot
	Chunks     []knowledge.Chunk
	MaxResults int
}

type Hybrid struct {
	ID         string
	Snapshot   knowledge.Snapshot
	Chunks     []knowledge.Chunk
	MaxResults int
	Embedder   Embedder
	Searcher   Searcher
}

func (r Lexical) Retrieve(_ context.Context, in Context) (Result, error) {
	query := strings.Join([]string{in.LatestCustomerText, strings.Join(in.DerivedSignals, " ")}, " ")
	queryTokens := tokenSet(query)
	if len(queryTokens) == 0 {
		queryTokens = tokenSet(in.ConversationText)
	}
	type scored struct {
		score int
		chunk knowledge.Chunk
	}
	var scoredChunks []scored
	for _, chunk := range r.Chunks {
		score := overlapScore(queryTokens, tokenSet(chunk.Text))
		if score == 0 && strings.TrimSpace(query) != "" {
			continue
		}
		scoredChunks = append(scoredChunks, scored{score: score, chunk: chunk})
	}
	sort.SliceStable(scoredChunks, func(i, j int) bool {
		if scoredChunks[i].score == scoredChunks[j].score {
			return scoredChunks[i].chunk.ID < scoredChunks[j].chunk.ID
		}
		return scoredChunks[i].score > scoredChunks[j].score
	})
	limit := r.MaxResults
	if limit <= 0 || limit > 3 {
		limit = 3
	}
	if limit > len(scoredChunks) {
		limit = len(scoredChunks)
	}
	var parts []string
	var citations []knowledge.Citation
	for _, item := range scoredChunks[:limit] {
		parts = append(parts, item.chunk.Text)
		citations = append(citations, item.chunk.Citations...)
	}
	data := strings.Join(parts, "\n\n")
	return Result{
		RetrieverID:         r.ID,
		KnowledgeSnapshotID: r.Snapshot.ID,
		Data:                data,
		Citations:           citations,
		ResultHash:          hash(data),
	}, nil
}

func (r Hybrid) Retrieve(ctx context.Context, in Context) (Result, error) {
	if r.Embedder == nil || !hasVectors(r.Chunks) {
		return Lexical{ID: r.ID, Snapshot: r.Snapshot, Chunks: r.Chunks, MaxResults: r.MaxResults}.Retrieve(ctx, in)
	}
	query := strings.TrimSpace(strings.Join([]string{in.LatestCustomerText, strings.Join(in.DerivedSignals, " ")}, " "))
	if query == "" {
		query = strings.TrimSpace(in.ConversationText)
	}
	if query == "" {
		return Lexical{ID: r.ID, Snapshot: r.Snapshot, Chunks: r.Chunks, MaxResults: r.MaxResults}.Retrieve(ctx, in)
	}
	embedded, err := r.Embedder.Embed(ctx, []string{query})
	if err != nil || len(embedded.Vectors) == 0 {
		return Lexical{ID: r.ID, Snapshot: r.Snapshot, Chunks: r.Chunks, MaxResults: r.MaxResults}.Retrieve(ctx, in)
	}
	queryVector := embedded.Vectors[0]
	if r.Searcher != nil && strings.TrimSpace(r.Snapshot.ID) != "" {
		chunks, err := r.Searcher.SearchKnowledgeChunks(ctx, knowledge.ChunkSearchQuery{
			ScopeKind:  r.Snapshot.ScopeKind,
			ScopeID:    r.Snapshot.ScopeID,
			SnapshotID: r.Snapshot.ID,
			Vector:     queryVector,
			Limit:      boundedLimit(r.MaxResults),
		})
		if err == nil && len(chunks) > 0 {
			return resultFromChunks(r.ID, r.Snapshot.ID, chunks, boundedLimit(r.MaxResults)), nil
		}
	}
	type scored struct {
		score float64
		chunk knowledge.Chunk
	}
	var scoredChunks []scored
	for _, chunk := range r.Chunks {
		if len(chunk.Vector) == 0 {
			continue
		}
		scoredChunks = append(scoredChunks, scored{score: cosine(queryVector, chunk.Vector), chunk: chunk})
	}
	sort.SliceStable(scoredChunks, func(i, j int) bool {
		if scoredChunks[i].score == scoredChunks[j].score {
			return scoredChunks[i].chunk.ID < scoredChunks[j].chunk.ID
		}
		return scoredChunks[i].score > scoredChunks[j].score
	})
	limit := boundedLimit(r.MaxResults)
	if limit > len(scoredChunks) {
		limit = len(scoredChunks)
	}
	chunks := make([]knowledge.Chunk, 0, limit)
	for _, item := range scoredChunks[:limit] {
		chunks = append(chunks, item.chunk)
	}
	return resultFromChunks(r.ID, r.Snapshot.ID, chunks, limit), nil
}

func tokenSet(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return r < 'a' || r > 'z'
	}) {
		if len(token) > 2 {
			out[token] = struct{}{}
		}
	}
	return out
}

func overlapScore(a, b map[string]struct{}) int {
	score := 0
	for token := range a {
		if _, ok := b[token]; ok {
			score++
		}
	}
	return score
}

func hash(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

func hasVectors(chunks []knowledge.Chunk) bool {
	for _, chunk := range chunks {
		if len(chunk.Vector) > 0 {
			return true
		}
	}
	return false
}

func boundedLimit(limit int) int {
	if limit <= 0 || limit > 3 {
		return 3
	}
	return limit
}

func resultFromChunks(retrieverID string, snapshotID string, chunks []knowledge.Chunk, limit int) Result {
	if limit <= 0 || limit > len(chunks) {
		limit = len(chunks)
	}
	var parts []string
	var citations []knowledge.Citation
	for _, chunk := range chunks[:limit] {
		parts = append(parts, chunk.Text)
		citations = append(citations, chunk.Citations...)
	}
	data := strings.Join(parts, "\n\n")
	return Result{
		RetrieverID:         retrieverID,
		KnowledgeSnapshotID: snapshotID,
		Data:                data,
		Citations:           citations,
		ResultHash:          hash(data),
	}
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
