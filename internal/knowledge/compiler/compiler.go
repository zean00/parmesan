package compiler

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/model"
	"github.com/sahal/parmesan/internal/store"
)

type Input struct {
	ScopeKind string
	ScopeID   string
	SourceID  string
	Root      string
}

type Compiler struct {
	repo     store.Repository
	embedder interface {
		Embed(context.Context, []string) (model.EmbeddingResponse, error)
	}
}

func New(repo store.Repository) *Compiler {
	return &Compiler{repo: repo}
}

func NewWithEmbedder(repo store.Repository, embedder interface {
	Embed(context.Context, []string) (model.EmbeddingResponse, error)
}) *Compiler {
	return &Compiler{repo: repo, embedder: embedder}
}

func (c *Compiler) CompileFolder(ctx context.Context, in Input) (knowledge.Snapshot, error) {
	if c == nil || c.repo == nil {
		return knowledge.Snapshot{}, fmt.Errorf("knowledge compiler requires repository")
	}
	root, err := filepath.Abs(in.Root)
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	var paths []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".md" || ext == ".txt" {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return knowledge.Snapshot{}, err
	}
	sort.Strings(paths)

	now := time.Now().UTC()
	var pageIDs []string
	var chunkIDs []string
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return knowledge.Snapshot{}, err
		}
		rel, _ := filepath.Rel(root, path)
		body := strings.TrimSpace(string(raw))
		if body == "" {
			continue
		}
		pageID := stableID("kpage", in.ScopeKind, in.ScopeID, rel)
		citation := knowledge.Citation{
			SourceID: in.SourceID,
			URI:      path,
			Title:    titleFromPath(rel),
		}
		page := knowledge.Page{
			ID:        pageID,
			ScopeKind: in.ScopeKind,
			ScopeID:   in.ScopeID,
			SourceID:  in.SourceID,
			Title:     titleFromContent(rel, body),
			Body:      body,
			PageType:  "source_summary",
			Citations: []knowledge.Citation{citation},
			Metadata:  map[string]any{"source_path": rel},
			Checksum:  checksum(body),
			CreatedAt: now,
			UpdatedAt: now,
		}
		chunks := chunksForPage(page, citation, now)
		if c.embedder != nil && len(chunks) > 0 {
			texts := make([]string, 0, len(chunks))
			for _, chunk := range chunks {
				texts = append(texts, chunk.Text)
			}
			embedded, err := c.embedder.Embed(ctx, texts)
			if err != nil {
				return knowledge.Snapshot{}, err
			}
			for i := range chunks {
				if i < len(embedded.Vectors) {
					chunks[i].Vector = append([]float32(nil), embedded.Vectors[i]...)
				}
			}
		}
		if err := c.repo.SaveKnowledgePage(ctx, page, chunks); err != nil {
			return knowledge.Snapshot{}, err
		}
		pageIDs = append(pageIDs, page.ID)
		for _, chunk := range chunks {
			chunkIDs = append(chunkIDs, chunk.ID)
		}
	}
	snapshot := knowledge.Snapshot{
		ID:        stableID("ksnap", in.ScopeKind, in.ScopeID, strings.Join(pageIDs, ","), strings.Join(chunkIDs, ",")),
		ScopeKind: in.ScopeKind,
		ScopeID:   in.ScopeID,
		PageIDs:   pageIDs,
		ChunkIDs:  chunkIDs,
		Metadata: map[string]any{
			"source_id": in.SourceID,
			"root":      root,
			"compiler":  "deterministic_folder_v1",
		},
		CreatedAt: now,
	}
	if err := c.repo.SaveKnowledgeSnapshot(ctx, snapshot); err != nil {
		return knowledge.Snapshot{}, err
	}
	return snapshot, nil
}

func chunksForPage(page knowledge.Page, citation knowledge.Citation, now time.Time) []knowledge.Chunk {
	paragraphs := strings.Split(page.Body, "\n\n")
	var out []knowledge.Chunk
	for i, para := range paragraphs {
		text := strings.TrimSpace(para)
		if text == "" {
			continue
		}
		out = append(out, knowledge.Chunk{
			ID:        stableID("kchunk", page.ID, fmt.Sprint(i), checksum(text)),
			PageID:    page.ID,
			ScopeKind: page.ScopeKind,
			ScopeID:   page.ScopeID,
			Text:      text,
			Citations: []knowledge.Citation{citation},
			Metadata:  map[string]any{"page_title": page.Title},
			CreatedAt: now,
		})
	}
	if len(out) == 0 {
		out = append(out, knowledge.Chunk{
			ID:        stableID("kchunk", page.ID, "0", page.Checksum),
			PageID:    page.ID,
			ScopeKind: page.ScopeKind,
			ScopeID:   page.ScopeID,
			Text:      page.Body,
			Citations: []knowledge.Citation{citation},
			Metadata:  map[string]any{"page_title": page.Title},
			CreatedAt: now,
		})
	}
	return out
}

func titleFromContent(path string, body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return titleFromPath(path)
}

func titleFromPath(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSpace(strings.TrimSuffix(base, ext))
}

func stableID(prefix string, parts ...string) string {
	return prefix + "_" + checksum(strings.Join(parts, "\x00"))[:16]
}

func checksum(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}
