package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sahal/parmesan/internal/controlgraph"
	"github.com/sahal/parmesan/internal/domain/agent"
	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/artifactmeta"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/customer"
	"github.com/sahal/parmesan/internal/domain/delivery"
	"github.com/sahal/parmesan/internal/domain/execution"
	"github.com/sahal/parmesan/internal/domain/feedback"
	gatewaydomain "github.com/sahal/parmesan/internal/domain/gateway"
	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/knowledge"
	"github.com/sahal/parmesan/internal/domain/maintainer"
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

const (
	policySnapshotIDMetadataKey = "policy_snapshot_id"
	capabilityIDMetadataKey     = "capability_id"
)

func (c *Client) SaveBundle(ctx context.Context, bundle policy.Bundle) error {
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	artifacts, edges, snapshot := policy.MaterializeGraph(bundle)
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO policy_bundles (id, version, source_yaml, bundle_json, imported_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE
		SET version = EXCLUDED.version,
		    source_yaml = EXCLUDED.source_yaml,
		    bundle_json = EXCLUDED.bundle_json,
		    imported_at = EXCLUDED.imported_at
	`, bundle.ID, bundle.Version, bundle.SourceYAML, raw, bundle.ImportedAt)
	if err != nil {
		return err
	}
	if err := c.savePolicyArtifactsTx(ctx, tx, artifacts); err != nil {
		return err
	}
	if err := c.savePolicyEdgesTx(ctx, tx, edges); err != nil {
		return err
	}
	if err := c.savePolicySnapshotTx(ctx, tx, snapshot); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Client) ListBundles(ctx context.Context) ([]policy.Bundle, error) {
	rows, err := c.Pool.Query(ctx, `SELECT bundle_json FROM policy_bundles ORDER BY imported_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []policy.Bundle
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var bundle policy.Bundle
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return nil, err
		}
		out = append(out, bundle)
	}
	return out, rows.Err()
}

func (c *Client) SavePolicyArtifacts(ctx context.Context, items []policy.GraphArtifact) error {
	if c.Pool == nil {
		return c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), items)
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := c.savePolicyArtifactsTx(ctx, tx, items); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Client) GetPolicyArtifact(ctx context.Context, artifactID string) (policy.GraphArtifact, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, bundle_id, kind, version, COALESCE(source_yaml,''), artifact_json, metadata_json, created_at
		FROM policy_artifacts
		WHERE id = $1
	`, artifactID)
	var item policy.GraphArtifact
	var payload, metadata []byte
	if err := row.Scan(&item.ID, &item.BundleID, &item.Kind, &item.Version, &item.SourceYAML, &payload, &metadata, &item.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return policy.GraphArtifact{}, errors.New("policy artifact not found")
		}
		return policy.GraphArtifact{}, err
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &item.Payload); err != nil {
			return policy.GraphArtifact{}, err
		}
	}
	item.ArtifactMeta, _, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListPolicyArtifacts(ctx context.Context, query policy.ArtifactQuery) ([]policy.GraphArtifact, error) {
	sql := `
		SELECT id, bundle_id, kind, version, COALESCE(source_yaml,''), artifact_json, metadata_json, created_at
		FROM policy_artifacts
		WHERE 1=1`
	var args []any
	idx := 1
	if query.ID != "" {
		sql += fmt.Sprintf(" AND id = $%d", idx)
		args = append(args, query.ID)
		idx++
	}
	if query.BundleID != "" {
		sql += fmt.Sprintf(" AND bundle_id = $%d", idx)
		args = append(args, query.BundleID)
		idx++
	}
	if query.Kind != "" {
		sql += fmt.Sprintf(" AND kind = $%d", idx)
		args = append(args, query.Kind)
		idx++
	}
	sql += " ORDER BY created_at DESC"
	if query.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT $%d", idx)
		args = append(args, query.Limit)
	}
	rows, err := c.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []policy.GraphArtifact
	for rows.Next() {
		var item policy.GraphArtifact
		var payload, metadata []byte
		if err := rows.Scan(&item.ID, &item.BundleID, &item.Kind, &item.Version, &item.SourceYAML, &payload, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &item.Payload); err != nil {
				return nil, err
			}
		}
		item.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SavePolicyEdges(ctx context.Context, items []policy.GraphEdge) error {
	if c.Pool == nil {
		return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), items)
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := c.savePolicyEdgesTx(ctx, tx, items); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Client) ListPolicyEdges(ctx context.Context, query policy.EdgeQuery) ([]policy.GraphEdge, error) {
	sql := `
		SELECT id, bundle_id, COALESCE(snapshot_id,''), source_id, kind, target_id, metadata_json, created_at
		FROM policy_edges
		WHERE 1=1`
	var args []any
	idx := 1
	if query.ID != "" {
		sql += fmt.Sprintf(" AND id = $%d", idx)
		args = append(args, query.ID)
		idx++
	}
	if query.BundleID != "" {
		sql += fmt.Sprintf(" AND bundle_id = $%d", idx)
		args = append(args, query.BundleID)
		idx++
	}
	if query.SnapshotID != "" {
		sql += fmt.Sprintf(" AND snapshot_id = $%d", idx)
		args = append(args, query.SnapshotID)
		idx++
	}
	if query.SourceID != "" {
		sql += fmt.Sprintf(" AND source_id = $%d", idx)
		args = append(args, query.SourceID)
		idx++
	}
	if query.TargetID != "" {
		sql += fmt.Sprintf(" AND target_id = $%d", idx)
		args = append(args, query.TargetID)
		idx++
	}
	if query.Kind != "" {
		sql += fmt.Sprintf(" AND kind = $%d", idx)
		args = append(args, query.Kind)
		idx++
	}
	sql += " ORDER BY created_at DESC"
	if query.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT $%d", idx)
		args = append(args, query.Limit)
	}
	rows, err := c.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []policy.GraphEdge
	for rows.Next() {
		var item policy.GraphEdge
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.BundleID, &item.SnapshotID, &item.SourceID, &item.Kind, &item.TargetID, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SavePolicySnapshot(ctx context.Context, snapshot policy.Snapshot) error {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := c.savePolicySnapshotTx(ctx, tx, snapshot); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Client) GetPolicySnapshot(ctx context.Context, snapshotID string) (policy.Snapshot, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, bundle_id, snapshot_json, metadata_json, created_at
		FROM policy_snapshots
		WHERE id = $1
	`, snapshotID)
	var item policy.Snapshot
	var raw, metadata []byte
	if err := row.Scan(&item.ID, &item.BundleID, &raw, &metadata, &item.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return policy.Snapshot{}, errors.New("policy snapshot not found")
		}
		return policy.Snapshot{}, err
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return policy.Snapshot{}, err
	}
	item.ArtifactMeta, _, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListPolicySnapshots(ctx context.Context, query policy.SnapshotQuery) ([]policy.Snapshot, error) {
	sql := `
		SELECT id, bundle_id, snapshot_json, metadata_json, created_at
		FROM policy_snapshots
		WHERE ($1 = '' OR bundle_id = $1)
		ORDER BY created_at DESC`
	args := []any{query.BundleID}
	if query.Limit > 0 {
		sql += " LIMIT $2"
		args = append(args, query.Limit)
	}
	rows, err := c.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []policy.Snapshot
	for rows.Next() {
		var item policy.Snapshot
		var raw, metadata []byte
		if err := rows.Scan(&item.ID, &item.BundleID, &raw, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) savePolicyArtifactsTx(ctx context.Context, tx pgx.Tx, items []policy.GraphArtifact) error {
	return c.savePolicyArtifactsQuery(ctx, tx, items)
}

func (c *Client) savePolicyArtifactsQuery(ctx context.Context, q sessionEventQuerier, items []policy.GraphArtifact) error {
	for _, item := range items {
		raw, err := json.Marshal(item.Payload)
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `
			INSERT INTO policy_artifacts (id, bundle_id, kind, version, source_yaml, artifact_json, metadata_json, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (id) DO UPDATE
			SET bundle_id = EXCLUDED.bundle_id,
			    kind = EXCLUDED.kind,
			    version = EXCLUDED.version,
			    source_yaml = EXCLUDED.source_yaml,
			    artifact_json = EXCLUDED.artifact_json,
			    metadata_json = EXCLUDED.metadata_json,
			    created_at = EXCLUDED.created_at
		`, item.ID, item.BundleID, item.Kind, item.Version, item.SourceYAML, raw, metadataJSON(nil, item.ArtifactMeta), item.CreatedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) savePolicyEdgesTx(ctx context.Context, tx pgx.Tx, items []policy.GraphEdge) error {
	return c.savePolicyEdgesQuery(ctx, tx, items)
}

func (c *Client) savePolicyEdgesQuery(ctx context.Context, q sessionEventQuerier, items []policy.GraphEdge) error {
	for _, item := range items {
		_, err := q.Exec(ctx, `
			INSERT INTO policy_edges (id, bundle_id, snapshot_id, source_id, kind, target_id, metadata_json, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (id) DO UPDATE
			SET bundle_id = EXCLUDED.bundle_id,
			    snapshot_id = EXCLUDED.snapshot_id,
			    source_id = EXCLUDED.source_id,
			    kind = EXCLUDED.kind,
			    target_id = EXCLUDED.target_id,
			    metadata_json = EXCLUDED.metadata_json,
			    created_at = EXCLUDED.created_at
		`, item.ID, item.BundleID, nullString(item.SnapshotID), item.SourceID, item.Kind, item.TargetID, metadataJSON(item.Metadata, item.ArtifactMeta), item.CreatedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) savePolicySnapshotTx(ctx context.Context, tx pgx.Tx, snapshot policy.Snapshot) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO policy_snapshots (id, bundle_id, snapshot_json, metadata_json, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE
		SET bundle_id = EXCLUDED.bundle_id,
		    snapshot_json = EXCLUDED.snapshot_json,
		    metadata_json = EXCLUDED.metadata_json,
		    created_at = EXCLUDED.created_at
	`, snapshot.ID, snapshot.BundleID, raw, metadataJSON(nil, snapshot.ArtifactMeta), snapshot.CreatedAt)
	return err
}

func (c *Client) SaveAgentProfile(ctx context.Context, profile agent.Profile) error {
	db := c.sessionQuery()
	metadata, err := json.Marshal(profile.Metadata)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO agent_profiles (id, name, description, status, default_policy_bundle_id, default_knowledge_scope_kind, default_knowledge_scope_id, metadata_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET name = EXCLUDED.name,
		    description = EXCLUDED.description,
		    status = EXCLUDED.status,
		    default_policy_bundle_id = EXCLUDED.default_policy_bundle_id,
		    default_knowledge_scope_kind = EXCLUDED.default_knowledge_scope_kind,
		    default_knowledge_scope_id = EXCLUDED.default_knowledge_scope_id,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = EXCLUDED.updated_at
	`, profile.ID, profile.Name, profile.Description, profile.Status, nullString(profile.DefaultPolicyBundleID), nullString(profile.DefaultKnowledgeScopeKind), nullString(profile.DefaultKnowledgeScopeID), metadata, profile.CreatedAt, profile.UpdatedAt)
	return err
}

func (c *Client) GetAgentProfile(ctx context.Context, profileID string) (agent.Profile, error) {
	db := c.sessionQuery()
	row := db.QueryRow(ctx, `
		SELECT id, name, COALESCE(description,''), status, COALESCE(default_policy_bundle_id,''), COALESCE(default_knowledge_scope_kind,''), COALESCE(default_knowledge_scope_id,''), metadata_json, created_at, updated_at
		FROM agent_profiles
		WHERE id = $1
	`, profileID)
	var profile agent.Profile
	var metadata []byte
	if err := row.Scan(&profile.ID, &profile.Name, &profile.Description, &profile.Status, &profile.DefaultPolicyBundleID, &profile.DefaultKnowledgeScopeKind, &profile.DefaultKnowledgeScopeID, &metadata, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Profile{}, errors.New("agent profile not found")
		}
		return agent.Profile{}, err
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &profile.Metadata)
	}
	return profile, nil
}

func (c *Client) ListAgentProfiles(ctx context.Context) ([]agent.Profile, error) {
	db := c.sessionQuery()
	rows, err := db.Query(ctx, `
		SELECT id, name, COALESCE(description,''), status, COALESCE(default_policy_bundle_id,''), COALESCE(default_knowledge_scope_kind,''), COALESCE(default_knowledge_scope_id,''), metadata_json, created_at, updated_at
		FROM agent_profiles
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []agent.Profile
	for rows.Next() {
		var profile agent.Profile
		var metadata []byte
		if err := rows.Scan(&profile.ID, &profile.Name, &profile.Description, &profile.Status, &profile.DefaultPolicyBundleID, &profile.DefaultKnowledgeScopeKind, &profile.DefaultKnowledgeScopeID, &metadata, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &profile.Metadata)
		}
		out = append(out, profile)
	}
	return out, rows.Err()
}

func (c *Client) SaveOperator(ctx context.Context, item operator.Operator) error {
	roles, err := json.Marshal(item.Roles)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return err
	}
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO operators (id, display_name, email, roles_json, status, metadata_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE
		SET display_name = EXCLUDED.display_name,
		    email = EXCLUDED.email,
		    roles_json = EXCLUDED.roles_json,
		    status = EXCLUDED.status,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = EXCLUDED.updated_at
	`, item.ID, item.DisplayName, item.Email, roles, item.Status, metadata, item.CreatedAt, item.UpdatedAt)
	return err
}

func (c *Client) GetOperator(ctx context.Context, operatorID string) (operator.Operator, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, display_name, COALESCE(email,''), roles_json, status, metadata_json, created_at, updated_at
		FROM operators
		WHERE id = $1
	`, operatorID)
	var item operator.Operator
	var roles, metadata []byte
	if err := row.Scan(&item.ID, &item.DisplayName, &item.Email, &roles, &item.Status, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operator.Operator{}, errors.New("operator not found")
		}
		return operator.Operator{}, err
	}
	_ = json.Unmarshal(roles, &item.Roles)
	_ = json.Unmarshal(metadata, &item.Metadata)
	return item, nil
}

func (c *Client) ListOperators(ctx context.Context) ([]operator.Operator, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, display_name, COALESCE(email,''), roles_json, status, metadata_json, created_at, updated_at
		FROM operators
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []operator.Operator
	for rows.Next() {
		var item operator.Operator
		var roles, metadata []byte
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Email, &roles, &item.Status, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(roles, &item.Roles)
		_ = json.Unmarshal(metadata, &item.Metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveOperatorAPIToken(ctx context.Context, token operator.APIToken) error {
	metadata, err := json.Marshal(token.Metadata)
	if err != nil {
		return err
	}
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO operator_api_tokens (id, operator_id, name, token_hash, status, last_used_at, expires_at, metadata_json, created_at, revoked_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET name = EXCLUDED.name,
		    token_hash = EXCLUDED.token_hash,
		    status = EXCLUDED.status,
		    last_used_at = EXCLUDED.last_used_at,
		    expires_at = EXCLUDED.expires_at,
		    metadata_json = EXCLUDED.metadata_json,
		    revoked_at = EXCLUDED.revoked_at
	`, token.ID, token.OperatorID, token.Name, token.TokenHash, token.Status, token.LastUsedAt, token.ExpiresAt, metadata, token.CreatedAt, token.RevokedAt)
	return err
}

func (c *Client) GetOperatorAPITokenByHash(ctx context.Context, tokenHash string) (operator.APIToken, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, operator_id, name, token_hash, status, last_used_at, expires_at, metadata_json, created_at, revoked_at
		FROM operator_api_tokens
		WHERE token_hash = $1
	`, tokenHash)
	var item operator.APIToken
	var metadata []byte
	if err := row.Scan(&item.ID, &item.OperatorID, &item.Name, &item.TokenHash, &item.Status, &item.LastUsedAt, &item.ExpiresAt, &metadata, &item.CreatedAt, &item.RevokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operator.APIToken{}, errors.New("operator api token not found")
		}
		return operator.APIToken{}, err
	}
	_ = json.Unmarshal(metadata, &item.Metadata)
	return item, nil
}

func (c *Client) ListOperatorAPITokens(ctx context.Context, operatorID string) ([]operator.APIToken, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, operator_id, name, token_hash, status, last_used_at, expires_at, metadata_json, created_at, revoked_at
		FROM operator_api_tokens
		WHERE ($1 = '' OR operator_id = $1)
		ORDER BY created_at DESC
	`, operatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []operator.APIToken
	for rows.Next() {
		var item operator.APIToken
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.OperatorID, &item.Name, &item.TokenHash, &item.Status, &item.LastUsedAt, &item.ExpiresAt, &metadata, &item.CreatedAt, &item.RevokedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		item.Plaintext = ""
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveCustomerPreference(ctx context.Context, pref customer.Preference, event customer.PreferenceEvent) error {
	db := c.sessionQuery()
	evidence, err := json.Marshal(pref.EvidenceRefs)
	if err != nil {
		return err
	}
	metadata := metadataJSON(pref.Metadata, pref.ArtifactMeta)
	_, err = db.Exec(ctx, `
		INSERT INTO customer_preferences (id, agent_id, customer_id, key, value, source, confidence, status, evidence_refs_json, metadata_json, last_confirmed_at, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (agent_id, customer_id, key) DO UPDATE
		SET value = EXCLUDED.value,
		    source = EXCLUDED.source,
		    confidence = EXCLUDED.confidence,
		    status = EXCLUDED.status,
		    evidence_refs_json = EXCLUDED.evidence_refs_json,
		    metadata_json = EXCLUDED.metadata_json,
		    last_confirmed_at = EXCLUDED.last_confirmed_at,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = EXCLUDED.updated_at
	`, pref.ID, pref.AgentID, pref.CustomerID, pref.Key, pref.Value, pref.Source, pref.Confidence, pref.Status, evidence, metadata, pref.LastConfirmedAt, pref.ExpiresAt, pref.CreatedAt, pref.UpdatedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.CustomerPreferenceRecord(pref)
	if err := c.savePolicyArtifactsQuery(ctx, db, artifacts); err != nil {
		return err
	}
	if err := c.savePolicyEdgesQuery(ctx, db, edges); err != nil {
		return err
	}
	if event.ID != "" {
		return c.AppendCustomerPreferenceEvent(ctx, event)
	}
	return nil
}

func (c *Client) GetCustomerPreference(ctx context.Context, agentID string, customerID string, key string) (customer.Preference, error) {
	db := c.sessionQuery()
	row := db.QueryRow(ctx, `
		SELECT id, agent_id, customer_id, key, value, source, confidence, status, evidence_refs_json, metadata_json, last_confirmed_at, expires_at, created_at, updated_at
		FROM customer_preferences
		WHERE agent_id = $1 AND customer_id = $2 AND key = $3
	`, agentID, customerID, key)
	var pref customer.Preference
	var evidence, metadata []byte
	if err := row.Scan(&pref.ID, &pref.AgentID, &pref.CustomerID, &pref.Key, &pref.Value, &pref.Source, &pref.Confidence, &pref.Status, &evidence, &metadata, &pref.LastConfirmedAt, &pref.ExpiresAt, &pref.CreatedAt, &pref.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return customer.Preference{}, errors.New("customer preference not found")
		}
		return customer.Preference{}, err
	}
	_ = json.Unmarshal(evidence, &pref.EvidenceRefs)
	pref.ArtifactMeta, pref.Metadata, _ = decodeMetadata(metadata)
	return pref, nil
}

func (c *Client) ListCustomerPreferences(ctx context.Context, query customer.PreferenceQuery) ([]customer.Preference, error) {
	db := c.sessionQuery()
	rows, err := db.Query(ctx, `
		SELECT id, agent_id, customer_id, key, value, source, confidence, status, evidence_refs_json, metadata_json, last_confirmed_at, expires_at, created_at, updated_at
		FROM customer_preferences
		WHERE ($1 = '' OR agent_id = $1)
		  AND ($2 = '' OR customer_id = $2)
		  AND ($3 = '' OR status = $3)
		  AND ($4 = '' OR key = $4)
		  AND ($5 = '' OR source = $5)
		  AND ($6::float8 = 0 OR confidence >= $6)
		  AND ($7::bool OR expires_at IS NULL OR expires_at > NOW())
		ORDER BY updated_at DESC
		LIMIT NULLIF($8, 0)
	`, query.AgentID, query.CustomerID, query.Status, query.Key, query.Source, query.MinConfidence, query.IncludeExpired, query.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []customer.Preference
	for rows.Next() {
		var pref customer.Preference
		var evidence, metadata []byte
		if err := rows.Scan(&pref.ID, &pref.AgentID, &pref.CustomerID, &pref.Key, &pref.Value, &pref.Source, &pref.Confidence, &pref.Status, &evidence, &metadata, &pref.LastConfirmedAt, &pref.ExpiresAt, &pref.CreatedAt, &pref.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &pref.EvidenceRefs)
		pref.ArtifactMeta, pref.Metadata, _ = decodeMetadata(metadata)
		out = append(out, pref)
	}
	return out, rows.Err()
}

func (c *Client) AppendCustomerPreferenceEvent(ctx context.Context, event customer.PreferenceEvent) error {
	db := c.sessionQuery()
	evidence, err := json.Marshal(event.EvidenceRefs)
	if err != nil {
		return err
	}
	metadata := metadataJSON(event.Metadata, event.ArtifactMeta)
	_, err = db.Exec(ctx, `
		INSERT INTO customer_preference_events (id, preference_id, agent_id, customer_id, key, value, action, source, confidence, evidence_refs_json, metadata_json, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, event.ID, nullString(event.PreferenceID), event.AgentID, event.CustomerID, event.Key, event.Value, event.Action, event.Source, event.Confidence, evidence, metadata, event.CreatedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.CustomerPreferenceEvent(event)
	if err := c.savePolicyArtifactsQuery(ctx, db, artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, db, edges)
}

func (c *Client) ListCustomerPreferenceEvents(ctx context.Context, query customer.PreferenceQuery) ([]customer.PreferenceEvent, error) {
	db := c.sessionQuery()
	rows, err := db.Query(ctx, `
		SELECT id, COALESCE(preference_id,''), agent_id, customer_id, COALESCE(key,''), COALESCE(value,''), action, source, confidence, evidence_refs_json, metadata_json, created_at
		FROM customer_preference_events
		WHERE ($1 = '' OR agent_id = $1)
		  AND ($2 = '' OR customer_id = $2)
		  AND ($3 = '' OR key = $3)
		  AND ($4 = '' OR source = $4)
		ORDER BY created_at DESC
		LIMIT NULLIF($5, 0)
	`, query.AgentID, query.CustomerID, query.Key, query.Source, query.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []customer.PreferenceEvent
	for rows.Next() {
		var item customer.PreferenceEvent
		var evidence, metadata []byte
		if err := rows.Scan(&item.ID, &item.PreferenceID, &item.AgentID, &item.CustomerID, &item.Key, &item.Value, &item.Action, &item.Source, &item.Confidence, &evidence, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &item.EvidenceRefs)
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveFeedbackRecord(ctx context.Context, record feedback.Record) error {
	db := c.sessionQuery()
	labels, err := json.Marshal(record.Labels)
	if err != nil {
		return err
	}
	targets, err := json.Marshal(record.TargetEventIDs)
	if err != nil {
		return err
	}
	metadata := metadataJSON(record.Metadata, record.ArtifactMeta)
	outputs, err := json.Marshal(record.Outputs)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO operator_feedback (id, session_id, response_id, execution_id, trace_id, operator_id, rating, score, category, text, comment, correction, labels_json, target_event_ids_json, metadata_json, outputs_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (id) DO UPDATE
		SET response_id = EXCLUDED.response_id,
		    execution_id = EXCLUDED.execution_id,
		    trace_id = EXCLUDED.trace_id,
		    operator_id = EXCLUDED.operator_id,
		    rating = EXCLUDED.rating,
		    score = EXCLUDED.score,
		    category = EXCLUDED.category,
		    text = EXCLUDED.text,
		    comment = EXCLUDED.comment,
		    correction = EXCLUDED.correction,
		    labels_json = EXCLUDED.labels_json,
		    target_event_ids_json = EXCLUDED.target_event_ids_json,
		    metadata_json = EXCLUDED.metadata_json,
		    outputs_json = EXCLUDED.outputs_json,
		    updated_at = EXCLUDED.updated_at
	`, record.ID, record.SessionID, nullString(record.ResponseID), nullString(record.ExecutionID), nullString(record.TraceID), nullString(record.OperatorID), record.Rating, nullInt(record.Score), record.Category, record.Text, nullString(record.Comment), nullString(record.Correction), labels, targets, metadata, outputs, record.CreatedAt, record.UpdatedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.FeedbackRecord(record)
	if err := c.savePolicyArtifactsQuery(ctx, db, artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, db, edges)
}

func (c *Client) GetFeedbackRecord(ctx context.Context, feedbackID string) (feedback.Record, error) {
	db := c.sessionQuery()
	row := db.QueryRow(ctx, `
		SELECT id, session_id, COALESCE(response_id,''), COALESCE(execution_id,''), COALESCE(trace_id,''), COALESCE(operator_id,''), rating, COALESCE(score,-1), COALESCE(category,''), text, COALESCE(comment,''), COALESCE(correction,''), labels_json, target_event_ids_json, metadata_json, outputs_json, created_at, updated_at
		FROM operator_feedback
		WHERE id = $1
	`, feedbackID)
	item, err := scanFeedbackRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return feedback.Record{}, errors.New("feedback record not found")
		}
		return feedback.Record{}, err
	}
	return item, nil
}

func (c *Client) ListFeedbackRecords(ctx context.Context, query feedback.Query) ([]feedback.Record, error) {
	db := c.sessionQuery()
	limit := query.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := db.Query(ctx, `
		SELECT id, session_id, COALESCE(response_id,''), COALESCE(execution_id,''), COALESCE(trace_id,''), COALESCE(operator_id,''), rating, COALESCE(score,-1), COALESCE(category,''), text, COALESCE(comment,''), COALESCE(correction,''), labels_json, target_event_ids_json, metadata_json, outputs_json, created_at, updated_at
		FROM operator_feedback
		WHERE ($1 = '' OR session_id = $1)
		  AND ($2 = '' OR operator_id = $2)
		  AND ($3 = '' OR category = $3)
		ORDER BY created_at DESC
		LIMIT $4
	`, query.SessionID, query.OperatorID, query.Category, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []feedback.Record
	for rows.Next() {
		item, err := scanFeedbackRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFeedbackRecord(row rowScanner) (feedback.Record, error) {
	var item feedback.Record
	var labels, targets, metadata, outputs []byte
	var score int
	if err := row.Scan(&item.ID, &item.SessionID, &item.ResponseID, &item.ExecutionID, &item.TraceID, &item.OperatorID, &item.Rating, &score, &item.Category, &item.Text, &item.Comment, &item.Correction, &labels, &targets, &metadata, &outputs, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return feedback.Record{}, err
	}
	if score >= 0 {
		item.Score = &score
	}
	_ = json.Unmarshal(labels, &item.Labels)
	_ = json.Unmarshal(targets, &item.TargetEventIDs)
	item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
	_ = json.Unmarshal(outputs, &item.Outputs)
	return item, nil
}

func (c *Client) CreateSession(ctx context.Context, sess session.Session) error {
	db := c.sessionQuery()
	metadata := metadataJSON(sess.Metadata, sess.ArtifactMeta)
	labels, err := json.Marshal(sess.Labels)
	if err != nil {
		return err
	}
	if sess.Status == "" {
		sess.Status = session.StatusActive
	}
	if sess.LastActivityAt.IsZero() {
		sess.LastActivityAt = sess.CreatedAt
	}
	_, err = db.Exec(ctx, `
		INSERT INTO sessions (id, channel, customer_id, agent_id, mode, status, title, metadata_json, labels_json, last_activity_at, idle_checked_at, awaiting_customer_since, closed_at, close_reason, keep_reason, followup_count, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, TIMESTAMPTZ '0001-01-01 00:00:00+00'), NULLIF($12, TIMESTAMPTZ '0001-01-01 00:00:00+00'), NULLIF($13, TIMESTAMPTZ '0001-01-01 00:00:00+00'), $14, $15, $16, $17)
		ON CONFLICT (id) DO NOTHING
	`, sess.ID, sess.Channel, nullString(sess.CustomerID), nullString(sess.AgentID), sess.Mode, string(sess.Status), nullString(sess.Title), metadata, labels, sess.LastActivityAt, sess.IdleCheckedAt, sess.AwaitingCustomerSince, sess.ClosedAt, nullString(sess.CloseReason), nullString(sess.KeepReason), sess.FollowupCount, sess.CreatedAt)
	return err
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (session.Session, error) {
	row := c.sessionQuery().QueryRow(ctx, `SELECT id, channel, COALESCE(customer_id,''), COALESCE(agent_id,''), COALESCE(mode,''), COALESCE(status,'active'), COALESCE(title,''), metadata_json, labels_json, COALESCE(last_activity_at, created_at), COALESCE(idle_checked_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(awaiting_customer_since, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(closed_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(close_reason,''), COALESCE(keep_reason,''), COALESCE(followup_count,0), created_at FROM sessions WHERE id = $1`, sessionID)
	var sess session.Session
	var metadata []byte
	var labels []byte
	var status string
	if err := row.Scan(&sess.ID, &sess.Channel, &sess.CustomerID, &sess.AgentID, &sess.Mode, &status, &sess.Title, &metadata, &labels, &sess.LastActivityAt, &sess.IdleCheckedAt, &sess.AwaitingCustomerSince, &sess.ClosedAt, &sess.CloseReason, &sess.KeepReason, &sess.FollowupCount, &sess.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return session.Session{}, errors.New("session not found")
		}
		return session.Session{}, err
	}
	sess.Status = session.Status(status)
	sess.ArtifactMeta, sess.Metadata, _ = decodeMetadata(metadata)
	if len(labels) > 0 {
		_ = json.Unmarshal(labels, &sess.Labels)
	}
	return sess, nil
}

func (c *Client) UpdateSession(ctx context.Context, sess session.Session) error {
	db := c.sessionQuery()
	metadata := metadataJSON(sess.Metadata, sess.ArtifactMeta)
	labels, err := json.Marshal(sess.Labels)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		UPDATE sessions
		SET channel = $2,
		    customer_id = $3,
		    agent_id = $4,
		    mode = $5,
		    status = $6,
		    title = $7,
		    metadata_json = $8,
		    labels_json = $9,
		    last_activity_at = $10,
		    idle_checked_at = NULLIF($11, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		    awaiting_customer_since = NULLIF($12, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		    closed_at = NULLIF($13, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		    close_reason = $14,
		    keep_reason = $15,
		    followup_count = $16
		WHERE id = $1
	`, sess.ID, sess.Channel, nullString(sess.CustomerID), nullString(sess.AgentID), sess.Mode, string(sess.Status), nullString(sess.Title), metadata, labels, sess.LastActivityAt, sess.IdleCheckedAt, sess.AwaitingCustomerSince, sess.ClosedAt, nullString(sess.CloseReason), nullString(sess.KeepReason), sess.FollowupCount)
	return err
}

func (c *Client) ListSessions(ctx context.Context) ([]session.Session, error) {
	rows, err := c.sessionQuery().Query(ctx, `SELECT id, channel, COALESCE(customer_id,''), COALESCE(agent_id,''), COALESCE(mode,''), COALESCE(status,'active'), COALESCE(title,''), metadata_json, labels_json, COALESCE(last_activity_at, created_at), COALESCE(idle_checked_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(awaiting_customer_since, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(closed_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(close_reason,''), COALESCE(keep_reason,''), COALESCE(followup_count,0), created_at FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []session.Session
	for rows.Next() {
		var sess session.Session
		var metadata []byte
		var labels []byte
		var status string
		if err := rows.Scan(&sess.ID, &sess.Channel, &sess.CustomerID, &sess.AgentID, &sess.Mode, &status, &sess.Title, &metadata, &labels, &sess.LastActivityAt, &sess.IdleCheckedAt, &sess.AwaitingCustomerSince, &sess.ClosedAt, &sess.CloseReason, &sess.KeepReason, &sess.FollowupCount, &sess.CreatedAt); err != nil {
			return nil, err
		}
		sess.Status = session.Status(status)
		sess.ArtifactMeta, sess.Metadata, _ = decodeMetadata(metadata)
		if len(labels) > 0 {
			_ = json.Unmarshal(labels, &sess.Labels)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (c *Client) SaveSessionWatch(ctx context.Context, watch session.Watch) error {
	arguments, err := json.Marshal(watch.Arguments)
	if err != nil {
		return err
	}
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO session_watches (id, session_id, kind, status, source, subject_ref, tool_id, arguments_json, poll_interval_seconds, next_run_at, stop_condition, dedupe_key, last_result_hash, last_checked_at, metadata_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10, TIMESTAMPTZ '0001-01-01 00:00:00+00'),$11,$12,$13,NULLIF($14, TIMESTAMPTZ '0001-01-01 00:00:00+00'),$15,$16,$17)
		ON CONFLICT (id) DO UPDATE SET
		    session_id = EXCLUDED.session_id,
		    kind = EXCLUDED.kind,
		    status = EXCLUDED.status,
		    source = EXCLUDED.source,
		    subject_ref = EXCLUDED.subject_ref,
		    tool_id = EXCLUDED.tool_id,
		    arguments_json = EXCLUDED.arguments_json,
		    poll_interval_seconds = EXCLUDED.poll_interval_seconds,
		    next_run_at = EXCLUDED.next_run_at,
		    stop_condition = EXCLUDED.stop_condition,
		    dedupe_key = EXCLUDED.dedupe_key,
		    last_result_hash = EXCLUDED.last_result_hash,
		    last_checked_at = EXCLUDED.last_checked_at,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = EXCLUDED.updated_at
	`, watch.ID, watch.SessionID, watch.Kind, string(watch.Status), nullString(watch.Source), nullString(watch.SubjectRef), nullString(watch.ToolID), arguments, int(watch.PollInterval/time.Second), watch.NextRunAt, nullString(watch.StopCondition), nullString(watch.DedupeKey), nullString(watch.LastResultHash), watch.LastCheckedAt, metadataJSON(watchMetadata(watch), watch.ArtifactMeta), watch.CreatedAt, watch.UpdatedAt)
	return err
}

func (c *Client) GetSessionWatch(ctx context.Context, watchID string) (session.Watch, error) {
	row := c.sessionQuery().QueryRow(ctx, `SELECT id, session_id, kind, status, COALESCE(source,''), COALESCE(subject_ref,''), COALESCE(tool_id,''), arguments_json, poll_interval_seconds, COALESCE(next_run_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(stop_condition,''), COALESCE(dedupe_key,''), COALESCE(last_result_hash,''), COALESCE(last_checked_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), metadata_json, created_at, updated_at FROM session_watches WHERE id = $1`, watchID)
	var item session.Watch
	var status string
	var arguments []byte
	var metadata []byte
	var pollSeconds int
	if err := row.Scan(&item.ID, &item.SessionID, &item.Kind, &status, &item.Source, &item.SubjectRef, &item.ToolID, &arguments, &pollSeconds, &item.NextRunAt, &item.StopCondition, &item.DedupeKey, &item.LastResultHash, &item.LastCheckedAt, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return session.Watch{}, errors.New("session watch not found")
		}
		return session.Watch{}, err
	}
	item.Status = session.WatchStatus(status)
	item.PollInterval = time.Duration(pollSeconds) * time.Second
	if len(arguments) > 0 {
		_ = json.Unmarshal(arguments, &item.Arguments)
	}
	item.ArtifactMeta, _, _ = decodeMetadata(metadata)
	applyWatchMetadata(metadata, &item)
	return item, nil
}

func (c *Client) ListSessionWatches(ctx context.Context, query session.WatchQuery) ([]session.Watch, error) {
	sql := `SELECT id, session_id, kind, status, COALESCE(source,''), COALESCE(subject_ref,''), COALESCE(tool_id,''), arguments_json, poll_interval_seconds, COALESCE(next_run_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(stop_condition,''), COALESCE(dedupe_key,''), COALESCE(last_result_hash,''), COALESCE(last_checked_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), metadata_json, created_at, updated_at FROM session_watches WHERE ($1 = '' OR session_id = $1) AND ($2 = '' OR status = $2) ORDER BY created_at DESC`
	rows, err := c.sessionQuery().Query(ctx, sql, query.SessionID, query.Status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []session.Watch
	for rows.Next() {
		var item session.Watch
		var status string
		var arguments []byte
		var metadata []byte
		var pollSeconds int
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Kind, &status, &item.Source, &item.SubjectRef, &item.ToolID, &arguments, &pollSeconds, &item.NextRunAt, &item.StopCondition, &item.DedupeKey, &item.LastResultHash, &item.LastCheckedAt, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Status = session.WatchStatus(status)
		item.PollInterval = time.Duration(pollSeconds) * time.Second
		if len(arguments) > 0 {
			_ = json.Unmarshal(arguments, &item.Arguments)
		}
		item.ArtifactMeta, _, _ = decodeMetadata(metadata)
		applyWatchMetadata(metadata, &item)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) ListRunnableSessionWatches(ctx context.Context, now time.Time) ([]session.Watch, error) {
	rows, err := c.sessionQuery().Query(ctx, `SELECT id, session_id, kind, status, COALESCE(source,''), COALESCE(subject_ref,''), COALESCE(tool_id,''), arguments_json, poll_interval_seconds, COALESCE(next_run_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(stop_condition,''), COALESCE(dedupe_key,''), COALESCE(last_result_hash,''), COALESCE(last_checked_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), metadata_json, created_at, updated_at FROM session_watches WHERE status = 'active' AND (next_run_at IS NULL OR next_run_at <= $1) ORDER BY next_run_at ASC, created_at ASC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []session.Watch
	for rows.Next() {
		var item session.Watch
		var status string
		var arguments []byte
		var metadata []byte
		var pollSeconds int
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Kind, &status, &item.Source, &item.SubjectRef, &item.ToolID, &arguments, &pollSeconds, &item.NextRunAt, &item.StopCondition, &item.DedupeKey, &item.LastResultHash, &item.LastCheckedAt, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Status = session.WatchStatus(status)
		item.PollInterval = time.Duration(pollSeconds) * time.Second
		if len(arguments) > 0 {
			_ = json.Unmarshal(arguments, &item.Arguments)
		}
		item.ArtifactMeta, _, _ = decodeMetadata(metadata)
		applyWatchMetadata(metadata, &item)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) AppendEvent(ctx context.Context, event session.Event) error {
	db := c.sessionQuery()
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO session_events (id, session_id, source, kind, execution_id, payload, created_at, "offset", trace_id, metadata_json, deleted)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO NOTHING
	`, event.ID, event.SessionID, event.Source, event.Kind, nullString(event.ExecutionID), raw, event.CreatedAt, event.Offset, nullString(event.TraceID), metadata, event.Deleted)
	return err
}

func (c *Client) ListEvents(ctx context.Context, sessionID string) ([]session.Event, error) {
	return c.ListEventsFiltered(ctx, session.EventQuery{SessionID: sessionID})
}

func (c *Client) ReadEvent(ctx context.Context, sessionID string, eventID string) (session.Event, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT payload, COALESCE("offset",0), COALESCE(trace_id,''), metadata_json, deleted
		FROM session_events
		WHERE session_id = $1 AND id = $2
	`, sessionID, eventID)
	var raw []byte
	var metadata []byte
	var event session.Event
	if err := row.Scan(&raw, &event.Offset, &event.TraceID, &metadata, &event.Deleted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return session.Event{}, errors.New("event not found")
		}
		return session.Event{}, err
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return session.Event{}, err
	}
	if len(metadata) > 0 && event.Metadata == nil {
		_ = json.Unmarshal(metadata, &event.Metadata)
	}
	return event, nil
}

func (c *Client) UpdateEvent(ctx context.Context, event session.Event) error {
	db := c.sessionQuery()
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		UPDATE session_events
		SET source = $3,
		    kind = $4,
		    execution_id = $5,
		    payload = $6,
		    created_at = $7,
		    "offset" = $8,
		    trace_id = $9,
		    metadata_json = $10,
		    deleted = $11
		WHERE session_id = $1 AND id = $2
	`, event.SessionID, event.ID, event.Source, event.Kind, nullString(event.ExecutionID), raw, event.CreatedAt, event.Offset, nullString(event.TraceID), metadata, event.Deleted)
	return err
}

func (c *Client) ListEventsFiltered(ctx context.Context, query session.EventQuery) ([]session.Event, error) {
	sql := `
		SELECT payload, COALESCE("offset",0), COALESCE(trace_id,''), metadata_json, deleted
		FROM session_events
		WHERE session_id = $1
	`
	args := []any{query.SessionID}
	arg := 2
	if query.Source != "" {
		sql += ` AND source = $` + strconv.Itoa(arg)
		args = append(args, query.Source)
		arg++
	}
	if query.TraceID != "" {
		sql += ` AND trace_id = $` + strconv.Itoa(arg)
		args = append(args, query.TraceID)
		arg++
	}
	if query.MinOffset > 0 {
		sql += ` AND COALESCE("offset",0) >= $` + strconv.Itoa(arg)
		args = append(args, query.MinOffset)
		arg++
	}
	if query.ExcludeDeleted {
		sql += ` AND deleted = FALSE`
	}
	if len(query.Kinds) > 0 {
		sql += ` AND kind = ANY($` + strconv.Itoa(arg) + `)`
		args = append(args, query.Kinds)
		arg++
	}
	sql += ` ORDER BY COALESCE("offset",0) ASC, created_at ASC`
	if query.Limit > 0 {
		sql += ` LIMIT $` + strconv.Itoa(arg)
		args = append(args, query.Limit)
		arg++
	}
	rows, err := c.sessionQuery().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []session.Event
	for rows.Next() {
		var raw []byte
		var event session.Event
		var metadata []byte
		if err := rows.Scan(&raw, &event.Offset, &event.TraceID, &metadata, &event.Deleted); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		if len(metadata) > 0 && event.Metadata == nil {
			_ = json.Unmarshal(metadata, &event.Metadata)
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (c *Client) SaveKnowledgeSource(ctx context.Context, source knowledge.Source) error {
	metadata, err := json.Marshal(source.Metadata)
	if err != nil {
		return err
	}
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO knowledge_sources (id, scope_kind, scope_id, kind, uri, checksum, status, metadata_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET scope_kind = EXCLUDED.scope_kind,
		    scope_id = EXCLUDED.scope_id,
		    kind = EXCLUDED.kind,
		    uri = EXCLUDED.uri,
		    checksum = EXCLUDED.checksum,
		    status = EXCLUDED.status,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = EXCLUDED.updated_at
	`, source.ID, source.ScopeKind, source.ScopeID, source.Kind, source.URI, nullString(source.Checksum), source.Status, metadata, source.CreatedAt, source.UpdatedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.KnowledgeSource(source)
	if err := c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), edges)
}

func (c *Client) GetKnowledgeSource(ctx context.Context, sourceID string) (knowledge.Source, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, scope_kind, scope_id, kind, uri, COALESCE(checksum,''), status, metadata_json, created_at, updated_at
		FROM knowledge_sources WHERE id = $1
	`, sourceID)
	var out knowledge.Source
	var metadata []byte
	if err := row.Scan(&out.ID, &out.ScopeKind, &out.ScopeID, &out.Kind, &out.URI, &out.Checksum, &out.Status, &metadata, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return knowledge.Source{}, errors.New("knowledge source not found")
		}
		return knowledge.Source{}, err
	}
	out.ArtifactMeta, out.Metadata, _ = decodeMetadata(metadata)
	return out, nil
}

func (c *Client) ListKnowledgeSources(ctx context.Context, scopeKind string, scopeID string) ([]knowledge.Source, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, scope_kind, scope_id, kind, uri, COALESCE(checksum,''), status, metadata_json, created_at, updated_at
		FROM knowledge_sources
		WHERE ($1 = '' OR scope_kind = $1) AND ($2 = '' OR scope_id = $2)
		ORDER BY created_at DESC
	`, scopeKind, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.Source
	for rows.Next() {
		var item knowledge.Source
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.Kind, &item.URI, &item.Checksum, &item.Status, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveMaintainerWorkspace(ctx context.Context, item maintainer.Workspace) error {
	schema, err := json.Marshal(item.Schema)
	if err != nil {
		return err
	}
	metadata := metadataJSON(item.Metadata, item.ArtifactMeta)
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO knowledge_workspaces (id, scope_kind, scope_id, mode, status, schema_json, index_page_id, log_page_id, metadata_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE
		SET scope_kind = EXCLUDED.scope_kind,
		    scope_id = EXCLUDED.scope_id,
		    mode = EXCLUDED.mode,
		    status = EXCLUDED.status,
		    schema_json = EXCLUDED.schema_json,
		    index_page_id = EXCLUDED.index_page_id,
		    log_page_id = EXCLUDED.log_page_id,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = EXCLUDED.updated_at
	`, item.ID, item.ScopeKind, item.ScopeID, item.Mode, item.Status, schema, nullString(item.IndexPageID), nullString(item.LogPageID), metadata, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.MaintainerWorkspace(item)
	if err := c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), edges)
}

func (c *Client) GetMaintainerWorkspace(ctx context.Context, workspaceID string) (maintainer.Workspace, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, scope_kind, scope_id, mode, status, schema_json, COALESCE(index_page_id,''), COALESCE(log_page_id,''), metadata_json, created_at, updated_at
		FROM knowledge_workspaces WHERE id = $1
	`, workspaceID)
	var item maintainer.Workspace
	var schema []byte
	var metadata []byte
	if err := row.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.Mode, &item.Status, &schema, &item.IndexPageID, &item.LogPageID, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return maintainer.Workspace{}, errors.New("maintainer workspace not found")
		}
		return maintainer.Workspace{}, err
	}
	_ = json.Unmarshal(schema, &item.Schema)
	item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListMaintainerWorkspaces(ctx context.Context, query maintainer.WorkspaceQuery) ([]maintainer.Workspace, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, scope_kind, scope_id, mode, status, schema_json, COALESCE(index_page_id,''), COALESCE(log_page_id,''), metadata_json, created_at, updated_at
		FROM knowledge_workspaces
		WHERE ($1 = '' OR scope_kind = $1)
		  AND ($2 = '' OR scope_id = $2)
		  AND ($3 = '' OR mode = $3)
		ORDER BY updated_at DESC
		LIMIT $4
	`, query.ScopeKind, query.ScopeID, query.Mode, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []maintainer.Workspace
	for rows.Next() {
		var item maintainer.Workspace
		var schema []byte
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.Mode, &item.Status, &schema, &item.IndexPageID, &item.LogPageID, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(schema, &item.Schema)
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveMaintainerJob(ctx context.Context, item maintainer.Job) error {
	metadata := metadataJSON(item.Metadata, item.ArtifactMeta)
	_, err := c.sessionQuery().Exec(ctx, `
		INSERT INTO knowledge_maintainer_jobs (id, workspace_id, scope_kind, scope_id, agent_id, customer_id, mode, trigger, status, requested_by, source_id, session_id, feedback_id, response_id, run_id, error, metadata_json, created_at, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT (id) DO UPDATE
		SET workspace_id = EXCLUDED.workspace_id,
		    status = EXCLUDED.status,
		    requested_by = EXCLUDED.requested_by,
		    response_id = EXCLUDED.response_id,
		    run_id = EXCLUDED.run_id,
		    error = EXCLUDED.error,
		    metadata_json = EXCLUDED.metadata_json,
		    started_at = EXCLUDED.started_at,
		    finished_at = EXCLUDED.finished_at
	`, item.ID, item.WorkspaceID, item.ScopeKind, item.ScopeID, item.AgentID, item.CustomerID, item.Mode, item.Trigger, item.Status, item.RequestedBy, item.SourceID, item.SessionID, item.FeedbackID, item.ResponseID, item.RunID, item.Error, metadata, item.CreatedAt, item.StartedAt, item.FinishedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.MaintainerJob(item)
	if err := c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), edges)
}

func (c *Client) GetMaintainerJob(ctx context.Context, jobID string) (maintainer.Job, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, COALESCE(workspace_id,''), scope_kind, scope_id, COALESCE(agent_id,''), COALESCE(customer_id,''), mode, trigger, status, COALESCE(requested_by,''), COALESCE(source_id,''), COALESCE(session_id,''), COALESCE(feedback_id,''), COALESCE(response_id,''), COALESCE(run_id,''), COALESCE(error,''), metadata_json, created_at, started_at, finished_at
		FROM knowledge_maintainer_jobs WHERE id = $1
	`, jobID)
	var item maintainer.Job
	var metadata []byte
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.ScopeKind, &item.ScopeID, &item.AgentID, &item.CustomerID, &item.Mode, &item.Trigger, &item.Status, &item.RequestedBy, &item.SourceID, &item.SessionID, &item.FeedbackID, &item.ResponseID, &item.RunID, &item.Error, &metadata, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return maintainer.Job{}, errors.New("maintainer job not found")
		}
		return maintainer.Job{}, err
	}
	item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListMaintainerJobs(ctx context.Context, query maintainer.JobQuery) ([]maintainer.Job, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, COALESCE(workspace_id,''), scope_kind, scope_id, COALESCE(agent_id,''), COALESCE(customer_id,''), mode, trigger, status, COALESCE(requested_by,''), COALESCE(source_id,''), COALESCE(session_id,''), COALESCE(feedback_id,''), COALESCE(response_id,''), COALESCE(run_id,''), COALESCE(error,''), metadata_json, created_at, started_at, finished_at
		FROM knowledge_maintainer_jobs
		WHERE ($1 = '' OR scope_kind = $1)
		  AND ($2 = '' OR scope_id = $2)
		  AND ($3 = '' OR mode = $3)
		  AND ($4 = '' OR status = $4)
		  AND ($5 = '' OR source_id = $5)
		  AND ($6 = '' OR session_id = $6)
		  AND ($7 = '' OR feedback_id = $7)
		ORDER BY created_at DESC
		LIMIT $8
	`, query.ScopeKind, query.ScopeID, query.Mode, query.Status, query.SourceID, query.SessionID, query.FeedbackID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []maintainer.Job
	for rows.Next() {
		var item maintainer.Job
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.ScopeKind, &item.ScopeID, &item.AgentID, &item.CustomerID, &item.Mode, &item.Trigger, &item.Status, &item.RequestedBy, &item.SourceID, &item.SessionID, &item.FeedbackID, &item.ResponseID, &item.RunID, &item.Error, &metadata, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) ListRunnableMaintainerJobs(ctx context.Context) ([]maintainer.Job, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, COALESCE(workspace_id,''), scope_kind, scope_id, COALESCE(agent_id,''), COALESCE(customer_id,''), mode, trigger, status, COALESCE(requested_by,''), COALESCE(source_id,''), COALESCE(session_id,''), COALESCE(feedback_id,''), COALESCE(response_id,''), COALESCE(run_id,''), COALESCE(error,''), metadata_json, created_at, started_at, finished_at
		FROM knowledge_maintainer_jobs
		WHERE status IN ('queued','running')
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []maintainer.Job
	for rows.Next() {
		var item maintainer.Job
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.ScopeKind, &item.ScopeID, &item.AgentID, &item.CustomerID, &item.Mode, &item.Trigger, &item.Status, &item.RequestedBy, &item.SourceID, &item.SessionID, &item.FeedbackID, &item.ResponseID, &item.RunID, &item.Error, &metadata, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveMaintainerRun(ctx context.Context, item maintainer.Run) error {
	inputSummary, err := json.Marshal(item.InputSummary)
	if err != nil {
		return err
	}
	outputSummary, err := json.Marshal(item.OutputSummary)
	if err != nil {
		return err
	}
	metadata := metadataJSON(item.Metadata, item.ArtifactMeta)
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO knowledge_maintainer_runs (id, job_id, workspace_id, scope_kind, scope_id, agent_id, customer_id, mode, trigger, status, response_id, provider, trace_id, input_summary_json, output_summary_json, metadata_json, created_at, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT (id) DO UPDATE
		SET workspace_id = EXCLUDED.workspace_id,
		    status = EXCLUDED.status,
		    response_id = EXCLUDED.response_id,
		    provider = EXCLUDED.provider,
		    trace_id = EXCLUDED.trace_id,
		    input_summary_json = EXCLUDED.input_summary_json,
		    output_summary_json = EXCLUDED.output_summary_json,
		    metadata_json = EXCLUDED.metadata_json,
		    started_at = EXCLUDED.started_at,
		    finished_at = EXCLUDED.finished_at
	`, item.ID, item.JobID, item.WorkspaceID, item.ScopeKind, item.ScopeID, item.AgentID, item.CustomerID, item.Mode, item.Trigger, item.Status, item.ResponseID, item.Provider, item.TraceID, inputSummary, outputSummary, metadata, item.CreatedAt, item.StartedAt, item.FinishedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.MaintainerRun(item)
	if err := c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), edges)
}

func (c *Client) GetMaintainerRun(ctx context.Context, runID string) (maintainer.Run, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, job_id, COALESCE(workspace_id,''), scope_kind, scope_id, COALESCE(agent_id,''), COALESCE(customer_id,''), mode, trigger, status, COALESCE(response_id,''), COALESCE(provider,''), COALESCE(trace_id,''), input_summary_json, output_summary_json, metadata_json, created_at, started_at, finished_at
		FROM knowledge_maintainer_runs WHERE id = $1
	`, runID)
	var item maintainer.Run
	var inputSummary []byte
	var outputSummary []byte
	var metadata []byte
	if err := row.Scan(&item.ID, &item.JobID, &item.WorkspaceID, &item.ScopeKind, &item.ScopeID, &item.AgentID, &item.CustomerID, &item.Mode, &item.Trigger, &item.Status, &item.ResponseID, &item.Provider, &item.TraceID, &inputSummary, &outputSummary, &metadata, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return maintainer.Run{}, errors.New("maintainer run not found")
		}
		return maintainer.Run{}, err
	}
	_ = json.Unmarshal(inputSummary, &item.InputSummary)
	_ = json.Unmarshal(outputSummary, &item.OutputSummary)
	item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListMaintainerRuns(ctx context.Context, query maintainer.RunQuery) ([]maintainer.Run, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, job_id, COALESCE(workspace_id,''), scope_kind, scope_id, COALESCE(agent_id,''), COALESCE(customer_id,''), mode, trigger, status, COALESCE(response_id,''), COALESCE(provider,''), COALESCE(trace_id,''), input_summary_json, output_summary_json, metadata_json, created_at, started_at, finished_at
		FROM knowledge_maintainer_runs
		WHERE ($1 = '' OR job_id = $1)
		  AND ($2 = '' OR workspace_id = $2)
		  AND ($3 = '' OR scope_kind = $3)
		  AND ($4 = '' OR scope_id = $4)
		  AND ($5 = '' OR status = $5)
		ORDER BY created_at DESC
		LIMIT $6
	`, query.JobID, query.WorkspaceID, query.ScopeKind, query.ScopeID, query.Status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []maintainer.Run
	for rows.Next() {
		var item maintainer.Run
		var inputSummary []byte
		var outputSummary []byte
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.JobID, &item.WorkspaceID, &item.ScopeKind, &item.ScopeID, &item.AgentID, &item.CustomerID, &item.Mode, &item.Trigger, &item.Status, &item.ResponseID, &item.Provider, &item.TraceID, &inputSummary, &outputSummary, &metadata, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(inputSummary, &item.InputSummary)
		_ = json.Unmarshal(outputSummary, &item.OutputSummary)
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveKnowledgeSyncJob(ctx context.Context, job knowledge.SyncJob) error {
	metadata := metadataJSON(job.Metadata, job.ArtifactMeta)
	_, err := c.sessionQuery().Exec(ctx, `
		INSERT INTO knowledge_source_sync_jobs (id, source_id, status, force, requested_by, error, old_checksum, new_checksum, snapshot_id, changed, metadata_json, created_at, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    force = EXCLUDED.force,
		    requested_by = EXCLUDED.requested_by,
		    error = EXCLUDED.error,
		    old_checksum = EXCLUDED.old_checksum,
		    new_checksum = EXCLUDED.new_checksum,
		    snapshot_id = EXCLUDED.snapshot_id,
		    changed = EXCLUDED.changed,
		    metadata_json = EXCLUDED.metadata_json,
		    started_at = EXCLUDED.started_at,
		    finished_at = EXCLUDED.finished_at
	`, job.ID, job.SourceID, job.Status, job.Force, nullString(job.RequestedBy), nullString(job.Error), nullString(job.OldChecksum), nullString(job.NewChecksum), nullString(job.SnapshotID), job.Changed, metadata, job.CreatedAt, job.StartedAt, job.FinishedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.KnowledgeSyncJob(job)
	if err := c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), edges)
}

func (c *Client) GetKnowledgeSyncJob(ctx context.Context, jobID string) (knowledge.SyncJob, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, source_id, status, force, COALESCE(requested_by,''), COALESCE(error,''), COALESCE(old_checksum,''), COALESCE(new_checksum,''), COALESCE(snapshot_id,''), changed, metadata_json, created_at, started_at, finished_at
		FROM knowledge_source_sync_jobs WHERE id = $1
	`, jobID)
	var item knowledge.SyncJob
	var metadata []byte
	if err := row.Scan(&item.ID, &item.SourceID, &item.Status, &item.Force, &item.RequestedBy, &item.Error, &item.OldChecksum, &item.NewChecksum, &item.SnapshotID, &item.Changed, &metadata, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return knowledge.SyncJob{}, errors.New("knowledge sync job not found")
		}
		return knowledge.SyncJob{}, err
	}
	item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListKnowledgeSyncJobs(ctx context.Context, query knowledge.SyncJobQuery) ([]knowledge.SyncJob, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, source_id, status, force, COALESCE(requested_by,''), COALESCE(error,''), COALESCE(old_checksum,''), COALESCE(new_checksum,''), COALESCE(snapshot_id,''), changed, metadata_json, created_at, started_at, finished_at
		FROM knowledge_source_sync_jobs
		WHERE ($1 = '' OR source_id = $1)
		  AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC
		LIMIT $3
	`, query.SourceID, query.Status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.SyncJob
	for rows.Next() {
		var item knowledge.SyncJob
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.SourceID, &item.Status, &item.Force, &item.RequestedBy, &item.Error, &item.OldChecksum, &item.NewChecksum, &item.SnapshotID, &item.Changed, &metadata, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) ListRunnableKnowledgeSyncJobs(ctx context.Context) ([]knowledge.SyncJob, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, source_id, status, force, COALESCE(requested_by,''), COALESCE(error,''), COALESCE(old_checksum,''), COALESCE(new_checksum,''), COALESCE(snapshot_id,''), changed, metadata_json, created_at, started_at, finished_at
		FROM knowledge_source_sync_jobs
		WHERE status IN ('queued','running')
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.SyncJob
	for rows.Next() {
		var item knowledge.SyncJob
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.SourceID, &item.Status, &item.Force, &item.RequestedBy, &item.Error, &item.OldChecksum, &item.NewChecksum, &item.SnapshotID, &item.Changed, &metadata, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveKnowledgePage(ctx context.Context, page knowledge.Page, chunks []knowledge.Chunk) error {
	citations, err := json.Marshal(page.Citations)
	if err != nil {
		return err
	}
	metadata := metadataJSON(page.Metadata, page.ArtifactMeta)
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO knowledge_pages (id, scope_kind, scope_id, source_id, title, body, page_type, citations_json, metadata_json, checksum, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE
		SET scope_kind = EXCLUDED.scope_kind,
		    scope_id = EXCLUDED.scope_id,
		    source_id = EXCLUDED.source_id,
		    title = EXCLUDED.title,
		    body = EXCLUDED.body,
		    page_type = EXCLUDED.page_type,
		    citations_json = EXCLUDED.citations_json,
		    metadata_json = EXCLUDED.metadata_json,
		    checksum = EXCLUDED.checksum,
		    updated_at = EXCLUDED.updated_at
	`, page.ID, page.ScopeKind, page.ScopeID, nullString(page.SourceID), page.Title, page.Body, page.PageType, citations, metadata, nullString(page.Checksum), page.CreatedAt, page.UpdatedAt)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM knowledge_chunks WHERE page_id = $1`, page.ID); err != nil {
		return err
	}
	for _, chunk := range chunks {
		vector, err := json.Marshal(chunk.Vector)
		if err != nil {
			return err
		}
		citations, err := json.Marshal(chunk.Citations)
		if err != nil {
			return err
		}
		metadata := metadataJSON(chunk.Metadata, chunk.ArtifactMeta)
		embedding := nullString(vectorLiteral(chunk.Vector))
		if _, err = tx.Exec(ctx, `
			INSERT INTO knowledge_chunks (id, page_id, scope_kind, scope_id, text, embedding, embedding_json, citations_json, metadata_json, created_at)
			VALUES ($1,$2,$3,$4,$5,$6::vector,$7,$8,$9,$10)
		`, chunk.ID, chunk.PageID, chunk.ScopeKind, chunk.ScopeID, chunk.Text, embedding, vector, citations, metadata, chunk.CreatedAt); err != nil {
			return err
		}
	}
	var snapshotIDs []string
	rows, err := tx.Query(ctx, `
		SELECT id
		FROM knowledge_snapshots
		WHERE scope_kind = $1
		  AND scope_id = $2
		  AND page_ids_json ? $3
	`, page.ScopeKind, page.ScopeID, page.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var snapshotID string
		if err := rows.Scan(&snapshotID); err != nil {
			rows.Close()
			return err
		}
		snapshotIDs = append(snapshotIDs, snapshotID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	artifacts, edges := controlgraph.KnowledgePage(page, snapshotIDs)
	if err := c.savePolicyArtifactsQuery(ctx, tx, artifacts); err != nil {
		return err
	}
	if err := c.savePolicyEdgesQuery(ctx, tx, edges); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Client) ListKnowledgePages(ctx context.Context, query knowledge.PageQuery) ([]knowledge.Page, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT p.id, p.scope_kind, p.scope_id, COALESCE(p.source_id,''), p.title, p.body, p.page_type, p.citations_json, p.metadata_json, COALESCE(p.checksum,''), p.created_at, p.updated_at
		FROM knowledge_pages p
		WHERE ($1 = '' OR p.scope_kind = $1)
		  AND ($2 = '' OR p.scope_id = $2)
		  AND ($3 = '' OR p.id IN (
		    SELECT jsonb_array_elements_text(page_ids_json) FROM knowledge_snapshots WHERE id = $3
		  ))
		ORDER BY p.updated_at DESC
		LIMIT $4
	`, query.ScopeKind, query.ScopeID, query.SnapshotID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.Page
	for rows.Next() {
		var item knowledge.Page
		var citations, metadata []byte
		if err := rows.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.SourceID, &item.Title, &item.Body, &item.PageType, &citations, &metadata, &item.Checksum, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(citations, &item.Citations)
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) ListKnowledgeChunks(ctx context.Context, query knowledge.ChunkQuery) ([]knowledge.Chunk, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT ch.id, ch.page_id, ch.scope_kind, ch.scope_id, ch.text, ch.embedding_json, ch.citations_json, ch.metadata_json, ch.created_at
		FROM knowledge_chunks ch
		WHERE ($1 = '' OR ch.scope_kind = $1)
		  AND ($2 = '' OR ch.scope_id = $2)
		  AND ($3 = '' OR ch.id IN (
		    SELECT jsonb_array_elements_text(chunk_ids_json) FROM knowledge_snapshots WHERE id = $3
		  ))
		ORDER BY ch.created_at DESC
		LIMIT $4
	`, query.ScopeKind, query.ScopeID, query.SnapshotID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.Chunk
	for rows.Next() {
		var item knowledge.Chunk
		var vector, citations, metadata []byte
		if err := rows.Scan(&item.ID, &item.PageID, &item.ScopeKind, &item.ScopeID, &item.Text, &vector, &citations, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(vector, &item.Vector)
		_ = json.Unmarshal(citations, &item.Citations)
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SearchKnowledgeChunks(ctx context.Context, query knowledge.ChunkSearchQuery) ([]knowledge.Chunk, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 3
	}
	if len(query.Vector) == 0 {
		return c.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{
			ScopeKind:  query.ScopeKind,
			ScopeID:    query.ScopeID,
			SnapshotID: query.SnapshotID,
			Limit:      limit,
		})
	}
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT ch.id, ch.page_id, ch.scope_kind, ch.scope_id, ch.text, ch.embedding_json, ch.citations_json, ch.metadata_json, ch.created_at
		FROM knowledge_chunks ch
		WHERE ($1 = '' OR ch.scope_kind = $1)
		  AND ($2 = '' OR ch.scope_id = $2)
		  AND ($3 = '' OR ch.id IN (
		    SELECT jsonb_array_elements_text(chunk_ids_json) FROM knowledge_snapshots WHERE id = $3
		  ))
		  AND ch.embedding IS NOT NULL
		ORDER BY ch.embedding <=> $4::vector ASC, ch.created_at DESC
		LIMIT $5
	`, query.ScopeKind, query.ScopeID, query.SnapshotID, vectorLiteral(query.Vector), limit)
	if err != nil {
		return c.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{
			ScopeKind:  query.ScopeKind,
			ScopeID:    query.ScopeID,
			SnapshotID: query.SnapshotID,
			Limit:      limit,
		})
	}
	defer rows.Close()
	var out []knowledge.Chunk
	for rows.Next() {
		var item knowledge.Chunk
		var vector, citations, metadata []byte
		if err := rows.Scan(&item.ID, &item.PageID, &item.ScopeKind, &item.ScopeID, &item.Text, &vector, &citations, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(vector, &item.Vector)
		_ = json.Unmarshal(citations, &item.Citations)
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	if len(out) > 0 || rows.Err() == nil {
		return out, rows.Err()
	}
	return c.ListKnowledgeChunks(ctx, knowledge.ChunkQuery{
		ScopeKind:  query.ScopeKind,
		ScopeID:    query.ScopeID,
		SnapshotID: query.SnapshotID,
		Limit:      limit,
	})
}

func (c *Client) SaveKnowledgeSnapshot(ctx context.Context, snapshot knowledge.Snapshot) error {
	pageIDs, err := json.Marshal(snapshot.PageIDs)
	if err != nil {
		return err
	}
	chunkIDs, err := json.Marshal(snapshot.ChunkIDs)
	if err != nil {
		return err
	}
	metadata := metadataJSON(snapshot.Metadata, snapshot.ArtifactMeta)
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO knowledge_snapshots (id, scope_kind, scope_id, page_ids_json, chunk_ids_json, metadata_json, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (id) DO UPDATE
		SET scope_kind = EXCLUDED.scope_kind,
		    scope_id = EXCLUDED.scope_id,
		    page_ids_json = EXCLUDED.page_ids_json,
		    chunk_ids_json = EXCLUDED.chunk_ids_json,
		    metadata_json = EXCLUDED.metadata_json
	`, snapshot.ID, snapshot.ScopeKind, snapshot.ScopeID, pageIDs, chunkIDs, metadata, snapshot.CreatedAt)
	if err != nil {
		return err
	}
	var previousSnapshotID string
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id
		FROM knowledge_snapshots
		WHERE scope_kind = $1
		  AND scope_id = $2
		  AND id <> $3
		ORDER BY created_at DESC
		LIMIT 1
	`, snapshot.ScopeKind, snapshot.ScopeID, snapshot.ID)
	if err := row.Scan(&previousSnapshotID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	artifacts, edges := controlgraph.KnowledgeSnapshotPrevious(snapshot, previousSnapshotID)
	if err := c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), edges)
}

func (c *Client) GetKnowledgeSnapshot(ctx context.Context, snapshotID string) (knowledge.Snapshot, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, scope_kind, scope_id, page_ids_json, chunk_ids_json, metadata_json, created_at
		FROM knowledge_snapshots WHERE id = $1
	`, snapshotID)
	var out knowledge.Snapshot
	var pageIDs, chunkIDs, metadata []byte
	if err := row.Scan(&out.ID, &out.ScopeKind, &out.ScopeID, &pageIDs, &chunkIDs, &metadata, &out.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return knowledge.Snapshot{}, errors.New("knowledge snapshot not found")
		}
		return knowledge.Snapshot{}, err
	}
	_ = json.Unmarshal(pageIDs, &out.PageIDs)
	_ = json.Unmarshal(chunkIDs, &out.ChunkIDs)
	out.ArtifactMeta, out.Metadata, _ = decodeMetadata(metadata)
	return out, nil
}

func (c *Client) ListKnowledgeSnapshots(ctx context.Context, query knowledge.SnapshotQuery) ([]knowledge.Snapshot, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, scope_kind, scope_id, page_ids_json, chunk_ids_json, metadata_json, created_at
		FROM knowledge_snapshots
		WHERE ($1 = '' OR scope_kind = $1) AND ($2 = '' OR scope_id = $2)
		ORDER BY created_at DESC
		LIMIT $3
	`, query.ScopeKind, query.ScopeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.Snapshot
	for rows.Next() {
		var item knowledge.Snapshot
		var pageIDs, chunkIDs, metadata []byte
		if err := rows.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &pageIDs, &chunkIDs, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(pageIDs, &item.PageIDs)
		_ = json.Unmarshal(chunkIDs, &item.ChunkIDs)
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveKnowledgeUpdateProposal(ctx context.Context, proposal knowledge.UpdateProposal) error {
	evidence, err := json.Marshal(proposal.Evidence)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(proposal.Payload)
	if err != nil {
		return err
	}
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO knowledge_update_proposals (id, scope_kind, scope_id, kind, state, rationale, evidence_json, payload_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET kind = EXCLUDED.kind,
		    state = EXCLUDED.state,
		    rationale = EXCLUDED.rationale,
		    evidence_json = EXCLUDED.evidence_json,
		    payload_json = EXCLUDED.payload_json,
		    updated_at = EXCLUDED.updated_at
	`, proposal.ID, proposal.ScopeKind, proposal.ScopeID, proposal.Kind, proposal.State, proposal.Rationale, evidence, payload, proposal.CreatedAt, proposal.UpdatedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.KnowledgeUpdateProposal(proposal)
	if err := c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), edges)
}

func (c *Client) GetKnowledgeUpdateProposal(ctx context.Context, proposalID string) (knowledge.UpdateProposal, error) {
	row := c.sessionQuery().QueryRow(ctx, `
		SELECT id, scope_kind, scope_id, kind, state, rationale, evidence_json, payload_json, created_at, updated_at
		FROM knowledge_update_proposals
		WHERE id = $1
	`, proposalID)
	var item knowledge.UpdateProposal
	var evidence, payload []byte
	if err := row.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.Kind, &item.State, &item.Rationale, &evidence, &payload, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return knowledge.UpdateProposal{}, errors.New("knowledge update proposal not found")
		}
		return knowledge.UpdateProposal{}, err
	}
	_ = json.Unmarshal(evidence, &item.Evidence)
	_ = json.Unmarshal(payload, &item.Payload)
	return item, nil
}

func (c *Client) ListKnowledgeUpdateProposals(ctx context.Context, scopeKind string, scopeID string) ([]knowledge.UpdateProposal, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, scope_kind, scope_id, kind, state, rationale, evidence_json, payload_json, created_at, updated_at
		FROM knowledge_update_proposals
		WHERE ($1 = '' OR scope_kind = $1) AND ($2 = '' OR scope_id = $2)
		ORDER BY created_at DESC
	`, scopeKind, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.UpdateProposal
	for rows.Next() {
		var item knowledge.UpdateProposal
		var evidence, payload []byte
		if err := rows.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.Kind, &item.State, &item.Rationale, &evidence, &payload, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &item.Evidence)
		_ = json.Unmarshal(payload, &item.Payload)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveKnowledgeLintFinding(ctx context.Context, finding knowledge.LintFinding) error {
	evidence, err := json.Marshal(finding.Evidence)
	if err != nil {
		return err
	}
	metadata := metadataJSON(finding.Metadata, finding.ArtifactMeta)
	_, err = c.sessionQuery().Exec(ctx, `
		INSERT INTO knowledge_lint_findings (id, scope_kind, scope_id, proposal_id, page_id, source_id, kind, severity, status, message, evidence_json, metadata_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    message = EXCLUDED.message,
		    evidence_json = EXCLUDED.evidence_json,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = EXCLUDED.updated_at
	`, finding.ID, finding.ScopeKind, finding.ScopeID, nullString(finding.ProposalID), nullString(finding.PageID), nullString(finding.SourceID), finding.Kind, finding.Severity, finding.Status, finding.Message, evidence, metadata, finding.CreatedAt, finding.UpdatedAt)
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.KnowledgeLintFinding(finding)
	if err := c.savePolicyArtifactsQuery(ctx, c.sessionQuery(), artifacts); err != nil {
		return err
	}
	return c.savePolicyEdgesQuery(ctx, c.sessionQuery(), edges)
}

func (c *Client) ListKnowledgeLintFindings(ctx context.Context, query knowledge.LintQuery) ([]knowledge.LintFinding, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, scope_kind, scope_id, COALESCE(proposal_id,''), COALESCE(page_id,''), COALESCE(source_id,''), kind, severity, status, message, evidence_json, metadata_json, created_at, updated_at
		FROM knowledge_lint_findings
		WHERE ($1 = '' OR scope_kind = $1)
		  AND ($2 = '' OR scope_id = $2)
		  AND ($3 = '' OR proposal_id = $3)
		  AND ($4 = '' OR page_id = $4)
		  AND ($5 = '' OR kind = $5)
		  AND ($6 = '' OR severity = $6)
		  AND ($7 = '' OR status = $7)
		ORDER BY created_at DESC
		LIMIT NULLIF($8, 0)
	`, query.ScopeKind, query.ScopeID, query.ProposalID, query.PageID, query.Kind, query.Severity, query.Status, query.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []knowledge.LintFinding
	for rows.Next() {
		var item knowledge.LintFinding
		var evidence, metadata []byte
		if err := rows.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.ProposalID, &item.PageID, &item.SourceID, &item.Kind, &item.Severity, &item.Status, &item.Message, &evidence, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &item.Evidence)
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveMediaAsset(ctx context.Context, asset media.Asset) error {
	metadata := metadataJSON(asset.Metadata, asset.ArtifactMeta)
	_, err := c.sessionQuery().Exec(ctx, `
		INSERT INTO media_assets (id, session_id, event_id, part_index, type, url, mime_type, checksum, status, retention, metadata_json, created_at, enriched_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    metadata_json = EXCLUDED.metadata_json,
		    enriched_at = EXCLUDED.enriched_at
	`, asset.ID, asset.SessionID, asset.EventID, asset.PartIndex, asset.Type, nullString(asset.URL), nullString(asset.MimeType), nullString(asset.Checksum), asset.Status, nullString(asset.Retention), metadata, asset.CreatedAt, nullTime(asset.EnrichedAt))
	return err
}

func (c *Client) ListMediaAssets(ctx context.Context, sessionID string) ([]media.Asset, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, session_id, event_id, part_index, type, COALESCE(url,''), COALESCE(mime_type,''), COALESCE(checksum,''), status, COALESCE(retention,''), metadata_json, created_at, COALESCE(enriched_at, '0001-01-01'::timestamptz)
		FROM media_assets
		WHERE ($1 = '' OR session_id = $1)
		ORDER BY created_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []media.Asset
	for rows.Next() {
		var item media.Asset
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.SessionID, &item.EventID, &item.PartIndex, &item.Type, &item.URL, &item.MimeType, &item.Checksum, &item.Status, &item.Retention, &metadata, &item.CreatedAt, &item.EnrichedAt); err != nil {
			return nil, err
		}
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveDerivedSignal(ctx context.Context, signal media.DerivedSignal) error {
	metadata := metadataJSON(signal.Metadata, signal.ArtifactMeta)
	_, err := c.sessionQuery().Exec(ctx, `
		INSERT INTO derived_signals (id, asset_id, session_id, event_id, kind, value, confidence, metadata_json, extractor, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET kind = EXCLUDED.kind,
		    value = EXCLUDED.value,
		    confidence = EXCLUDED.confidence,
		    metadata_json = EXCLUDED.metadata_json,
		    extractor = EXCLUDED.extractor
	`, signal.ID, signal.AssetID, signal.SessionID, signal.EventID, signal.Kind, signal.Value, signal.Confidence, metadata, signal.Extractor, signal.CreatedAt)
	return err
}

func (c *Client) ListDerivedSignals(ctx context.Context, sessionID string) ([]media.DerivedSignal, error) {
	rows, err := c.sessionQuery().Query(ctx, `
		SELECT id, asset_id, session_id, event_id, kind, value, confidence, metadata_json, extractor, created_at
		FROM derived_signals
		WHERE ($1 = '' OR session_id = $1)
		ORDER BY created_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []media.DerivedSignal
	for rows.Next() {
		var item media.DerivedSignal
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.AssetID, &item.SessionID, &item.EventID, &item.Kind, &item.Value, &item.Confidence, &metadata, &item.Extractor, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.ArtifactMeta, item.Metadata, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) UpsertConversationBinding(ctx context.Context, binding gatewaydomain.ConversationBinding) error {
	raw, err := json.Marshal(binding.CapabilityProfile)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO conversation_bindings (id, channel, external_conversation_id, external_user_id, session_id, capability_profile, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE
		SET external_user_id = EXCLUDED.external_user_id,
		    session_id = EXCLUDED.session_id,
		    capability_profile = EXCLUDED.capability_profile,
		    updated_at = EXCLUDED.updated_at
	`, binding.ID, binding.Channel, binding.ExternalConversationID, nullString(binding.ExternalUserID), binding.SessionID, raw, binding.CreatedAt, binding.UpdatedAt)
	return err
}

func (c *Client) GetConversationBinding(ctx context.Context, channel string, externalConversationID string) (gatewaydomain.ConversationBinding, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, channel, external_conversation_id, COALESCE(external_user_id,''), session_id, capability_profile, created_at, updated_at
		FROM conversation_bindings
		WHERE channel = $1 AND external_conversation_id = $2
	`, channel, externalConversationID)
	var out gatewaydomain.ConversationBinding
	var raw []byte
	if err := row.Scan(&out.ID, &out.Channel, &out.ExternalConversationID, &out.ExternalUserID, &out.SessionID, &raw, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gatewaydomain.ConversationBinding{}, errors.New("conversation binding not found")
		}
		return gatewaydomain.ConversationBinding{}, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.CapabilityProfile); err != nil {
			return gatewaydomain.ConversationBinding{}, err
		}
	}
	return out, nil
}

func (c *Client) ListConversationBindings(ctx context.Context) ([]gatewaydomain.ConversationBinding, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, channel, external_conversation_id, COALESCE(external_user_id,''), session_id, capability_profile, created_at, updated_at
		FROM conversation_bindings ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gatewaydomain.ConversationBinding
	for rows.Next() {
		var item gatewaydomain.ConversationBinding
		var raw []byte
		if err := rows.Scan(&item.ID, &item.Channel, &item.ExternalConversationID, &item.ExternalUserID, &item.SessionID, &raw, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &item.CapabilityProfile); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) CreateExecution(ctx context.Context, exec execution.TurnExecution, steps []execution.ExecutionStep) error {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO turn_executions (
			id, session_id, trigger_event_id, trigger_event_ids, policy_bundle_id, proposal_id, rollout_id, selection_reason, trace_id, status, lease_owner, lease_expires_at, blocked_reason, resume_signal, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (id) DO NOTHING
	`, exec.ID, exec.SessionID, exec.TriggerEventID, mustJSONValue(triggerEventIDs(exec)), nullString(exec.PolicyBundleID), nullString(exec.ProposalID), nullString(exec.RolloutID), nullString(exec.SelectionReason), nullString(exec.TraceID), exec.Status, nullString(exec.LeaseOwner), nullTime(exec.LeaseExpiresAt), nullString(exec.BlockedReason), nullString(exec.ResumeSignal), exec.CreatedAt, exec.UpdatedAt)
	if err != nil {
		return err
	}
	for _, step := range steps {
		_, err = tx.Exec(ctx, `
			INSERT INTO execution_steps (
				id, execution_id, name, status, attempt, recomputable, lease_owner, lease_expires_at, idempotency_key, last_error, next_attempt_at, max_attempts, max_elapsed_seconds, backoff_seconds, retry_reason, blocked_reason, resume_signal, started_at, finished_at, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
			ON CONFLICT (id) DO NOTHING
		`, step.ID, step.ExecutionID, step.Name, step.Status, step.Attempt, step.Recomputable, nullString(step.LeaseOwner), nullTime(step.LeaseExpiresAt), step.IdempotencyKey, nullString(step.LastError), nullTime(step.NextAttemptAt), stepMaxAttemptsForStore(step), step.MaxElapsedSeconds, stepBackoffSecondsForStore(step), nullString(step.RetryReason), nullString(step.BlockedReason), nullString(step.ResumeSignal), nullTime(step.StartedAt), nullTime(step.FinishedAt), step.CreatedAt, step.UpdatedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (c *Client) CreateOrCoalesceExecution(ctx context.Context, exec execution.TurnExecution, steps []execution.ExecutionStep, triggerEventID string, coalesceUntil time.Time) (execution.TurnExecution, []execution.ExecutionStep, bool, error) {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return execution.TurnExecution{}, nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		SELECT id, session_id, trigger_event_id, trigger_event_ids, COALESCE(policy_bundle_id,''), COALESCE(proposal_id,''), COALESCE(rollout_id,''), COALESCE(selection_reason,''), COALESCE(trace_id,''), status, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(blocked_reason,''), COALESCE(resume_signal,''), created_at, updated_at, metadata_json
		FROM turn_executions
		WHERE session_id = $1
		  AND status IN ('pending','waiting')
		  AND COALESCE(blocked_reason, '') = ''
		  AND NOT EXISTS (
			SELECT 1 FROM session_events
			WHERE execution_id = turn_executions.id
			  AND source = 'ai_agent'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM execution_steps
			WHERE execution_id = turn_executions.id
			  AND name = 'compose_response'
			  AND status IN ('running','succeeded','blocked','failed','abandoned')
		  )
		ORDER BY created_at DESC
		LIMIT 1
		FOR UPDATE
	`, exec.SessionID)
	var existing execution.TurnExecution
	var raw []byte
	var metadata []byte
	if err := row.Scan(&existing.ID, &existing.SessionID, &existing.TriggerEventID, &raw, &existing.PolicyBundleID, &existing.ProposalID, &existing.RolloutID, &existing.SelectionReason, &existing.TraceID, &existing.Status, &existing.LeaseOwner, &existing.LeaseExpiresAt, &existing.BlockedReason, &existing.ResumeSignal, &existing.CreatedAt, &existing.UpdatedAt, &metadata); err == nil {
		_ = json.Unmarshal(raw, &existing.TriggerEventIDs)
		existing.ArtifactMeta, _, _ = decodeMetadata(metadata)
		applyExecutionMetadata(metadata, &existing)
		existing.TriggerEventIDs = appendUniqueString(triggerEventIDs(existing), triggerEventID)
		existing.LeaseExpiresAt = coalesceUntil
		if existing.Status != execution.StatusRunning {
			existing.Status = execution.StatusWaiting
		}
		existing.UpdatedAt = exec.UpdatedAt
		_, err = tx.Exec(ctx, `
			UPDATE turn_executions
			SET trigger_event_ids = $2, lease_owner = '', lease_expires_at = $3, status = $4, updated_at = $5
			WHERE id = $1
		`, existing.ID, mustJSONValue(existing.TriggerEventIDs), nullTime(existing.LeaseExpiresAt), existing.Status, existing.UpdatedAt)
		if err != nil {
			return execution.TurnExecution{}, nil, false, err
		}
		_, err = tx.Exec(ctx, `
			UPDATE execution_steps
			SET status = 'waiting', next_attempt_at = $2, lease_owner = '', lease_expires_at = $2, updated_at = $3
			WHERE execution_id = $1
			  AND status IN ('pending','waiting')
			  AND name = (
				SELECT name FROM execution_steps
				WHERE execution_id = $1 AND status IN ('pending','waiting')
				ORDER BY created_at ASC LIMIT 1
			  )
		`, existing.ID, nullTime(coalesceUntil), existing.UpdatedAt)
		if err != nil {
			return execution.TurnExecution{}, nil, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return execution.TurnExecution{}, nil, false, err
		}
		loaded, loadedSteps, err := c.GetExecution(ctx, existing.ID)
		return loaded, loadedSteps, true, err
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return execution.TurnExecution{}, nil, false, err
	}

	if len(exec.TriggerEventIDs) == 0 {
		exec.TriggerEventIDs = []string{triggerEventID}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO turn_executions (
			id, session_id, trigger_event_id, trigger_event_ids, policy_bundle_id, proposal_id, rollout_id, selection_reason, trace_id, status, lease_owner, lease_expires_at, blocked_reason, resume_signal, created_at, updated_at, metadata_json
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
	`, exec.ID, exec.SessionID, exec.TriggerEventID, mustJSONValue(triggerEventIDs(exec)), nullString(exec.PolicyBundleID), nullString(exec.ProposalID), nullString(exec.RolloutID), nullString(exec.SelectionReason), nullString(exec.TraceID), exec.Status, nullString(exec.LeaseOwner), nullTime(exec.LeaseExpiresAt), nullString(exec.BlockedReason), nullString(exec.ResumeSignal), exec.CreatedAt, exec.UpdatedAt, metadataJSON(executionMetadata(exec), exec.ArtifactMeta))
	if err != nil {
		return execution.TurnExecution{}, nil, false, err
	}
	for _, step := range steps {
		_, err = tx.Exec(ctx, `
			INSERT INTO execution_steps (
				id, execution_id, name, status, attempt, recomputable, lease_owner, lease_expires_at, idempotency_key, last_error, next_attempt_at, max_attempts, max_elapsed_seconds, backoff_seconds, retry_reason, blocked_reason, resume_signal, started_at, finished_at, created_at, updated_at, metadata_json
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		`, step.ID, step.ExecutionID, step.Name, step.Status, step.Attempt, step.Recomputable, nullString(step.LeaseOwner), nullTime(step.LeaseExpiresAt), step.IdempotencyKey, nullString(step.LastError), nullTime(step.NextAttemptAt), stepMaxAttemptsForStore(step), step.MaxElapsedSeconds, stepBackoffSecondsForStore(step), nullString(step.RetryReason), nullString(step.BlockedReason), nullString(step.ResumeSignal), nullTime(step.StartedAt), nullTime(step.FinishedAt), step.CreatedAt, step.UpdatedAt, metadataJSON(nil, step.ArtifactMeta))
		if err != nil {
			return execution.TurnExecution{}, nil, false, err
		}
	}
	return exec, steps, false, tx.Commit(ctx)
}

func (c *Client) ListExecutions(ctx context.Context) ([]execution.TurnExecution, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, trigger_event_id, trigger_event_ids, COALESCE(policy_bundle_id,''), COALESCE(proposal_id,''), COALESCE(rollout_id,''), COALESCE(selection_reason,''), COALESCE(trace_id,''), status, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(blocked_reason,''), COALESCE(resume_signal,''), created_at, updated_at, metadata_json
		FROM turn_executions
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []execution.TurnExecution
	for rows.Next() {
		var exec execution.TurnExecution
		var raw []byte
		var metadata []byte
		if err := rows.Scan(&exec.ID, &exec.SessionID, &exec.TriggerEventID, &raw, &exec.PolicyBundleID, &exec.ProposalID, &exec.RolloutID, &exec.SelectionReason, &exec.TraceID, &exec.Status, &exec.LeaseOwner, &exec.LeaseExpiresAt, &exec.BlockedReason, &exec.ResumeSignal, &exec.CreatedAt, &exec.UpdatedAt, &metadata); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &exec.TriggerEventIDs)
		exec.ArtifactMeta, _, _ = decodeMetadata(metadata)
		applyExecutionMetadata(metadata, &exec)
		out = append(out, exec)
	}
	return out, rows.Err()
}

func (c *Client) GetExecution(ctx context.Context, executionID string) (execution.TurnExecution, []execution.ExecutionStep, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, session_id, trigger_event_id, trigger_event_ids, COALESCE(policy_bundle_id,''), COALESCE(proposal_id,''), COALESCE(rollout_id,''), COALESCE(selection_reason,''), COALESCE(trace_id,''), status, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(blocked_reason,''), COALESCE(resume_signal,''), created_at, updated_at, metadata_json
		FROM turn_executions
		WHERE id = $1
	`, executionID)
	var exec execution.TurnExecution
	var raw []byte
	var metadata []byte
	if err := row.Scan(&exec.ID, &exec.SessionID, &exec.TriggerEventID, &raw, &exec.PolicyBundleID, &exec.ProposalID, &exec.RolloutID, &exec.SelectionReason, &exec.TraceID, &exec.Status, &exec.LeaseOwner, &exec.LeaseExpiresAt, &exec.BlockedReason, &exec.ResumeSignal, &exec.CreatedAt, &exec.UpdatedAt, &metadata); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return execution.TurnExecution{}, nil, errors.New("execution not found")
		}
		return execution.TurnExecution{}, nil, err
	}
	_ = json.Unmarshal(raw, &exec.TriggerEventIDs)
	exec.ArtifactMeta, _, _ = decodeMetadata(metadata)
	applyExecutionMetadata(metadata, &exec)

	rows, err := c.Pool.Query(ctx, `
		SELECT id, execution_id, name, status, attempt, recomputable, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), idempotency_key, COALESCE(last_error,''), COALESCE(next_attempt_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(max_attempts, 0), COALESCE(max_elapsed_seconds, 0), COALESCE(backoff_seconds, 0), COALESCE(retry_reason,''), COALESCE(blocked_reason,''), COALESCE(resume_signal,''), COALESCE(started_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(finished_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at, metadata_json
		FROM execution_steps
		WHERE execution_id = $1
		ORDER BY created_at ASC
	`, executionID)
	if err != nil {
		return execution.TurnExecution{}, nil, err
	}
	defer rows.Close()
	var steps []execution.ExecutionStep
	for rows.Next() {
		var step execution.ExecutionStep
		var metadata []byte
		if err := rows.Scan(&step.ID, &step.ExecutionID, &step.Name, &step.Status, &step.Attempt, &step.Recomputable, &step.LeaseOwner, &step.LeaseExpiresAt, &step.IdempotencyKey, &step.LastError, &step.NextAttemptAt, &step.MaxAttempts, &step.MaxElapsedSeconds, &step.BackoffSeconds, &step.RetryReason, &step.BlockedReason, &step.ResumeSignal, &step.StartedAt, &step.FinishedAt, &step.CreatedAt, &step.UpdatedAt, &metadata); err != nil {
			return execution.TurnExecution{}, nil, err
		}
		step.ArtifactMeta, _, _ = decodeMetadata(metadata)
		steps = append(steps, step)
	}
	return exec, steps, rows.Err()
}

func (c *Client) UpdateExecution(ctx context.Context, exec execution.TurnExecution) error {
	_, err := c.Pool.Exec(ctx, `
		UPDATE turn_executions
		SET session_id = $2,
		    trigger_event_id = $3,
		    trigger_event_ids = $4,
		    policy_bundle_id = $5,
		    proposal_id = $6,
		    rollout_id = $7,
		    selection_reason = $8,
		    trace_id = $9,
		    status = $10,
		    lease_owner = $11,
		    lease_expires_at = $12,
		    blocked_reason = $13,
		    resume_signal = $14,
		    updated_at = $15,
		    metadata_json = $16
		WHERE id = $1
	`, exec.ID, exec.SessionID, exec.TriggerEventID, mustJSONValue(triggerEventIDs(exec)), nullString(exec.PolicyBundleID), nullString(exec.ProposalID), nullString(exec.RolloutID), nullString(exec.SelectionReason), nullString(exec.TraceID), exec.Status, nullString(exec.LeaseOwner), nullTime(exec.LeaseExpiresAt), nullString(exec.BlockedReason), nullString(exec.ResumeSignal), exec.UpdatedAt, metadataJSON(executionMetadata(exec), exec.ArtifactMeta))
	return err
}

func (c *Client) UpdateExecutionStep(ctx context.Context, step execution.ExecutionStep) error {
	_, err := c.Pool.Exec(ctx, `
		UPDATE execution_steps
		SET status = $2,
		    attempt = $3,
		    recomputable = $4,
		    lease_owner = $5,
		    lease_expires_at = $6,
		    idempotency_key = $7,
		    last_error = $8,
		    next_attempt_at = $9,
		    max_attempts = $10,
		    max_elapsed_seconds = $11,
		    backoff_seconds = $12,
		    retry_reason = $13,
		    blocked_reason = $14,
		    resume_signal = $15,
		    started_at = $16,
		    finished_at = $17,
		    updated_at = $18,
		    metadata_json = $19
		WHERE id = $1
	`, step.ID, step.Status, step.Attempt, step.Recomputable, nullString(step.LeaseOwner), nullTime(step.LeaseExpiresAt), step.IdempotencyKey, nullString(step.LastError), nullTime(step.NextAttemptAt), stepMaxAttemptsForStore(step), step.MaxElapsedSeconds, stepBackoffSecondsForStore(step), nullString(step.RetryReason), nullString(step.BlockedReason), nullString(step.ResumeSignal), nullTime(step.StartedAt), nullTime(step.FinishedAt), step.UpdatedAt, metadataJSON(nil, step.ArtifactMeta))
	return err
}

func (c *Client) ListRunnableExecutions(ctx context.Context, now time.Time) ([]execution.TurnExecution, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, trigger_event_id, trigger_event_ids, COALESCE(policy_bundle_id,''), COALESCE(proposal_id,''), COALESCE(rollout_id,''), COALESCE(selection_reason,''), COALESCE(trace_id,''), status, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(blocked_reason,''), COALESCE(resume_signal,''), created_at, updated_at, metadata_json
		FROM turn_executions
		WHERE (
			(status IN ('pending', 'running') AND (lease_expires_at IS NULL OR lease_expires_at < $1 OR lease_owner = ''))
			OR (status = 'waiting' AND (lease_expires_at IS NULL OR lease_expires_at < $1))
		  )
		ORDER BY created_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []execution.TurnExecution
	for rows.Next() {
		var exec execution.TurnExecution
		var raw []byte
		var metadata []byte
		if err := rows.Scan(&exec.ID, &exec.SessionID, &exec.TriggerEventID, &raw, &exec.PolicyBundleID, &exec.ProposalID, &exec.RolloutID, &exec.SelectionReason, &exec.TraceID, &exec.Status, &exec.LeaseOwner, &exec.LeaseExpiresAt, &exec.BlockedReason, &exec.ResumeSignal, &exec.CreatedAt, &exec.UpdatedAt, &metadata); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &exec.TriggerEventIDs)
		exec.ArtifactMeta, _, _ = decodeMetadata(metadata)
		applyExecutionMetadata(metadata, &exec)
		out = append(out, exec)
	}
	return out, rows.Err()
}

func (c *Client) UpsertJourneyInstance(ctx context.Context, instance journey.Instance) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO journey_instances (id, session_id, journey_id, state_id, path, status, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (id) DO UPDATE
		SET state_id = EXCLUDED.state_id,
		    path = EXCLUDED.path,
		    status = EXCLUDED.status,
		    updated_at = EXCLUDED.updated_at
	`, instance.ID, instance.SessionID, instance.JourneyID, instance.StateID, instance.Path, instance.Status, instance.UpdatedAt)
	return err
}

func (c *Client) ListJourneyInstances(ctx context.Context, sessionID string) ([]journey.Instance, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, journey_id, state_id, path, status, updated_at
		FROM journey_instances
		WHERE session_id = $1
		ORDER BY updated_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []journey.Instance
	for rows.Next() {
		var item journey.Instance
		if err := rows.Scan(&item.ID, &item.SessionID, &item.JourneyID, &item.StateID, &item.Path, &item.Status, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) RegisterProvider(ctx context.Context, binding tool.ProviderBinding) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO tool_provider_bindings (id, kind, name, uri, healthy, registered_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE
		SET kind = EXCLUDED.kind,
		    name = EXCLUDED.name,
		    uri = EXCLUDED.uri,
		    healthy = EXCLUDED.healthy,
		    registered_at = EXCLUDED.registered_at
	`, binding.ID, binding.Kind, binding.Name, binding.URI, binding.Healthy, binding.RegisteredAt)
	return err
}

func (c *Client) GetProvider(ctx context.Context, providerID string) (tool.ProviderBinding, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, kind, name, uri, registered_at, healthy
		FROM tool_provider_bindings
		WHERE id = $1
	`, providerID)
	var out tool.ProviderBinding
	if err := row.Scan(&out.ID, &out.Kind, &out.Name, &out.URI, &out.RegisteredAt, &out.Healthy); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return tool.ProviderBinding{}, errors.New("provider not found")
		}
		return tool.ProviderBinding{}, err
	}
	return out, nil
}

func (c *Client) ListProviders(ctx context.Context) ([]tool.ProviderBinding, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, kind, name, uri, registered_at, healthy
		FROM tool_provider_bindings
		ORDER BY registered_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tool.ProviderBinding
	for rows.Next() {
		var item tool.ProviderBinding
		if err := rows.Scan(&item.ID, &item.Kind, &item.Name, &item.URI, &item.RegisteredAt, &item.Healthy); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveProviderAuthBinding(ctx context.Context, binding tool.AuthBinding) error {
	if c.Crypter == nil {
		return errors.New("provider auth encryption unavailable")
	}
	ciphertext, err := c.Crypter.Encrypt(binding.Secret)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO tool_auth_bindings (provider_id, auth_type, header_name, secret_ciphertext, updated_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (provider_id) DO UPDATE
		SET auth_type = EXCLUDED.auth_type,
		    header_name = EXCLUDED.header_name,
		    secret_ciphertext = EXCLUDED.secret_ciphertext,
		    updated_at = EXCLUDED.updated_at
	`, binding.ProviderID, binding.Type, nullString(binding.HeaderName), ciphertext, binding.UpdatedAt)
	return err
}

func (c *Client) GetProviderAuthBinding(ctx context.Context, providerID string) (tool.AuthBinding, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT provider_id, auth_type, COALESCE(header_name,''), secret_ciphertext, updated_at
		FROM tool_auth_bindings
		WHERE provider_id = $1
	`, providerID)
	var out tool.AuthBinding
	var ciphertext string
	if err := row.Scan(&out.ProviderID, &out.Type, &out.HeaderName, &ciphertext, &out.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return tool.AuthBinding{}, errors.New("provider auth binding not found")
		}
		return tool.AuthBinding{}, err
	}
	if c.Crypter == nil {
		return tool.AuthBinding{}, errors.New("provider auth decryption unavailable")
	}
	secret, err := c.Crypter.Decrypt(ciphertext)
	if err != nil {
		return tool.AuthBinding{}, err
	}
	out.Secret = secret
	return out, nil
}

func (c *Client) SaveCatalogEntries(ctx context.Context, entries []tool.CatalogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `DELETE FROM tool_catalog WHERE provider_id = $1`, entries[0].ProviderID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var schemaJSON any
		if json.Valid([]byte(entry.Schema)) {
			schemaJSON = []byte(entry.Schema)
		} else {
			schemaJSON = []byte(`{"raw":` + mustJSONString(entry.Schema) + `}`)
		}
		_, err = tx.Exec(ctx, `
		INSERT INTO tool_catalog (id, provider_id, name, description, schema_json, runtime_protocol, metadata_json, imported_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, entry.ID, entry.ProviderID, entry.Name, entry.Description, schemaJSON, entry.RuntimeProtocol, mustJSON(entry.MetadataJSON), entry.ImportedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (c *Client) ListCatalogEntries(ctx context.Context) ([]tool.CatalogEntry, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, provider_id, name, description, schema_json::text, runtime_protocol, metadata_json::text, imported_at
		FROM tool_catalog
		ORDER BY imported_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tool.CatalogEntry
	for rows.Next() {
		var entry tool.CatalogEntry
		if err := rows.Scan(&entry.ID, &entry.ProviderID, &entry.Name, &entry.Description, &entry.Schema, &entry.RuntimeProtocol, &entry.MetadataJSON, &entry.ImportedAt); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (c *Client) AppendAuditRecord(ctx context.Context, record audit.Record) error {
	fields, err := json.Marshal(record.Fields)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO audit_log (id, kind, session_id, execution_id, trace_id, message, fields, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO NOTHING
	`, record.ID, record.Kind, nullString(record.SessionID), nullString(record.ExecutionID), nullString(record.TraceID), record.Message, fields, record.CreatedAt)
	return err
}

func (c *Client) ListAuditRecords(ctx context.Context) ([]audit.Record, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, kind, COALESCE(session_id,''), COALESCE(execution_id,''), COALESCE(trace_id,''), message, fields, created_at
		FROM audit_log
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.Record
	for rows.Next() {
		var record audit.Record
		var fields []byte
		if err := rows.Scan(&record.ID, &record.Kind, &record.SessionID, &record.ExecutionID, &record.TraceID, &record.Message, &fields, &record.CreatedAt); err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			if err := json.Unmarshal(fields, &record.Fields); err != nil {
				return nil, err
			}
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (c *Client) SaveResponse(ctx context.Context, record responsedomain.Response) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO responses (
			id, session_id, execution_id, trace_id, trigger_event_ids, trigger_source, trigger_reason, dedupe_key, status, reason, iteration_count, max_iterations,
			stability_reached, generation_mode, preamble_event_id, message_event_ids, tool_insights, glossary_terms,
			started_at, completed_at, canceled_at, created_at, updated_at, metadata_json
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
		ON CONFLICT (id) DO UPDATE
		SET session_id = EXCLUDED.session_id,
		    execution_id = EXCLUDED.execution_id,
		    trace_id = EXCLUDED.trace_id,
		    trigger_event_ids = EXCLUDED.trigger_event_ids,
		    trigger_source = EXCLUDED.trigger_source,
		    trigger_reason = EXCLUDED.trigger_reason,
		    dedupe_key = EXCLUDED.dedupe_key,
		    status = EXCLUDED.status,
		    reason = EXCLUDED.reason,
		    iteration_count = EXCLUDED.iteration_count,
		    max_iterations = EXCLUDED.max_iterations,
		    stability_reached = EXCLUDED.stability_reached,
		    generation_mode = EXCLUDED.generation_mode,
		    preamble_event_id = EXCLUDED.preamble_event_id,
		    message_event_ids = EXCLUDED.message_event_ids,
		    tool_insights = EXCLUDED.tool_insights,
		    glossary_terms = EXCLUDED.glossary_terms,
		    started_at = EXCLUDED.started_at,
		    completed_at = EXCLUDED.completed_at,
		    canceled_at = EXCLUDED.canceled_at,
		    updated_at = EXCLUDED.updated_at,
		    metadata_json = EXCLUDED.metadata_json
	`, record.ID, record.SessionID, record.ExecutionID, nullString(record.TraceID), mustJSONValue(record.TriggerEventIDs), nullString(record.TriggerSource), nullString(record.TriggerReason), nullString(record.DedupeKey), record.Status, nullString(record.Reason), record.IterationCount, record.MaxIterations, record.StabilityReached, nullString(record.GenerationMode), nullString(record.PreambleEventID), mustJSONValue(record.MessageEventIDs), mustJSONValue(record.ToolInsights), mustJSONValue(record.GlossaryTerms), nullTime(record.StartedAt), nullTime(record.CompletedAt), nullTime(record.CanceledAt), record.CreatedAt, record.UpdatedAt, metadataJSON(responseMetadata(record), record.ArtifactMeta))
	return err
}

func (c *Client) GetResponse(ctx context.Context, responseID string) (responsedomain.Response, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, session_id, execution_id, COALESCE(trace_id,''), trigger_event_ids, status, COALESCE(reason,''), iteration_count,
		       COALESCE(trigger_source,''), COALESCE(trigger_reason,''), COALESCE(dedupe_key,''), max_iterations, stability_reached, COALESCE(generation_mode,''), COALESCE(preamble_event_id,''), message_event_ids,
		       tool_insights, glossary_terms, COALESCE(started_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		       COALESCE(completed_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		       COALESCE(canceled_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at, metadata_json
		FROM responses WHERE id = $1
	`, responseID)
	var record responsedomain.Response
	var triggerIDs, messageIDs, toolInsights, glossary, metadata []byte
	if err := row.Scan(&record.ID, &record.SessionID, &record.ExecutionID, &record.TraceID, &triggerIDs, &record.Status, &record.Reason, &record.IterationCount, &record.TriggerSource, &record.TriggerReason, &record.DedupeKey, &record.MaxIterations, &record.StabilityReached, &record.GenerationMode, &record.PreambleEventID, &messageIDs, &toolInsights, &glossary, &record.StartedAt, &record.CompletedAt, &record.CanceledAt, &record.CreatedAt, &record.UpdatedAt, &metadata); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return responsedomain.Response{}, errors.New("response not found")
		}
		return responsedomain.Response{}, err
	}
	_ = json.Unmarshal(triggerIDs, &record.TriggerEventIDs)
	_ = json.Unmarshal(messageIDs, &record.MessageEventIDs)
	_ = json.Unmarshal(toolInsights, &record.ToolInsights)
	_ = json.Unmarshal(glossary, &record.GlossaryTerms)
	record.ArtifactMeta, _, _ = decodeMetadata(metadata)
	applyResponseMetadata(metadata, &record)
	return record, nil
}

func (c *Client) ListResponses(ctx context.Context, query responsedomain.Query) ([]responsedomain.Response, error) {
	sql := `
		SELECT id, session_id, execution_id, COALESCE(trace_id,''), trigger_event_ids, status, COALESCE(reason,''), iteration_count,
		       COALESCE(trigger_source,''), COALESCE(trigger_reason,''), COALESCE(dedupe_key,''), max_iterations, stability_reached, COALESCE(generation_mode,''), COALESCE(preamble_event_id,''), message_event_ids,
		       tool_insights, glossary_terms, COALESCE(started_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		       COALESCE(completed_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
		       COALESCE(canceled_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at, metadata_json
		FROM responses WHERE 1=1`
	var args []any
	idx := 1
	if query.SessionID != "" {
		sql += fmt.Sprintf(" AND session_id = $%d", idx)
		args = append(args, query.SessionID)
		idx++
	}
	if query.ExecutionID != "" {
		sql += fmt.Sprintf(" AND execution_id = $%d", idx)
		args = append(args, query.ExecutionID)
		idx++
	}
	if query.Status != "" {
		sql += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, query.Status)
		idx++
	}
	sql += " ORDER BY created_at DESC"
	if query.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT $%d", idx)
		args = append(args, query.Limit)
	}
	rows, err := c.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []responsedomain.Response
	for rows.Next() {
		var record responsedomain.Response
		var triggerIDs, messageIDs, toolInsights, glossary, metadata []byte
		if err := rows.Scan(&record.ID, &record.SessionID, &record.ExecutionID, &record.TraceID, &triggerIDs, &record.Status, &record.Reason, &record.IterationCount, &record.TriggerSource, &record.TriggerReason, &record.DedupeKey, &record.MaxIterations, &record.StabilityReached, &record.GenerationMode, &record.PreambleEventID, &messageIDs, &toolInsights, &glossary, &record.StartedAt, &record.CompletedAt, &record.CanceledAt, &record.CreatedAt, &record.UpdatedAt, &metadata); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(triggerIDs, &record.TriggerEventIDs)
		_ = json.Unmarshal(messageIDs, &record.MessageEventIDs)
		_ = json.Unmarshal(toolInsights, &record.ToolInsights)
		_ = json.Unmarshal(glossary, &record.GlossaryTerms)
		record.ArtifactMeta, _, _ = decodeMetadata(metadata)
		applyResponseMetadata(metadata, &record)
		out = append(out, record)
	}
	return out, rows.Err()
}

func (c *Client) SaveResponseTraceSpan(ctx context.Context, span responsedomain.TraceSpan) error {
	fields, err := json.Marshal(span.Fields)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO response_trace_spans (id, response_id, session_id, execution_id, trace_id, parent_id, kind, name, iteration, status, fields, started_at, finished_at, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE
		SET response_id = EXCLUDED.response_id,
		    session_id = EXCLUDED.session_id,
		    execution_id = EXCLUDED.execution_id,
		    trace_id = EXCLUDED.trace_id,
		    parent_id = EXCLUDED.parent_id,
		    kind = EXCLUDED.kind,
		    name = EXCLUDED.name,
		    iteration = EXCLUDED.iteration,
		    status = EXCLUDED.status,
		    fields = EXCLUDED.fields,
		    started_at = EXCLUDED.started_at,
		    finished_at = EXCLUDED.finished_at,
		    metadata_json = EXCLUDED.metadata_json
	`, span.ID, nullString(span.ResponseID), nullString(span.SessionID), nullString(span.ExecutionID), nullString(span.TraceID), nullString(span.ParentID), span.Kind, nullString(span.Name), span.Iteration, nullString(span.Status), fields, span.StartedAt, nullTime(span.FinishedAt), metadataJSON(nil, span.ArtifactMeta))
	return err
}

func (c *Client) ListResponseTraceSpans(ctx context.Context, query responsedomain.TraceSpanQuery) ([]responsedomain.TraceSpan, error) {
	sql := `
		SELECT id, COALESCE(response_id,''), COALESCE(session_id,''), COALESCE(execution_id,''), COALESCE(trace_id,''), COALESCE(parent_id,''),
		       kind, COALESCE(name,''), iteration, COALESCE(status,''), fields, started_at, COALESCE(finished_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), metadata_json
		FROM response_trace_spans WHERE 1=1`
	var args []any
	idx := 1
	if query.ResponseID != "" {
		sql += fmt.Sprintf(" AND response_id = $%d", idx)
		args = append(args, query.ResponseID)
		idx++
	}
	if query.SessionID != "" {
		sql += fmt.Sprintf(" AND session_id = $%d", idx)
		args = append(args, query.SessionID)
		idx++
	}
	if query.ExecutionID != "" {
		sql += fmt.Sprintf(" AND execution_id = $%d", idx)
		args = append(args, query.ExecutionID)
		idx++
	}
	if query.TraceID != "" {
		sql += fmt.Sprintf(" AND trace_id = $%d", idx)
		args = append(args, query.TraceID)
	}
	sql += " ORDER BY started_at ASC"
	rows, err := c.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []responsedomain.TraceSpan
	for rows.Next() {
		var span responsedomain.TraceSpan
		var fields, metadata []byte
		if err := rows.Scan(&span.ID, &span.ResponseID, &span.SessionID, &span.ExecutionID, &span.TraceID, &span.ParentID, &span.Kind, &span.Name, &span.Iteration, &span.Status, &fields, &span.StartedAt, &span.FinishedAt, &metadata); err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			if err := json.Unmarshal(fields, &span.Fields); err != nil {
				return nil, err
			}
		}
		span.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, span)
	}
	return out, rows.Err()
}

func (c *Client) SaveApprovalSession(ctx context.Context, session approval.Session) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO approval_sessions (id, session_id, execution_id, tool_id, status, request_text, decision, expires_at, created_at, updated_at, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    request_text = EXCLUDED.request_text,
		    decision = EXCLUDED.decision,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = EXCLUDED.updated_at,
		    metadata_json = EXCLUDED.metadata_json
	`, session.ID, session.SessionID, session.ExecutionID, session.ToolID, session.Status, session.RequestText, nullString(session.Decision), nullTime(session.ExpiresAt), session.CreatedAt, session.UpdatedAt, metadataJSON(nil, session.ArtifactMeta))
	return err
}

func (c *Client) GetApprovalSession(ctx context.Context, approvalID string) (approval.Session, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, session_id, execution_id, tool_id, status, request_text, COALESCE(decision,''), COALESCE(expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at, metadata_json
		FROM approval_sessions WHERE id = $1
	`, approvalID)
	var item approval.Session
	var metadata []byte
	if err := row.Scan(&item.ID, &item.SessionID, &item.ExecutionID, &item.ToolID, &item.Status, &item.RequestText, &item.Decision, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt, &metadata); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return approval.Session{}, errors.New("approval session not found")
		}
		return approval.Session{}, err
	}
	item.ArtifactMeta, _, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListApprovalSessions(ctx context.Context, sessionID string) ([]approval.Session, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, execution_id, tool_id, status, request_text, COALESCE(decision,''), COALESCE(expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at, metadata_json
		FROM approval_sessions WHERE session_id = $1 ORDER BY created_at DESC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []approval.Session
	for rows.Next() {
		var item approval.Session
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.SessionID, &item.ExecutionID, &item.ToolID, &item.Status, &item.RequestText, &item.Decision, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt, &metadata); err != nil {
			return nil, err
		}
		item.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveToolRun(ctx context.Context, run toolrun.Run) error {
	input, err := normalizeJSON(run.InputJSON)
	if err != nil {
		return err
	}
	output, err := normalizeJSON(run.OutputJSON)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO tool_runs (id, execution_id, tool_id, status, idempotency_key, input_json, output_json, created_at, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    input_json = EXCLUDED.input_json,
		    output_json = EXCLUDED.output_json,
		    metadata_json = EXCLUDED.metadata_json
	`, run.ID, run.ExecutionID, run.ToolID, run.Status, run.IdempotencyKey, input, output, run.CreatedAt, metadataJSON(nil, run.ArtifactMeta))
	return err
}

func (c *Client) ListToolRuns(ctx context.Context, executionID string) ([]toolrun.Run, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, execution_id, tool_id, status, idempotency_key, input_json::text, output_json::text, created_at, metadata_json
		FROM tool_runs WHERE execution_id = $1 ORDER BY created_at ASC
	`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []toolrun.Run
	for rows.Next() {
		var run toolrun.Run
		var metadata []byte
		if err := rows.Scan(&run.ID, &run.ExecutionID, &run.ToolID, &run.Status, &run.IdempotencyKey, &run.InputJSON, &run.OutputJSON, &run.CreatedAt, &metadata); err != nil {
			return nil, err
		}
		run.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, run)
	}
	return out, rows.Err()
}

func (c *Client) SaveDeliveryAttempt(ctx context.Context, attempt delivery.Attempt) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO delivery_attempts (id, session_id, execution_id, event_id, channel, status, idempotency_key, created_at, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    metadata_json = EXCLUDED.metadata_json
	`, attempt.ID, attempt.SessionID, attempt.ExecutionID, attempt.EventID, attempt.Channel, attempt.Status, attempt.IdempotencyKey, attempt.CreatedAt, metadataJSON(nil, attempt.ArtifactMeta))
	return err
}

func (c *Client) ListDeliveryAttempts(ctx context.Context, executionID string) ([]delivery.Attempt, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, execution_id, event_id, channel, status, idempotency_key, created_at, metadata_json
		FROM delivery_attempts WHERE execution_id = $1 ORDER BY created_at ASC
	`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []delivery.Attempt
	for rows.Next() {
		var attempt delivery.Attempt
		var metadata []byte
		if err := rows.Scan(&attempt.ID, &attempt.SessionID, &attempt.ExecutionID, &attempt.EventID, &attempt.Channel, &attempt.Status, &attempt.IdempotencyKey, &attempt.CreatedAt, &metadata); err != nil {
			return nil, err
		}
		attempt.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, attempt)
	}
	return out, rows.Err()
}

func (c *Client) CreateEvalRun(ctx context.Context, run replay.Run) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO eval_runs (id, type, source_execution_id, proposal_id, active_bundle_id, shadow_bundle_id, status, result_json, diff_json, last_error, created_at, updated_at, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO NOTHING
	`, run.ID, run.Type, run.SourceExecutionID, nullString(run.ProposalID), nullString(run.ActiveBundleID), nullString(run.ShadowBundleID), run.Status, mustJSON(run.ResultJSON), mustJSON(run.DiffJSON), nullString(run.LastError), run.CreatedAt, run.UpdatedAt, metadataJSON(nil, run.ArtifactMeta))
	return err
}

func (c *Client) UpdateEvalRun(ctx context.Context, run replay.Run) error {
	_, err := c.Pool.Exec(ctx, `
		UPDATE eval_runs
		SET proposal_id = $2,
		    active_bundle_id = $3,
		    shadow_bundle_id = $4,
		    status = $5,
		    result_json = $6,
		    diff_json = $7,
		    last_error = $8,
		    updated_at = $9,
		    metadata_json = $10
		WHERE id = $1
	`, run.ID, nullString(run.ProposalID), nullString(run.ActiveBundleID), nullString(run.ShadowBundleID), run.Status, mustJSON(run.ResultJSON), mustJSON(run.DiffJSON), nullString(run.LastError), run.UpdatedAt, metadataJSON(nil, run.ArtifactMeta))
	return err
}

func (c *Client) GetEvalRun(ctx context.Context, runID string) (replay.Run, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, type, source_execution_id, COALESCE(proposal_id,''), COALESCE(active_bundle_id,''), COALESCE(shadow_bundle_id,''), status, result_json::text, diff_json::text, COALESCE(last_error,''), created_at, updated_at, metadata_json
		FROM eval_runs WHERE id = $1
	`, runID)
	var run replay.Run
	var metadata []byte
	if err := row.Scan(&run.ID, &run.Type, &run.SourceExecutionID, &run.ProposalID, &run.ActiveBundleID, &run.ShadowBundleID, &run.Status, &run.ResultJSON, &run.DiffJSON, &run.LastError, &run.CreatedAt, &run.UpdatedAt, &metadata); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return replay.Run{}, errors.New("eval run not found")
		}
		return replay.Run{}, err
	}
	run.ArtifactMeta, _, _ = decodeMetadata(metadata)
	return run, nil
}

func (c *Client) ListEvalRuns(ctx context.Context) ([]replay.Run, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, type, source_execution_id, COALESCE(proposal_id,''), COALESCE(active_bundle_id,''), COALESCE(shadow_bundle_id,''), status, result_json::text, diff_json::text, COALESCE(last_error,''), created_at, updated_at, metadata_json
		FROM eval_runs ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []replay.Run
	for rows.Next() {
		var run replay.Run
		var metadata []byte
		if err := rows.Scan(&run.ID, &run.Type, &run.SourceExecutionID, &run.ProposalID, &run.ActiveBundleID, &run.ShadowBundleID, &run.Status, &run.ResultJSON, &run.DiffJSON, &run.LastError, &run.CreatedAt, &run.UpdatedAt, &metadata); err != nil {
			return nil, err
		}
		run.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, run)
	}
	return out, rows.Err()
}

func (c *Client) ListRunnableEvalRuns(ctx context.Context, now time.Time) ([]replay.Run, error) {
	_ = now
	rows, err := c.Pool.Query(ctx, `
		SELECT id, type, source_execution_id, COALESCE(proposal_id,''), COALESCE(active_bundle_id,''), COALESCE(shadow_bundle_id,''), status, result_json::text, diff_json::text, COALESCE(last_error,''), created_at, updated_at, metadata_json
		FROM eval_runs
		WHERE status IN ('pending', 'running')
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []replay.Run
	for rows.Next() {
		var run replay.Run
		var metadata []byte
		if err := rows.Scan(&run.ID, &run.Type, &run.SourceExecutionID, &run.ProposalID, &run.ActiveBundleID, &run.ShadowBundleID, &run.Status, &run.ResultJSON, &run.DiffJSON, &run.LastError, &run.CreatedAt, &run.UpdatedAt, &metadata); err != nil {
			return nil, err
		}
		run.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, run)
	}
	return out, rows.Err()
}

func (c *Client) SaveProposal(ctx context.Context, proposal rollout.Proposal) error {
	db := c.sessionQuery()
	evidence, err := json.Marshal(proposal.EvidenceRefs)
	if err != nil {
		return err
	}
	risks, err := json.Marshal(proposal.RiskFlags)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO policy_proposals (id, source_bundle_id, candidate_bundle_id, state, rationale, evidence_refs, replay_score, safety_score, risk_flags, requires_manual_approval, eval_summary_json, origin, created_at, updated_at, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE
		SET state = EXCLUDED.state,
		    rationale = EXCLUDED.rationale,
		    evidence_refs = EXCLUDED.evidence_refs,
		    replay_score = EXCLUDED.replay_score,
		    safety_score = EXCLUDED.safety_score,
		    risk_flags = EXCLUDED.risk_flags,
		    requires_manual_approval = EXCLUDED.requires_manual_approval,
		    eval_summary_json = EXCLUDED.eval_summary_json,
		    origin = EXCLUDED.origin,
		    updated_at = EXCLUDED.updated_at,
		    metadata_json = EXCLUDED.metadata_json
	`, proposal.ID, proposal.SourceBundleID, proposal.CandidateBundleID, proposal.State, proposal.Rationale, evidence, proposal.ReplayScore, proposal.SafetyScore, risks, proposal.RequiresManualApproval, mustJSON(proposal.EvalSummaryJSON), nullString(proposal.Origin), proposal.CreatedAt, proposal.UpdatedAt, metadataJSON(nil, proposal.ArtifactMeta))
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.RolloutProposal(proposal)
	if err := c.SavePolicyArtifacts(ctx, artifacts); err != nil {
		return err
	}
	return c.SavePolicyEdges(ctx, edges)
}

func (c *Client) GetProposal(ctx context.Context, proposalID string) (rollout.Proposal, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, source_bundle_id, candidate_bundle_id, state, rationale, evidence_refs, replay_score, safety_score, risk_flags, requires_manual_approval, eval_summary_json::text, COALESCE(origin,''), created_at, updated_at, metadata_json
		FROM policy_proposals WHERE id = $1
	`, proposalID)
	var item rollout.Proposal
	var evidence, risks []byte
	var metadata []byte
	if err := row.Scan(&item.ID, &item.SourceBundleID, &item.CandidateBundleID, &item.State, &item.Rationale, &evidence, &item.ReplayScore, &item.SafetyScore, &risks, &item.RequiresManualApproval, &item.EvalSummaryJSON, &item.Origin, &item.CreatedAt, &item.UpdatedAt, &metadata); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rollout.Proposal{}, errors.New("proposal not found")
		}
		return rollout.Proposal{}, err
	}
	_ = json.Unmarshal(evidence, &item.EvidenceRefs)
	_ = json.Unmarshal(risks, &item.RiskFlags)
	item.ArtifactMeta, _, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListProposals(ctx context.Context) ([]rollout.Proposal, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, source_bundle_id, candidate_bundle_id, state, rationale, evidence_refs, replay_score, safety_score, risk_flags, requires_manual_approval, eval_summary_json::text, COALESCE(origin,''), created_at, updated_at, metadata_json
		FROM policy_proposals ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rollout.Proposal
	for rows.Next() {
		var item rollout.Proposal
		var evidence, risks []byte
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.SourceBundleID, &item.CandidateBundleID, &item.State, &item.Rationale, &evidence, &item.ReplayScore, &item.SafetyScore, &risks, &item.RequiresManualApproval, &item.EvalSummaryJSON, &item.Origin, &item.CreatedAt, &item.UpdatedAt, &metadata); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &item.EvidenceRefs)
		_ = json.Unmarshal(risks, &item.RiskFlags)
		item.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveRollout(ctx context.Context, record rollout.Record) error {
	db := c.sessionQuery()
	includeRaw, err := json.Marshal(record.IncludeSessionIDs)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO policy_rollouts (id, proposal_id, status, channel, percentage, include_session_ids, previous_bundle_id, created_at, updated_at, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    percentage = EXCLUDED.percentage,
		    include_session_ids = EXCLUDED.include_session_ids,
		    previous_bundle_id = EXCLUDED.previous_bundle_id,
		    updated_at = EXCLUDED.updated_at,
		    metadata_json = EXCLUDED.metadata_json
	`, record.ID, record.ProposalID, record.Status, record.Channel, record.Percentage, includeRaw, nullString(record.PreviousBundleID), record.CreatedAt, record.UpdatedAt, metadataJSON(nil, record.ArtifactMeta))
	if err != nil {
		return err
	}
	artifacts, edges := controlgraph.RolloutRecord(record)
	if err := c.SavePolicyArtifacts(ctx, artifacts); err != nil {
		return err
	}
	return c.SavePolicyEdges(ctx, edges)
}

func (c *Client) GetRollout(ctx context.Context, rolloutID string) (rollout.Record, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, proposal_id, status, channel, percentage, include_session_ids, COALESCE(previous_bundle_id,''), created_at, updated_at, metadata_json
		FROM policy_rollouts WHERE id = $1
	`, rolloutID)
	var item rollout.Record
	var includeRaw []byte
	var metadata []byte
	if err := row.Scan(&item.ID, &item.ProposalID, &item.Status, &item.Channel, &item.Percentage, &includeRaw, &item.PreviousBundleID, &item.CreatedAt, &item.UpdatedAt, &metadata); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rollout.Record{}, errors.New("rollout not found")
		}
		return rollout.Record{}, err
	}
	_ = json.Unmarshal(includeRaw, &item.IncludeSessionIDs)
	item.ArtifactMeta, _, _ = decodeMetadata(metadata)
	return item, nil
}

func (c *Client) ListRollouts(ctx context.Context) ([]rollout.Record, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, proposal_id, status, channel, percentage, include_session_ids, COALESCE(previous_bundle_id,''), created_at, updated_at, metadata_json
		FROM policy_rollouts ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rollout.Record
	for rows.Next() {
		var item rollout.Record
		var includeRaw []byte
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.ProposalID, &item.Status, &item.Channel, &item.Percentage, &includeRaw, &item.PreviousBundleID, &item.CreatedAt, &item.UpdatedAt, &metadata); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(includeRaw, &item.IncludeSessionIDs)
		item.ArtifactMeta, _, _ = decodeMetadata(metadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullTime(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v
}

func stepMaxAttemptsForStore(step execution.ExecutionStep) int {
	if step.MaxAttempts > 0 {
		return step.MaxAttempts
	}
	return execution.DefaultRetryPolicy(step.Recomputable).MaxAttempts
}

func stepBackoffSecondsForStore(step execution.ExecutionStep) int {
	if step.BackoffSeconds > 0 {
		return step.BackoffSeconds
	}
	return execution.DefaultRetryPolicy(step.Recomputable).BackoffSeconds
}

func triggerEventIDs(exec execution.TurnExecution) []string {
	ids := append([]string(nil), exec.TriggerEventIDs...)
	if strings.TrimSpace(exec.TriggerEventID) != "" {
		ids = appendUniqueString(ids, exec.TriggerEventID)
	}
	return ids
}

func appendUniqueString(items []string, value string) []string {
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

func vectorLiteral(values []float32) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatFloat(float64(value), 'f', -1, 32))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func mustJSONString(v string) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func normalizeJSON(v string) ([]byte, error) {
	if v == "" {
		return []byte(`{}`), nil
	}
	if json.Valid([]byte(v)) {
		return []byte(v), nil
	}
	raw, err := json.Marshal(map[string]string{"raw": v})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func mustJSON(v string) []byte {
	raw, err := normalizeJSON(v)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

func mustJSONValue(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`null`)
	}
	return raw
}

func metadataJSON(metadata map[string]any, meta artifactmeta.Meta) []byte {
	return mustJSONValue(artifactmeta.Merge(metadata, meta))
}

func decodeMetadata(raw []byte) (artifactmeta.Meta, map[string]any, error) {
	if len(raw) == 0 {
		return artifactmeta.Meta{}, map[string]any{}, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return artifactmeta.Meta{}, nil, err
	}
	meta, metadata := artifactmeta.Extract(metadata)
	return meta, metadata, nil
}

func executionMetadata(exec execution.TurnExecution) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(exec.PolicySnapshotID) != "" {
		out[policySnapshotIDMetadataKey] = exec.PolicySnapshotID
	}
	if strings.TrimSpace(exec.RetryModelProfileID) != "" {
		out["retry_model_profile_id"] = exec.RetryModelProfileID
	}
	if !exec.RetryModelOverride.IsZero() {
		out["retry_model_override"] = map[string]any{
			"reasoning": map[string]any{
				"provider": exec.RetryModelOverride.Reasoning.Provider,
				"model":    exec.RetryModelOverride.Reasoning.Model,
			},
			"structured": map[string]any{
				"provider": exec.RetryModelOverride.Structured.Provider,
				"model":    exec.RetryModelOverride.Structured.Model,
			},
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyExecutionMetadata(raw []byte, exec *execution.TurnExecution) {
	if exec == nil {
		return
	}
	_, metadata, err := decodeMetadata(raw)
	if err != nil {
		return
	}
	exec.PolicySnapshotID = strings.TrimSpace(stringValue(metadata[policySnapshotIDMetadataKey]))
	exec.RetryModelProfileID = strings.TrimSpace(stringValue(metadata["retry_model_profile_id"]))
	if item, ok := metadata["retry_model_override"].(map[string]any); ok {
		if reasoning, ok := item["reasoning"].(map[string]any); ok {
			exec.RetryModelOverride.Reasoning.Provider = strings.TrimSpace(stringValue(reasoning["provider"]))
			exec.RetryModelOverride.Reasoning.Model = strings.TrimSpace(stringValue(reasoning["model"]))
		}
		if structured, ok := item["structured"].(map[string]any); ok {
			exec.RetryModelOverride.Structured.Provider = strings.TrimSpace(stringValue(structured["provider"]))
			exec.RetryModelOverride.Structured.Model = strings.TrimSpace(stringValue(structured["model"]))
		}
	}
}

func responseMetadata(record responsedomain.Response) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(record.PolicySnapshotID) != "" {
		out[policySnapshotIDMetadataKey] = record.PolicySnapshotID
	}
	if len(record.HeldMessageEventIDs) > 0 {
		out["held_message_event_ids"] = append([]string(nil), record.HeldMessageEventIDs...)
	}
	if strings.TrimSpace(record.ReviewDecision) != "" {
		out["review_decision"] = record.ReviewDecision
	}
	if strings.TrimSpace(record.ReviewedBy) != "" {
		out["reviewed_by"] = record.ReviewedBy
	}
	if !record.ReviewedAt.IsZero() {
		out["reviewed_at"] = record.ReviewedAt
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyResponseMetadata(raw []byte, record *responsedomain.Response) {
	if record == nil {
		return
	}
	_, metadata, err := decodeMetadata(raw)
	if err != nil {
		return
	}
	record.PolicySnapshotID = strings.TrimSpace(stringValue(metadata[policySnapshotIDMetadataKey]))
	record.HeldMessageEventIDs = stringSlice(metadata["held_message_event_ids"])
	record.ReviewDecision = strings.TrimSpace(stringValue(metadata["review_decision"]))
	record.ReviewedBy = strings.TrimSpace(stringValue(metadata["reviewed_by"]))
	if reviewedAt, ok := metadata["reviewed_at"].(string); ok {
		if ts, err := time.Parse(time.RFC3339Nano, reviewedAt); err == nil {
			record.ReviewedAt = ts
		}
	}
}

func watchMetadata(watch session.Watch) map[string]any {
	if strings.TrimSpace(watch.CapabilityID) == "" {
		return nil
	}
	return map[string]any{
		capabilityIDMetadataKey: watch.CapabilityID,
	}
}

func applyWatchMetadata(raw []byte, watch *session.Watch) {
	if watch == nil {
		return
	}
	_, metadata, err := decodeMetadata(raw)
	if err != nil {
		return
	}
	watch.CapabilityID = strings.TrimSpace(stringValue(metadata[capabilityIDMetadataKey]))
}

func stringValue(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func stringSlice(v any) []string {
	switch item := v.(type) {
	case []string:
		return append([]string(nil), item...)
	case []any:
		out := make([]string, 0, len(item))
		for _, value := range item {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
