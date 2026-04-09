import { useEffect, useMemo, useState } from "react";
import { getJSON } from "./lib/api";
import { arrayOfStrings, formatDate, titleCase } from "./lib/format";
import type { ControlFormState, JSONObject } from "./types";
import { JsonBlock } from "./components/JsonBlock";
import { KeyValueList } from "./components/KeyValueList";
import { Pill } from "./components/Pill";
import { Section } from "./components/Section";

const defaultForm: ControlFormState = {
  agentId: "agent_profile_live_support",
  channel: "web",
  customerId: "",
  sessionId: "",
  sessionKey: "",
  scopeKind: "",
  scopeId: "",
};

type DashboardState = {
  health?: JSONObject;
  agents?: JSONObject[];
  controlState?: JSONObject;
  controlHistory?: JSONObject;
  policyComposedState?: JSONObject;
  changesApplied?: JSONObject;
  changesPending?: JSONObject;
};

export default function App() {
  const [form, setForm] = useState<ControlFormState>(defaultForm);
  const [data, setData] = useState<DashboardState>({});
  const [error, setError] = useState<string>("");
  const [loading, setLoading] = useState<boolean>(true);

  const query = useMemo(
    () => ({
      agent_id: form.agentId,
      channel: form.channel,
      customer_id: form.customerId,
      session_id: form.sessionId,
      session_key: form.sessionKey,
      scope_kind: form.scopeKind,
      scope_id: form.scopeId,
    }),
    [form],
  );

  async function loadDashboard() {
    setLoading(true);
    setError("");
    try {
      const [health, agents, controlState, controlHistory, policyComposedState, changesApplied, changesPending] =
        await Promise.all([
          getJSON<JSONObject>("/healthz").catch(() => ({ status: "unknown" })),
          getJSON<JSONObject[]>("/v1/operator/agents").catch(() => []),
          getJSON<JSONObject>("/v1/operator/control-state", query),
          getJSON<JSONObject>("/v1/operator/control-state/history", query),
          getJSON<JSONObject>("/v1/operator/policy/composed-state", {
            agent_id: form.agentId,
            channel: form.channel,
            customer_id: form.customerId,
            session_key: form.sessionKey,
          }),
          getJSON<JSONObject>("/v1/operator/control-state/applied", query),
          getJSON<JSONObject>("/v1/operator/control-state/pending", query),
        ]);
      setData({ health, agents, controlState, controlHistory, policyComposedState, changesApplied, changesPending });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadDashboard();
  }, []);

  const agentOptions = (data.agents ?? []) as JSONObject[];
  const controlGroups = (data.controlState?.control_groups ?? {}) as JSONObject;
  const recentChanges = ((data.controlHistory?.recent_changes ?? data.controlState?.recent_changes ?? []) as unknown[]) ?? [];
  const policySelection = (data.policyComposedState?.selection ?? {}) as JSONObject;
  const controlPolicy = ((data.controlState?.policy ?? {}) as JSONObject) ?? {};
  const capabilityIsolation = (data.policyComposedState?.capability_isolation ?? controlPolicy.capability_isolation ?? {}) as JSONObject;
  const knowledgeState = (data.controlState?.knowledge ?? {}) as JSONObject;
  const teachingState = (data.controlState?.teaching ?? {}) as JSONObject;
  const appliedChanges = ((data.changesApplied?.recent_changes ?? []) as unknown[]) ?? [];
  const pendingChanges = ((data.changesPending?.recent_changes ?? []) as unknown[]) ?? [];

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="sidebar__brand">
          <p className="brand-kicker">Parmesan</p>
          <h1>Control Panel</h1>
          <p className="brand-copy">
            Live operator workspace for policy snapshots, knowledge state, teaching outputs, and graph-native control changes.
          </p>
        </div>

        <form
          className="query-form"
          onSubmit={(event) => {
            event.preventDefault();
            void loadDashboard();
          }}
        >
          <label>
            <span>Agent Profile</span>
            <input
              list="agent-options"
              value={form.agentId}
              onChange={(event) => setForm((current) => ({ ...current, agentId: event.target.value }))}
              placeholder="agent_profile_live_support"
            />
          </label>
          <datalist id="agent-options">
            {agentOptions.map((item) => (
              <option key={String(item.id)} value={String(item.id)} />
            ))}
          </datalist>

          <label>
            <span>Channel</span>
            <input
              value={form.channel}
              onChange={(event) => setForm((current) => ({ ...current, channel: event.target.value }))}
              placeholder="web"
            />
          </label>

          <label>
            <span>Session ID</span>
            <input
              value={form.sessionId}
              onChange={(event) => setForm((current) => ({ ...current, sessionId: event.target.value }))}
              placeholder="sess_validation_1"
            />
          </label>

          <label>
            <span>Session Key</span>
            <input
              value={form.sessionKey}
              onChange={(event) => setForm((current) => ({ ...current, sessionKey: event.target.value }))}
              placeholder="Optional canary key"
            />
          </label>

          <label>
            <span>Customer ID</span>
            <input
              value={form.customerId}
              onChange={(event) => setForm((current) => ({ ...current, customerId: event.target.value }))}
              placeholder="cust_1"
            />
          </label>

          <div className="query-form__row">
            <label>
              <span>Scope Kind</span>
              <input
                value={form.scopeKind}
                onChange={(event) => setForm((current) => ({ ...current, scopeKind: event.target.value }))}
                placeholder="agent"
              />
            </label>
            <label>
              <span>Scope ID</span>
              <input
                value={form.scopeId}
                onChange={(event) => setForm((current) => ({ ...current, scopeId: event.target.value }))}
                placeholder="agent_profile_live_support"
              />
            </label>
          </div>

          <button className="button button--primary" type="submit" disabled={loading}>
            {loading ? "Refreshing..." : "Refresh control state"}
          </button>
        </form>

        <div className="sidebar__meta">
          <div>
            <span>API health</span>
            <strong>{String(data.health?.status ?? "loading")}</strong>
          </div>
          <div>
            <span>Known agents</span>
            <strong>{agentOptions.length}</strong>
          </div>
          <div>
            <span>Recent changes</span>
            <strong>{recentChanges.length}</strong>
          </div>
        </div>
      </aside>

      <main className="workspace">
        <header className="workspace__header">
          <div>
            <p className="workspace__eyebrow">Runtime control plane</p>
            <h2>Active state and recent governance flow</h2>
          </div>
          <div className="workspace__status">
            {loading ? <Pill label="Syncing" tone="attention" /> : <Pill label="Live view" tone="positive" />}
            {error ? <Pill label="Fetch error" tone="attention" /> : null}
          </div>
        </header>

        {error ? <div className="banner banner--error">{error}</div> : null}

        <div className="workspace-grid">
          <Section
            eyebrow="Selection"
            title="Policy state"
            summary="The snapshot, rollout, and capability boundary currently governing this scope."
          >
            <div className="section-grid">
              <KeyValueList
                entries={[
                  ["Bundle", policySelection.bundle_id ?? "n/a"],
                  ["Proposal", policySelection.proposal_id ?? "n/a"],
                  ["Rollout", policySelection.rollout_id ?? "n/a"],
                  ["Reason", policySelection.reason ?? "n/a"],
                ]}
              />
              <div className="metric-strip">
                <Metric label="Providers" value={arrayOfStrings(capabilityIsolation.allowed_provider_ids).length} />
                <Metric label="Tools" value={arrayOfStrings(capabilityIsolation.allowed_tool_ids).length} />
                <Metric label="Retrievers" value={arrayOfStrings(capabilityIsolation.allowed_retriever_ids).length} />
                <Metric
                  label="Knowledge scopes"
                  value={Array.isArray(capabilityIsolation.allowed_knowledge_scopes) ? capabilityIsolation.allowed_knowledge_scopes.length : 0}
                />
              </div>
              <JsonBlock value={capabilityIsolation} />
            </div>
          </Section>

          <Section
            eyebrow="Control"
            title="Scope control-state"
            summary="Cross-plane summary for policy, knowledge, teaching, rollouts, and customer preferences."
          >
            <div className="section-grid">
              <KeyValueList
                entries={[
                  ["Agent", form.agentId || "n/a"],
                  ["Channel", form.channel || "n/a"],
                  ["Session", form.sessionId || form.sessionKey || "n/a"],
                  ["Customer", form.customerId || "n/a"],
                ]}
              />
              <JsonBlock value={data.controlState} />
            </div>
          </Section>

          <Section
            eyebrow="Knowledge"
            title="Knowledge state"
            summary="Active snapshot metadata, lint pressure, and scope health."
          >
            <div className="section-grid">
              <KeyValueList
                entries={[
                  ["Snapshot", (knowledgeState.snapshot as JSONObject | undefined)?.id ?? "n/a"],
                  ["Scope", `${String((knowledgeState.snapshot as JSONObject | undefined)?.scope_kind ?? "n/a")}:${String((knowledgeState.snapshot as JSONObject | undefined)?.scope_id ?? "n/a")}`],
                  ["Sources", Array.isArray(knowledgeState.sources) ? knowledgeState.sources.length : 0],
                  ["Findings", Array.isArray(knowledgeState.open_lint_findings) ? knowledgeState.open_lint_findings.length : 0],
                ]}
              />
              <JsonBlock value={knowledgeState} />
            </div>
          </Section>

          <Section
            eyebrow="Teaching"
            title="Teaching state"
            summary="Feedback outputs and downstream control artifacts grouped for the current session."
          >
            <div className="section-grid">
              <KeyValueList
                entries={[
                  ["Feedback records", Array.isArray(teachingState.feedback) ? teachingState.feedback.length : 0],
                  ["Preference outputs", Array.isArray((teachingState.aggregated_outputs as JSONObject | undefined)?.preferences) ? ((teachingState.aggregated_outputs as JSONObject).preferences as unknown[]).length : 0],
                  ["Knowledge outputs", Array.isArray((teachingState.aggregated_outputs as JSONObject | undefined)?.knowledge_proposals) ? ((teachingState.aggregated_outputs as JSONObject).knowledge_proposals as unknown[]).length : 0],
                  ["Regression outputs", Array.isArray((teachingState.aggregated_outputs as JSONObject | undefined)?.regression_fixtures) ? ((teachingState.aggregated_outputs as JSONObject).regression_fixtures as unknown[]).length : 0],
                ]}
              />
              <JsonBlock value={teachingState} />
            </div>
          </Section>

          <Section
            eyebrow="Governance"
            title="Recent control changes"
            summary="Pending and applied graph-native changes for the selected scope."
          >
            <div className="timeline-grid">
              <ChangeColumn title="Pending" items={pendingChanges} emptyLabel="No pending changes" />
              <ChangeColumn title="Applied" items={appliedChanges} emptyLabel="No applied changes" />
            </div>
          </Section>

          <Section
            eyebrow="History"
            title="Recent lineage trail"
            summary="Recent change items across the resolved graph groups."
            actions={
              <div className="group-list">
                {Object.entries(controlGroups).map(([key, value]) => (
                  <span className="group-list__item" key={key}>
                    {key}: <code>{String(value)}</code>
                  </span>
                ))}
              </div>
            }
          >
            <RecentChangesList items={recentChanges} />
          </Section>
        </div>
      </main>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="metric">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function ChangeColumn({ title, items, emptyLabel }: { title: string; items: unknown[]; emptyLabel: string }) {
  return (
    <div className="change-column">
      <div className="change-column__header">
        <h3>{title}</h3>
        <Pill label={`${items.length} items`} />
      </div>
      {items.length === 0 ? (
        <p className="muted">{emptyLabel}</p>
      ) : (
        <ul className="change-list">
          {items.map((item, index) => {
            const row = (item ?? {}) as JSONObject;
            return (
              <li key={`${String(row.id ?? row.artifact_id ?? title)}-${index}`}>
                <p>{titleCase(String(row.kind ?? row.action ?? "change"))}</p>
                <span>{formatDate(row.created_at ?? row.updated_at)}</span>
                <code>{String(row.id ?? row.artifact_id ?? "n/a")}</code>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function RecentChangesList({ items }: { items: unknown[] }) {
  if (items.length === 0) {
    return <p className="muted">No recent control-plane changes for the selected scope.</p>;
  }
  return (
    <div className="history-table">
      {items.map((item, index) => {
        const row = (item ?? {}) as JSONObject;
        return (
          <article className="history-row" key={`${String(row.id ?? row.artifact_id ?? "change")}-${index}`}>
            <div className="history-row__meta">
              <Pill label={titleCase(String(row.kind ?? row.action ?? "change"))} />
              <span>{formatDate(row.created_at ?? row.updated_at)}</span>
            </div>
            <h3>{String(row.summary ?? row.id ?? row.artifact_id ?? "Unnamed change")}</h3>
            <p className="muted">{String(row.group_id ?? row.bundle_id ?? row.scope_id ?? "No group")} </p>
            <JsonBlock value={row} />
          </article>
        );
      })}
    </div>
  );
}
