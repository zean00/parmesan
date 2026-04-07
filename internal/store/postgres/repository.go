package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sahal/parmesan/internal/domain/approval"
	"github.com/sahal/parmesan/internal/domain/audit"
	"github.com/sahal/parmesan/internal/domain/delivery"
	"github.com/sahal/parmesan/internal/domain/execution"
	gatewaydomain "github.com/sahal/parmesan/internal/domain/gateway"
	"github.com/sahal/parmesan/internal/domain/journey"
	"github.com/sahal/parmesan/internal/domain/policy"
	"github.com/sahal/parmesan/internal/domain/replay"
	"github.com/sahal/parmesan/internal/domain/rollout"
	"github.com/sahal/parmesan/internal/domain/session"
	"github.com/sahal/parmesan/internal/domain/tool"
	"github.com/sahal/parmesan/internal/domain/toolrun"
)

func (c *Client) SaveBundle(ctx context.Context, bundle policy.Bundle) error {
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO policy_bundles (id, version, source_yaml, bundle_json, imported_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE
		SET version = EXCLUDED.version,
		    source_yaml = EXCLUDED.source_yaml,
		    bundle_json = EXCLUDED.bundle_json,
		    imported_at = EXCLUDED.imported_at
	`, bundle.ID, bundle.Version, bundle.SourceYAML, raw, bundle.ImportedAt)
	return err
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

func (c *Client) CreateSession(ctx context.Context, sess session.Session) error {
	metadata, err := json.Marshal(sess.Metadata)
	if err != nil {
		return err
	}
	labels, err := json.Marshal(sess.Labels)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO sessions (id, channel, customer_id, agent_id, mode, title, metadata_json, labels_json, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO NOTHING
	`, sess.ID, sess.Channel, nullString(sess.CustomerID), nullString(sess.AgentID), sess.Mode, nullString(sess.Title), metadata, labels, sess.CreatedAt)
	return err
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (session.Session, error) {
	row := c.Pool.QueryRow(ctx, `SELECT id, channel, COALESCE(customer_id,''), COALESCE(agent_id,''), COALESCE(mode,''), COALESCE(title,''), metadata_json, labels_json, created_at FROM sessions WHERE id = $1`, sessionID)
	var sess session.Session
	var metadata []byte
	var labels []byte
	if err := row.Scan(&sess.ID, &sess.Channel, &sess.CustomerID, &sess.AgentID, &sess.Mode, &sess.Title, &metadata, &labels, &sess.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return session.Session{}, errors.New("session not found")
		}
		return session.Session{}, err
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &sess.Metadata)
	}
	if len(labels) > 0 {
		_ = json.Unmarshal(labels, &sess.Labels)
	}
	return sess, nil
}

func (c *Client) UpdateSession(ctx context.Context, sess session.Session) error {
	metadata, err := json.Marshal(sess.Metadata)
	if err != nil {
		return err
	}
	labels, err := json.Marshal(sess.Labels)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		UPDATE sessions
		SET channel = $2,
		    customer_id = $3,
		    agent_id = $4,
		    mode = $5,
		    title = $6,
		    metadata_json = $7,
		    labels_json = $8
		WHERE id = $1
	`, sess.ID, sess.Channel, nullString(sess.CustomerID), nullString(sess.AgentID), sess.Mode, nullString(sess.Title), metadata, labels)
	return err
}

func (c *Client) ListSessions(ctx context.Context) ([]session.Session, error) {
	rows, err := c.Pool.Query(ctx, `SELECT id, channel, COALESCE(customer_id,''), COALESCE(agent_id,''), COALESCE(mode,''), COALESCE(title,''), metadata_json, labels_json, created_at FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []session.Session
	for rows.Next() {
		var sess session.Session
		var metadata []byte
		var labels []byte
		if err := rows.Scan(&sess.ID, &sess.Channel, &sess.CustomerID, &sess.AgentID, &sess.Mode, &sess.Title, &metadata, &labels, &sess.CreatedAt); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &sess.Metadata)
		}
		if len(labels) > 0 {
			_ = json.Unmarshal(labels, &sess.Labels)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (c *Client) AppendEvent(ctx context.Context, event session.Event) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO session_events (id, session_id, source, kind, execution_id, payload, created_at, offset, trace_id, metadata_json, deleted)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO NOTHING
	`, event.ID, event.SessionID, event.Source, event.Kind, nullString(event.ExecutionID), raw, event.CreatedAt, event.Offset, nullString(event.TraceID), metadata, event.Deleted)
	return err
}

func (c *Client) ListEvents(ctx context.Context, sessionID string) ([]session.Event, error) {
	return c.ListEventsFiltered(ctx, session.EventQuery{SessionID: sessionID})
}

func (c *Client) ReadEvent(ctx context.Context, sessionID string, eventID string) (session.Event, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT payload, COALESCE(offset,0), COALESCE(trace_id,''), metadata_json, deleted
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
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		UPDATE session_events
		SET source = $3,
		    kind = $4,
		    execution_id = $5,
		    payload = $6,
		    created_at = $7,
		    offset = $8,
		    trace_id = $9,
		    metadata_json = $10,
		    deleted = $11
		WHERE session_id = $1 AND id = $2
	`, event.SessionID, event.ID, event.Source, event.Kind, nullString(event.ExecutionID), raw, event.CreatedAt, event.Offset, nullString(event.TraceID), metadata, event.Deleted)
	return err
}

func (c *Client) ListEventsFiltered(ctx context.Context, query session.EventQuery) ([]session.Event, error) {
	sql := `
		SELECT payload, COALESCE(offset,0), COALESCE(trace_id,''), metadata_json, deleted
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
		sql += ` AND COALESCE(offset,0) >= $` + strconv.Itoa(arg)
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
	sql += ` ORDER BY COALESCE(offset,0) ASC, created_at ASC`
	rows, err := c.Pool.Query(ctx, sql, args...)
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
			id, session_id, trigger_event_id, policy_bundle_id, proposal_id, rollout_id, selection_reason, trace_id, status, lease_owner, lease_expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO NOTHING
	`, exec.ID, exec.SessionID, exec.TriggerEventID, nullString(exec.PolicyBundleID), nullString(exec.ProposalID), nullString(exec.RolloutID), nullString(exec.SelectionReason), nullString(exec.TraceID), exec.Status, nullString(exec.LeaseOwner), nullTime(exec.LeaseExpiresAt), exec.CreatedAt, exec.UpdatedAt)
	if err != nil {
		return err
	}
	for _, step := range steps {
		_, err = tx.Exec(ctx, `
			INSERT INTO execution_steps (
				id, execution_id, name, status, attempt, recomputable, lease_owner, lease_expires_at, idempotency_key, last_error, started_at, finished_at, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (id) DO NOTHING
		`, step.ID, step.ExecutionID, step.Name, step.Status, step.Attempt, step.Recomputable, nullString(step.LeaseOwner), nullTime(step.LeaseExpiresAt), step.IdempotencyKey, nullString(step.LastError), nullTime(step.StartedAt), nullTime(step.FinishedAt), step.CreatedAt, step.UpdatedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (c *Client) ListExecutions(ctx context.Context) ([]execution.TurnExecution, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, trigger_event_id, COALESCE(policy_bundle_id,''), COALESCE(proposal_id,''), COALESCE(rollout_id,''), COALESCE(selection_reason,''), COALESCE(trace_id,''), status, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at
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
		if err := rows.Scan(&exec.ID, &exec.SessionID, &exec.TriggerEventID, &exec.PolicyBundleID, &exec.ProposalID, &exec.RolloutID, &exec.SelectionReason, &exec.TraceID, &exec.Status, &exec.LeaseOwner, &exec.LeaseExpiresAt, &exec.CreatedAt, &exec.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, exec)
	}
	return out, rows.Err()
}

func (c *Client) GetExecution(ctx context.Context, executionID string) (execution.TurnExecution, []execution.ExecutionStep, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, session_id, trigger_event_id, COALESCE(policy_bundle_id,''), COALESCE(proposal_id,''), COALESCE(rollout_id,''), COALESCE(selection_reason,''), COALESCE(trace_id,''), status, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at
		FROM turn_executions
		WHERE id = $1
	`, executionID)
	var exec execution.TurnExecution
	if err := row.Scan(&exec.ID, &exec.SessionID, &exec.TriggerEventID, &exec.PolicyBundleID, &exec.ProposalID, &exec.RolloutID, &exec.SelectionReason, &exec.TraceID, &exec.Status, &exec.LeaseOwner, &exec.LeaseExpiresAt, &exec.CreatedAt, &exec.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return execution.TurnExecution{}, nil, errors.New("execution not found")
		}
		return execution.TurnExecution{}, nil, err
	}

	rows, err := c.Pool.Query(ctx, `
		SELECT id, execution_id, name, status, attempt, recomputable, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), idempotency_key, COALESCE(last_error,''), COALESCE(started_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), COALESCE(finished_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at
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
		if err := rows.Scan(&step.ID, &step.ExecutionID, &step.Name, &step.Status, &step.Attempt, &step.Recomputable, &step.LeaseOwner, &step.LeaseExpiresAt, &step.IdempotencyKey, &step.LastError, &step.StartedAt, &step.FinishedAt, &step.CreatedAt, &step.UpdatedAt); err != nil {
			return execution.TurnExecution{}, nil, err
		}
		steps = append(steps, step)
	}
	return exec, steps, rows.Err()
}

func (c *Client) UpdateExecution(ctx context.Context, exec execution.TurnExecution) error {
	_, err := c.Pool.Exec(ctx, `
		UPDATE turn_executions
		SET session_id = $2,
		    trigger_event_id = $3,
		    policy_bundle_id = $4,
		    proposal_id = $5,
		    rollout_id = $6,
		    selection_reason = $7,
		    trace_id = $8,
		    status = $9,
		    lease_owner = $10,
		    lease_expires_at = $11,
		    updated_at = $12
		WHERE id = $1
	`, exec.ID, exec.SessionID, exec.TriggerEventID, nullString(exec.PolicyBundleID), nullString(exec.ProposalID), nullString(exec.RolloutID), nullString(exec.SelectionReason), nullString(exec.TraceID), exec.Status, nullString(exec.LeaseOwner), nullTime(exec.LeaseExpiresAt), exec.UpdatedAt)
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
		    started_at = $9,
		    finished_at = $10,
		    updated_at = $11
		WHERE id = $1
	`, step.ID, step.Status, step.Attempt, step.Recomputable, nullString(step.LeaseOwner), nullTime(step.LeaseExpiresAt), step.IdempotencyKey, nullString(step.LastError), nullTime(step.StartedAt), nullTime(step.FinishedAt), step.UpdatedAt)
	return err
}

func (c *Client) ListRunnableExecutions(ctx context.Context, now time.Time) ([]execution.TurnExecution, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, trigger_event_id, COALESCE(policy_bundle_id,''), COALESCE(proposal_id,''), COALESCE(rollout_id,''), COALESCE(selection_reason,''), COALESCE(trace_id,''), status, COALESCE(lease_owner,''), COALESCE(lease_expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at
		FROM turn_executions
		WHERE status IN ('pending', 'running')
		  AND (lease_expires_at IS NULL OR lease_expires_at < $1 OR lease_owner = '')
		ORDER BY created_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []execution.TurnExecution
	for rows.Next() {
		var exec execution.TurnExecution
		if err := rows.Scan(&exec.ID, &exec.SessionID, &exec.TriggerEventID, &exec.PolicyBundleID, &exec.ProposalID, &exec.RolloutID, &exec.SelectionReason, &exec.TraceID, &exec.Status, &exec.LeaseOwner, &exec.LeaseExpiresAt, &exec.CreatedAt, &exec.UpdatedAt); err != nil {
			return nil, err
		}
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

func (c *Client) SaveApprovalSession(ctx context.Context, session approval.Session) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO approval_sessions (id, session_id, execution_id, tool_id, status, request_text, decision, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    request_text = EXCLUDED.request_text,
		    decision = EXCLUDED.decision,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = EXCLUDED.updated_at
	`, session.ID, session.SessionID, session.ExecutionID, session.ToolID, session.Status, session.RequestText, nullString(session.Decision), nullTime(session.ExpiresAt), session.CreatedAt, session.UpdatedAt)
	return err
}

func (c *Client) GetApprovalSession(ctx context.Context, approvalID string) (approval.Session, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, session_id, execution_id, tool_id, status, request_text, COALESCE(decision,''), COALESCE(expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at
		FROM approval_sessions WHERE id = $1
	`, approvalID)
	var item approval.Session
	if err := row.Scan(&item.ID, &item.SessionID, &item.ExecutionID, &item.ToolID, &item.Status, &item.RequestText, &item.Decision, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return approval.Session{}, errors.New("approval session not found")
		}
		return approval.Session{}, err
	}
	return item, nil
}

func (c *Client) ListApprovalSessions(ctx context.Context, sessionID string) ([]approval.Session, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, execution_id, tool_id, status, request_text, COALESCE(decision,''), COALESCE(expires_at, TIMESTAMPTZ '0001-01-01 00:00:00+00'), created_at, updated_at
		FROM approval_sessions WHERE session_id = $1 ORDER BY created_at DESC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []approval.Session
	for rows.Next() {
		var item approval.Session
		if err := rows.Scan(&item.ID, &item.SessionID, &item.ExecutionID, &item.ToolID, &item.Status, &item.RequestText, &item.Decision, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
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
		INSERT INTO tool_runs (id, execution_id, tool_id, status, idempotency_key, input_json, output_json, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    input_json = EXCLUDED.input_json,
		    output_json = EXCLUDED.output_json
	`, run.ID, run.ExecutionID, run.ToolID, run.Status, run.IdempotencyKey, input, output, run.CreatedAt)
	return err
}

func (c *Client) ListToolRuns(ctx context.Context, executionID string) ([]toolrun.Run, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, execution_id, tool_id, status, idempotency_key, input_json::text, output_json::text, created_at
		FROM tool_runs WHERE execution_id = $1 ORDER BY created_at ASC
	`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []toolrun.Run
	for rows.Next() {
		var run toolrun.Run
		if err := rows.Scan(&run.ID, &run.ExecutionID, &run.ToolID, &run.Status, &run.IdempotencyKey, &run.InputJSON, &run.OutputJSON, &run.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (c *Client) SaveDeliveryAttempt(ctx context.Context, attempt delivery.Attempt) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO delivery_attempts (id, session_id, execution_id, event_id, channel, status, idempotency_key, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status
	`, attempt.ID, attempt.SessionID, attempt.ExecutionID, attempt.EventID, attempt.Channel, attempt.Status, attempt.IdempotencyKey, attempt.CreatedAt)
	return err
}

func (c *Client) ListDeliveryAttempts(ctx context.Context, executionID string) ([]delivery.Attempt, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, session_id, execution_id, event_id, channel, status, idempotency_key, created_at
		FROM delivery_attempts WHERE execution_id = $1 ORDER BY created_at ASC
	`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []delivery.Attempt
	for rows.Next() {
		var attempt delivery.Attempt
		if err := rows.Scan(&attempt.ID, &attempt.SessionID, &attempt.ExecutionID, &attempt.EventID, &attempt.Channel, &attempt.Status, &attempt.IdempotencyKey, &attempt.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

func (c *Client) CreateEvalRun(ctx context.Context, run replay.Run) error {
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO eval_runs (id, type, source_execution_id, proposal_id, active_bundle_id, shadow_bundle_id, status, result_json, diff_json, last_error, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO NOTHING
	`, run.ID, run.Type, run.SourceExecutionID, nullString(run.ProposalID), nullString(run.ActiveBundleID), nullString(run.ShadowBundleID), run.Status, mustJSON(run.ResultJSON), mustJSON(run.DiffJSON), nullString(run.LastError), run.CreatedAt, run.UpdatedAt)
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
		    updated_at = $9
		WHERE id = $1
	`, run.ID, nullString(run.ProposalID), nullString(run.ActiveBundleID), nullString(run.ShadowBundleID), run.Status, mustJSON(run.ResultJSON), mustJSON(run.DiffJSON), nullString(run.LastError), run.UpdatedAt)
	return err
}

func (c *Client) GetEvalRun(ctx context.Context, runID string) (replay.Run, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, type, source_execution_id, COALESCE(proposal_id,''), COALESCE(active_bundle_id,''), COALESCE(shadow_bundle_id,''), status, result_json::text, diff_json::text, COALESCE(last_error,''), created_at, updated_at
		FROM eval_runs WHERE id = $1
	`, runID)
	var run replay.Run
	if err := row.Scan(&run.ID, &run.Type, &run.SourceExecutionID, &run.ProposalID, &run.ActiveBundleID, &run.ShadowBundleID, &run.Status, &run.ResultJSON, &run.DiffJSON, &run.LastError, &run.CreatedAt, &run.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return replay.Run{}, errors.New("eval run not found")
		}
		return replay.Run{}, err
	}
	return run, nil
}

func (c *Client) ListEvalRuns(ctx context.Context) ([]replay.Run, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, type, source_execution_id, COALESCE(proposal_id,''), COALESCE(active_bundle_id,''), COALESCE(shadow_bundle_id,''), status, result_json::text, diff_json::text, COALESCE(last_error,''), created_at, updated_at
		FROM eval_runs ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []replay.Run
	for rows.Next() {
		var run replay.Run
		if err := rows.Scan(&run.ID, &run.Type, &run.SourceExecutionID, &run.ProposalID, &run.ActiveBundleID, &run.ShadowBundleID, &run.Status, &run.ResultJSON, &run.DiffJSON, &run.LastError, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (c *Client) ListRunnableEvalRuns(ctx context.Context, now time.Time) ([]replay.Run, error) {
	_ = now
	rows, err := c.Pool.Query(ctx, `
		SELECT id, type, source_execution_id, COALESCE(proposal_id,''), COALESCE(active_bundle_id,''), COALESCE(shadow_bundle_id,''), status, result_json::text, diff_json::text, COALESCE(last_error,''), created_at, updated_at
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
		if err := rows.Scan(&run.ID, &run.Type, &run.SourceExecutionID, &run.ProposalID, &run.ActiveBundleID, &run.ShadowBundleID, &run.Status, &run.ResultJSON, &run.DiffJSON, &run.LastError, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (c *Client) SaveProposal(ctx context.Context, proposal rollout.Proposal) error {
	evidence, err := json.Marshal(proposal.EvidenceRefs)
	if err != nil {
		return err
	}
	risks, err := json.Marshal(proposal.RiskFlags)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO policy_proposals (id, source_bundle_id, candidate_bundle_id, state, rationale, evidence_refs, replay_score, safety_score, risk_flags, requires_manual_approval, eval_summary_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO UPDATE
		SET state = EXCLUDED.state,
		    rationale = EXCLUDED.rationale,
		    evidence_refs = EXCLUDED.evidence_refs,
		    replay_score = EXCLUDED.replay_score,
		    safety_score = EXCLUDED.safety_score,
		    risk_flags = EXCLUDED.risk_flags,
		    requires_manual_approval = EXCLUDED.requires_manual_approval,
		    eval_summary_json = EXCLUDED.eval_summary_json,
		    updated_at = EXCLUDED.updated_at
	`, proposal.ID, proposal.SourceBundleID, proposal.CandidateBundleID, proposal.State, proposal.Rationale, evidence, proposal.ReplayScore, proposal.SafetyScore, risks, proposal.RequiresManualApproval, mustJSON(proposal.EvalSummaryJSON), proposal.CreatedAt, proposal.UpdatedAt)
	return err
}

func (c *Client) GetProposal(ctx context.Context, proposalID string) (rollout.Proposal, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, source_bundle_id, candidate_bundle_id, state, rationale, evidence_refs, replay_score, safety_score, risk_flags, requires_manual_approval, eval_summary_json::text, created_at, updated_at
		FROM policy_proposals WHERE id = $1
	`, proposalID)
	var item rollout.Proposal
	var evidence, risks []byte
	if err := row.Scan(&item.ID, &item.SourceBundleID, &item.CandidateBundleID, &item.State, &item.Rationale, &evidence, &item.ReplayScore, &item.SafetyScore, &risks, &item.RequiresManualApproval, &item.EvalSummaryJSON, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rollout.Proposal{}, errors.New("proposal not found")
		}
		return rollout.Proposal{}, err
	}
	_ = json.Unmarshal(evidence, &item.EvidenceRefs)
	_ = json.Unmarshal(risks, &item.RiskFlags)
	return item, nil
}

func (c *Client) ListProposals(ctx context.Context) ([]rollout.Proposal, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, source_bundle_id, candidate_bundle_id, state, rationale, evidence_refs, replay_score, safety_score, risk_flags, requires_manual_approval, eval_summary_json::text, created_at, updated_at
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
		if err := rows.Scan(&item.ID, &item.SourceBundleID, &item.CandidateBundleID, &item.State, &item.Rationale, &evidence, &item.ReplayScore, &item.SafetyScore, &risks, &item.RequiresManualApproval, &item.EvalSummaryJSON, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidence, &item.EvidenceRefs)
		_ = json.Unmarshal(risks, &item.RiskFlags)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (c *Client) SaveRollout(ctx context.Context, record rollout.Record) error {
	includeRaw, err := json.Marshal(record.IncludeSessionIDs)
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO policy_rollouts (id, proposal_id, status, channel, percentage, include_session_ids, previous_bundle_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    percentage = EXCLUDED.percentage,
		    include_session_ids = EXCLUDED.include_session_ids,
		    previous_bundle_id = EXCLUDED.previous_bundle_id,
		    updated_at = EXCLUDED.updated_at
	`, record.ID, record.ProposalID, record.Status, record.Channel, record.Percentage, includeRaw, nullString(record.PreviousBundleID), record.CreatedAt, record.UpdatedAt)
	return err
}

func (c *Client) GetRollout(ctx context.Context, rolloutID string) (rollout.Record, error) {
	row := c.Pool.QueryRow(ctx, `
		SELECT id, proposal_id, status, channel, percentage, include_session_ids, COALESCE(previous_bundle_id,''), created_at, updated_at
		FROM policy_rollouts WHERE id = $1
	`, rolloutID)
	var item rollout.Record
	var includeRaw []byte
	if err := row.Scan(&item.ID, &item.ProposalID, &item.Status, &item.Channel, &item.Percentage, &includeRaw, &item.PreviousBundleID, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rollout.Record{}, errors.New("rollout not found")
		}
		return rollout.Record{}, err
	}
	_ = json.Unmarshal(includeRaw, &item.IncludeSessionIDs)
	return item, nil
}

func (c *Client) ListRollouts(ctx context.Context) ([]rollout.Record, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, proposal_id, status, channel, percentage, include_session_ids, COALESCE(previous_bundle_id,''), created_at, updated_at
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
		if err := rows.Scan(&item.ID, &item.ProposalID, &item.Status, &item.Channel, &item.Percentage, &includeRaw, &item.PreviousBundleID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(includeRaw, &item.IncludeSessionIDs)
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

func nullTime(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v
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
