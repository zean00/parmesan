package compiler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/store/memory"
)

func TestCompileFolderCreatesSnapshotWithCitedPages(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "returns.md"), []byte("# Returns\n\nDamaged orders can be refunded."), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := memory.New()
	source := knowledge.Source{ID: "src_1", ScopeKind: "agent", ScopeID: "agent_1", Kind: "folder", URI: root, Status: "registered"}
	if err := repo.SaveKnowledgeSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	snapshot, err := New(repo).CompileFolder(context.Background(), Input{
		ScopeKind: "agent",
		ScopeID:   "agent_1",
		SourceID:  source.ID,
		Root:      root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID == "" || len(snapshot.PageIDs) != 1 || len(snapshot.ChunkIDs) == 0 {
		t.Fatalf("snapshot = %#v, want one page and at least one chunk", snapshot)
	}
	pages, err := repo.ListKnowledgePages(context.Background(), knowledge.PageQuery{SnapshotID: snapshot.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Title != "Returns" || len(pages[0].Citations) != 1 {
		t.Fatalf("pages = %#v, want cited Returns page", pages)
	}
}

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, []string) (model.EmbeddingResponse, error) {
	return model.EmbeddingResponse{Provider: "fake", Vectors: [][]float32{{1, 0}, {0, 1}}}, nil
}

func TestCompileFolderStoresChunkVectorsWhenEmbedderConfigured(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A\n\nalpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("# B\n\nbeta"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := memory.New()
	_, err := NewWithEmbedder(repo, fakeEmbedder{}).CompileFolder(context.Background(), Input{
		ScopeKind: "agent",
		ScopeID:   "agent_1",
		SourceID:  "src_1",
		Root:      root,
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := repo.ListKnowledgeChunks(context.Background(), knowledge.ChunkQuery{ScopeKind: "agent", ScopeID: "agent_1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 || len(chunks[0].Vector) == 0 || len(chunks[1].Vector) == 0 {
		t.Fatalf("chunks = %#v, want embedded vectors", chunks)
	}
}
