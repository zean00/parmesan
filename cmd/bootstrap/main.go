package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sahal/parmesan/internal/config"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	maintainerdomain "github.com/sahal/parmesan/internal/domain/maintainer"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/maintainer"
	"github.com/sahal/parmesan/internal/policyyaml"
	"github.com/sahal/parmesan/internal/store/postgres"
)

type agentFile struct {
	ID                    string         `yaml:"id"`
	Name                  string         `yaml:"name"`
	Description           string         `yaml:"description"`
	Status                string         `yaml:"status"`
	PolicyBundlePath      string         `yaml:"policy_bundle_path"`
	KnowledgeSeedPath     string         `yaml:"knowledge_seed_path"`
	KnowledgeSourceID     string         `yaml:"knowledge_source_id"`
	DefaultKnowledgeScope scopeFile      `yaml:"default_knowledge_scope"`
	CapabilityIsolation   map[string]any `yaml:"capability_isolation"`
	Metadata              map[string]any `yaml:"metadata"`
}

type scopeFile struct {
	Kind string `yaml:"kind"`
	ID   string `yaml:"id"`
}

func main() {
	cfg := config.Load("bootstrap")
	if cfg.DatabaseURL == "" {
		exitf("DATABASE_URL or database.url in PARMESAN_CONFIG is required")
	}
	agentsDir := firstNonEmpty(os.Getenv("PARMESAN_AGENTS_DIR"), cfg.Bootstrap.AgentsDir, "agents")
	knowledgeRoot := firstNonEmpty(os.Getenv("KNOWLEDGE_SOURCE_ROOT"), cfg.Knowledge.Root, "knowledge")

	files, err := filepath.Glob(filepath.Join(agentsDir, "*.yaml"))
	if err != nil {
		exitf("list agent definitions: %v", err)
	}
	more, err := filepath.Glob(filepath.Join(agentsDir, "*.yml"))
	if err != nil {
		exitf("list agent definitions: %v", err)
	}
	files = append(files, more...)
	sort.Strings(files)
	if len(files) == 0 {
		exitf("no agent definition files found in %s", agentsDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	repo, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		exitf("connect database: %v", err)
	}
	defer repo.Close()

	service := maintainer.NewService(repo)
	if err := bootstrapProviders(ctx, repo, cfg); err != nil {
		exitf("bootstrap providers: %v", err)
	}
	for _, path := range files {
		if err := bootstrapAgent(ctx, repo, service, path, knowledgeRoot); err != nil {
			exitf("bootstrap %s: %v", path, err)
		}
	}
}

func bootstrapProviders(ctx context.Context, repo *postgres.Client, cfg config.Config) error {
	now := time.Now().UTC()
	for _, provider := range cfg.MCP.Providers {
		if strings.TrimSpace(provider.ID) == "" {
			return errors.New("mcp provider id is required")
		}
		kind := tool.ProviderKind(firstNonEmpty(provider.Kind, string(tool.ProviderMCP)))
		binding := tool.ProviderBinding{
			ID:           strings.TrimSpace(provider.ID),
			Kind:         kind,
			Name:         firstNonEmpty(provider.Name, provider.ID),
			URI:          strings.TrimSpace(provider.BaseURL),
			RegisteredAt: now,
			Healthy:      true,
		}
		if err := repo.RegisterProvider(ctx, binding); err != nil {
			return fmt.Errorf("register provider %s: %w", binding.ID, err)
		}
		fmt.Printf("registered provider %s (%s)\n", binding.ID, binding.Kind)
	}
	return nil
}

func bootstrapAgent(ctx context.Context, repo *postgres.Client, service *maintainer.Service, path, knowledgeRoot string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var spec agentFile
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return fmt.Errorf("parse agent yaml: %w", err)
	}
	spec.ID = strings.TrimSpace(spec.ID)
	if spec.ID == "" {
		return errors.New("id is required")
	}
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = spec.ID
	}
	if strings.TrimSpace(spec.Status) == "" {
		spec.Status = "active"
	}
	scopeKind := firstNonEmpty(spec.DefaultKnowledgeScope.Kind, "agent")
	scopeID := firstNonEmpty(spec.DefaultKnowledgeScope.ID, spec.ID)
	if strings.TrimSpace(spec.PolicyBundlePath) == "" {
		return errors.New("policy_bundle_path is required")
	}

	policyPath := resolveRelative(path, spec.PolicyBundlePath)
	policyRaw, err := os.ReadFile(policyPath)
	if err != nil {
		return fmt.Errorf("read policy bundle %s: %w", policyPath, err)
	}
	bundle, err := policyyaml.ParseBundle(policyRaw)
	if err != nil {
		return fmt.Errorf("parse policy bundle %s: %w", policyPath, err)
	}
	if err := repo.SaveBundle(ctx, bundle); err != nil {
		return fmt.Errorf("save policy bundle: %w", err)
	}

	now := time.Now().UTC()
	metadata := map[string]any{
		"agent_definition_path": path,
		"policy_bundle_path":    policyPath,
		"bootstrapped_by":       "cmd/bootstrap",
	}
	for key, value := range spec.Metadata {
		metadata[key] = value
	}
	if len(spec.CapabilityIsolation) > 0 {
		metadata["capability_isolation"] = spec.CapabilityIsolation
	}
	profile := agent.Profile{
		ID:                        spec.ID,
		Name:                      spec.Name,
		Description:               spec.Description,
		Status:                    spec.Status,
		DefaultPolicyBundleID:     bundle.ID,
		DefaultKnowledgeScopeKind: scopeKind,
		DefaultKnowledgeScopeID:   scopeID,
		Metadata:                  metadata,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	if err := repo.SaveAgentProfile(ctx, profile); err != nil {
		return fmt.Errorf("save agent profile: %w", err)
	}

	if strings.TrimSpace(spec.KnowledgeSeedPath) != "" {
		sourceID := firstNonEmpty(spec.KnowledgeSourceID, "source_"+spec.ID+"_seed")
		seedPath, err := resolveKnowledgeSeed(knowledgeRoot, spec.KnowledgeSeedPath)
		if err != nil {
			return err
		}
		source := knowledge.Source{
			ID:        sourceID,
			ScopeKind: scopeKind,
			ScopeID:   scopeID,
			Kind:      "folder",
			URI:       seedPath,
			Status:    "queued",
			Metadata: map[string]any{
				"agent_id":        spec.ID,
				"seed_path":       spec.KnowledgeSeedPath,
				"bootstrapped_by": "cmd/bootstrap",
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := repo.SaveKnowledgeSource(ctx, source); err != nil {
			return fmt.Errorf("save knowledge source: %w", err)
		}
		job := knowledge.SyncJob{
			ID:          "ksync_" + stableSuffix(sourceID, "bootstrap"),
			SourceID:    sourceID,
			Status:      "queued",
			Force:       true,
			RequestedBy: "bootstrap",
			Metadata: map[string]any{
				"agent_id":        spec.ID,
				"bootstrapped_by": "cmd/bootstrap",
			},
			CreatedAt: now,
		}
		if err := repo.SaveKnowledgeSyncJob(ctx, job); err != nil {
			return fmt.Errorf("queue knowledge sync: %w", err)
		}
	}

	workspaces, err := repo.ListMaintainerWorkspaces(ctx, maintainerdomain.WorkspaceQuery{
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
		Mode:      maintainerdomain.ModeSharedWiki,
		Limit:     1,
	})
	if err != nil {
		return fmt.Errorf("check maintainer workspace: %w", err)
	}
	if len(workspaces) == 0 {
		if _, err := service.QueueBootstrap(ctx, profile, "bootstrap"); err != nil {
			return fmt.Errorf("queue maintainer bootstrap: %w", err)
		}
	}

	fmt.Printf("bootstrapped agent %s with bundle %s\n", profile.ID, bundle.ID)
	return nil
}

func resolveRelative(baseFile, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(baseFile), value))
}

func resolveKnowledgeSeed(root, value string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	resolved := value
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(root, resolved)
	}
	resolved = filepath.Clean(resolved)
	if _, err := os.Stat(resolved); err != nil {
		return "", fmt.Errorf("knowledge seed path %s: %w", resolved, err)
	}
	return resolved, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stableSuffix(parts ...string) string {
	h := sha1.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
