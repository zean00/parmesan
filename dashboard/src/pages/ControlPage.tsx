import { useEffect, useMemo, useState } from "react";
import { getJSON } from "../lib/api";
import { arrayOfStrings } from "../lib/format";
import type { JSONObject } from "../types";
import { InspectPanel } from "../components/InspectPanel";
import { KeyValueList } from "../components/KeyValueList";
import { PageHeader } from "../components/PageHeader";
import { Pill } from "../components/Pill";
import { Section } from "../components/Section";

type ControlFormState = {
  agentId: string;
  channel: string;
  customerId: string;
  sessionId: string;
  sessionKey: string;
  scopeKind: string;
  scopeId: string;
};

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

export function ControlPage({ token }: { token: string }) {
  const [form, setForm] = useState<ControlFormState>(defaultForm);
  const [data, setData] = useState<DashboardState>({});
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

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
          getJSON<JSONObject>(token, "/healthz").catch(() => ({ status: "unknown" })),
          getJSON<JSONObject[]>(token, "/v1/operator/agents").catch(() => []),
          getJSON<JSONObject>(token, "/v1/operator/control-state", query),
          getJSON<JSONObject>(token, "/v1/operator/control-state/history", query),
          getJSON<JSONObject>(token, "/v1/operator/policy/composed-state", {
            agent_id: form.agentId,
            channel: form.channel,
            customer_id: form.customerId,
            session_key: form.sessionKey,
          }),
          getJSON<JSONObject>(token, "/v1/operator/control-state/applied", query),
          getJSON<JSONObject>(token, "/v1/operator/control-state/pending", query),
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
    <>
      <PageHeader
        eyebrow="Runtime control plane"
        title="Active state and recent governance flow"
        summary="Inspect composed policy, graph-native changes, knowledge state, and teaching outputs for any current scope."
        actions={
          <>
            {loading ? <Pill label="Syncing" tone="attention" /> : <Pill label="Live view" tone="positive" />}
            {error ? <Pill label="Fetch error" tone="attention" /> : null}
          </>
        }
      />
      <div className="panel-form">
        <div className="stack-heading">
          <p className="stack-heading__eyebrow">Scope</p>
          <h3>Resolve a runtime scope</h3>
          <p>Use any combination of agent, session, customer, or scope keys to inspect the active control state.</p>
        </div>
        <form
          className="query-form query-form--inline"
          onSubmit={(event) => {
            event.preventDefault();
            void loadDashboard();
          }}
        >
          <label>
            <span>Agent</span>
            <input list="control-agent-options" value={form.agentId} onChange={(event) => setForm((current) => ({ ...current, agentId: event.target.value }))} />
          </label>
          <datalist id="control-agent-options">
            {agentOptions.map((item) => (
              <option key={String(item.id)} value={String(item.id)} />
            ))}
          </datalist>
          <label>
            <span>Channel</span>
            <input value={form.channel} onChange={(event) => setForm((current) => ({ ...current, channel: event.target.value }))} />
          </label>
          <label>
            <span>Session</span>
            <input value={form.sessionId} onChange={(event) => setForm((current) => ({ ...current, sessionId: event.target.value }))} />
          </label>
          <label>
            <span>Customer</span>
            <input value={form.customerId} onChange={(event) => setForm((current) => ({ ...current, customerId: event.target.value }))} />
          </label>
          <button className="button button--primary" type="submit" disabled={loading}>
            Refresh
          </button>
        </form>
        <div className="filters-grid">
          <label>
            <span>Session key</span>
            <input value={form.sessionKey} onChange={(event) => setForm((current) => ({ ...current, sessionKey: event.target.value }))} />
          </label>
          <label>
            <span>Scope kind</span>
            <input value={form.scopeKind} onChange={(event) => setForm((current) => ({ ...current, scopeKind: event.target.value }))} />
          </label>
          <label>
            <span>Scope id</span>
            <input value={form.scopeId} onChange={(event) => setForm((current) => ({ ...current, scopeId: event.target.value }))} />
          </label>
          <div className="surface-panel">
            <div className="stack-heading">
              <p className="stack-heading__eyebrow">Health</p>
              <h3>{String(data.health?.status ?? "unknown")}</h3>
              <p>Operator API health probe.</p>
            </div>
          </div>
        </div>
      </div>
      {error ? <div className="banner banner--error">{error}</div> : null}
      <div className="workspace-grid">
        <Section eyebrow="Selection" title="Policy state" summary="The snapshot, rollout, and capability boundary currently governing this scope.">
          <div className="section-grid">
            <KeyValueList
              entries={[
                ["Bundle", policySelection.bundle_id ?? "n/a"],
                ["Proposal", policySelection.proposal_id ?? "n/a"],
                ["Rollout", policySelection.rollout_id ?? "n/a"],
                ["Reason", policySelection.reason ?? "n/a"],
              ]}
            />
            <div>
              <div className="metric-strip">
                <Metric label="Providers" value={arrayOfStrings(capabilityIsolation.allowed_provider_ids).length} />
                <Metric label="Tools" value={arrayOfStrings(capabilityIsolation.allowed_tool_ids).length} />
                <Metric label="Retrievers" value={arrayOfStrings(capabilityIsolation.allowed_retriever_ids).length} />
                <Metric label="Knowledge scopes" value={Array.isArray(capabilityIsolation.allowed_knowledge_scopes) ? capabilityIsolation.allowed_knowledge_scopes.length : 0} />
              </div>
              <InspectPanel title="Capability isolation payload" summary="Provider, tool, and retriever restrictions after resolution." value={capabilityIsolation} />
            </div>
          </div>
        </Section>
        <Section eyebrow="Control" title="Scope control-state" summary="Cross-plane summary for policy, knowledge, teaching, rollouts, and customer preferences.">
          <div className="section-grid">
            <KeyValueList entries={[["Agent", form.agentId || "n/a"], ["Channel", form.channel || "n/a"], ["Session", form.sessionId || form.sessionKey || "n/a"], ["Customer", form.customerId || "n/a"]]} />
            <InspectPanel title="Control-state payload" summary="Raw control graph payload for the selected scope." value={data.controlState} />
          </div>
        </Section>
        <Section eyebrow="Knowledge" title="Knowledge state" summary="Active snapshot metadata, lint pressure, and scope health.">
          <div className="section-grid">
            <KeyValueList
              entries={[
                ["Snapshot", (knowledgeState.snapshot as JSONObject | undefined)?.id ?? "n/a"],
                ["Sources", Array.isArray(knowledgeState.sources) ? knowledgeState.sources.length : 0],
                ["Findings", Array.isArray(knowledgeState.open_lint_findings) ? knowledgeState.open_lint_findings.length : 0],
                ["Jobs", Array.isArray(knowledgeState.recent_sync_jobs) ? knowledgeState.recent_sync_jobs.length : 0],
              ]}
            />
            <InspectPanel title="Knowledge payload" summary="Snapshot, source, and lint details for deeper inspection." value={knowledgeState} />
          </div>
        </Section>
        <Section eyebrow="Teaching" title="Teaching state" summary="Feedback outputs and downstream control artifacts grouped for the current session.">
          <div className="section-grid">
            <KeyValueList
              entries={[
                ["Feedback records", Array.isArray(teachingState.feedback) ? teachingState.feedback.length : 0],
                ["Outputs", Object.keys((teachingState.aggregated_outputs as JSONObject | undefined) ?? {}).length],
                ["Recent changes", recentChanges.length],
                ["Control groups", Object.keys(controlGroups).length],
              ]}
            />
            <InspectPanel title="Teaching payload" summary="Feedback and downstream artifacts for the selected scope." value={teachingState} />
          </div>
        </Section>
        <Section eyebrow="Governance" title="Recent control changes" summary="Pending and applied graph-native changes for the selected scope.">
          <div className="timeline-grid">
            <ChangeColumn title="Pending" items={pendingChanges} />
            <ChangeColumn title="Applied" items={appliedChanges} />
          </div>
        </Section>
        <Section eyebrow="History" title="Recent lineage trail" summary="Recent change items across the resolved graph groups.">
          <div className="group-list">
            {Object.entries(controlGroups).map(([key, value]) => (
              <span className="group-list__item" key={key}>
                {key}: <code>{String(value)}</code>
              </span>
            ))}
          </div>
          <InspectPanel title="Recent lineage payload" summary="Raw recent-change list across the resolved control groups." value={recentChanges} />
        </Section>
      </div>
    </>
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

function ChangeColumn({ title, items }: { title: string; items: unknown[] }) {
  return (
    <div className="change-column">
      <div className="change-column__header">
        <h3>{title}</h3>
        <Pill label={`${items.length} items`} />
      </div>
      <InspectPanel title={`${title} changes`} summary="Raw change items for this governance bucket." value={items} defaultOpen />
    </div>
  );
}
