import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { InspectPanel } from "../components/InspectPanel";
import { useParams } from "react-router-dom";
import { KeyValueList } from "../components/KeyValueList";
import { PageHeader } from "../components/PageHeader";
import { Pill } from "../components/Pill";
import { Section } from "../components/Section";
import { getJSON } from "../lib/api";
import { arrayOfStrings } from "../lib/format";
import type { AgentProfile, AgentStats, JSONObject } from "../types";

export function AgentDetailPage({ token }: { token: string }) {
  const { agentId = "" } = useParams();
  const [agent, setAgent] = useState<AgentProfile | null>(null);
  const [stats, setStats] = useState<AgentStats | null>(null);
  const [composedState, setComposedState] = useState<JSONObject>({});
  const [controlState, setControlState] = useState<JSONObject>({});
  const [windowName, setWindowName] = useState("24h");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  async function load() {
    if (!agentId) return;
    setLoading(true);
    setError("");
    try {
      const [agentPayload, statsPayload, composedPayload, controlPayload] = await Promise.all([
        getJSON<AgentProfile>(token, `/v1/operator/agents/${agentId}`),
        getJSON<AgentStats>(token, `/v1/operator/agents/${agentId}/stats`, { window: windowName }),
        getJSON<JSONObject>(token, "/v1/operator/policy/composed-state", { agent_id: agentId }),
        getJSON<JSONObject>(token, "/v1/operator/control-state", { agent_id: agentId }),
      ]);
      setAgent(agentPayload);
      setStats(statsPayload);
      setComposedState(composedPayload);
      setControlState(controlPayload);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, [agentId, windowName]);

  const selection = (composedState.selection ?? {}) as JSONObject;
  const capabilityIsolation = (composedState.capability_isolation ?? {}) as JSONObject;

  return (
    <>
      <PageHeader
        eyebrow="Agent"
        title={agent?.name || agentId || "Agent detail"}
        summary="Inspect profile configuration, policy composition, capability isolation, and operational KPIs for one agent."
        actions={
          <>
            {loading ? <Pill label="Loading" tone="attention" /> : <Pill label={agent?.status || "ready"} tone="positive" />}
            {agentId ? (
              <Link className="button button--primary" to={`/agents/${agentId}/test`}>
                Open test console
              </Link>
            ) : null}
          </>
        }
      />
      {error ? <div className="banner banner--error">{error}</div> : null}
      <div className="workspace-grid">
        <Section eyebrow="Profile" title="Configuration" summary="Default bundle, knowledge scope, and metadata attached to this agent profile.">
          <div className="section-grid">
            <KeyValueList
              entries={[
                ["ID", agent?.id || agentId || "n/a"],
                ["Status", agent?.status || "n/a"],
                ["Default bundle", agent?.default_policy_bundle_id || "n/a"],
                ["Knowledge scope", [agent?.default_knowledge_scope_kind, agent?.default_knowledge_scope_id].filter(Boolean).join(":") || "n/a"],
                ["Soul hash", agent?.soul_hash || "n/a"],
              ]}
            />
            <InspectPanel title="Profile metadata" summary="Raw attached metadata for debugging and audit." value={agent?.metadata ?? {}} />
          </div>
        </Section>
        <Section
          eyebrow="KPIs"
          title="Operational statistics"
          summary="Short-window operator-facing metrics for sessions, failures, approvals, takeovers, and response speed."
          actions={
            <div className="inline-actions">
              {["1h", "24h", "7d"].map((item) => (
                <button
                  className={`button ${windowName === item ? "button--primary" : "button--ghost"}`}
                  key={item}
                  type="button"
                  onClick={() => setWindowName(item)}
                >
                  {item}
                </button>
              ))}
            </div>
          }
        >
          <div className="metric-strip metric-strip--wide">
            <Metric label="Active sessions" value={stats?.active_session_count ?? 0} />
            <Metric label="New sessions" value={stats?.sessions_created ?? 0} />
            <Metric label="Failed executions" value={stats?.failed_executions ?? 0} />
            <Metric label="Pending approvals" value={stats?.pending_approvals ?? 0} />
            <Metric label="Takeovers" value={stats?.takeovers ?? 0} />
            <Metric label="Operator replies" value={stats?.operator_replies ?? 0} />
            <Metric label="Avg first response" value={stats ? `${stats.average_first_response_seconds.toFixed(1)}s` : "0.0s"} />
          </div>
        </Section>
        <Section eyebrow="Policy" title="Composed state" summary="The effective selection and snapshot state this agent will use by default.">
          <div className="section-grid">
            <KeyValueList
              entries={[
                ["Bundle", selection.bundle_id ?? "n/a"],
                ["Proposal", selection.proposal_id ?? "n/a"],
                ["Rollout", selection.rollout_id ?? "n/a"],
                ["Reason", selection.reason ?? "n/a"],
              ]}
            />
            <InspectPanel title="Composed state payload" summary="Resolved state as returned by the operator API." value={composedState} />
          </div>
        </Section>
        <Section eyebrow="Isolation" title="Capability boundary" summary="Allowed providers, tools, retrievers, and knowledge scopes after bundle-level isolation.">
          <div className="metric-strip">
            <Metric label="Providers" value={arrayOfStrings(capabilityIsolation.allowed_provider_ids).length} />
            <Metric label="Tools" value={arrayOfStrings(capabilityIsolation.allowed_tool_ids).length} />
            <Metric label="Retrievers" value={arrayOfStrings(capabilityIsolation.allowed_retriever_ids).length} />
            <Metric label="Knowledge scopes" value={Array.isArray(capabilityIsolation.allowed_knowledge_scopes) ? capabilityIsolation.allowed_knowledge_scopes.length : 0} />
          </div>
          <InspectPanel title="Capability isolation payload" summary="Raw isolation data for provider/tool/retriever auditing." value={capabilityIsolation} />
        </Section>
        <Section eyebrow="Control" title="Agent control-state" summary="Scope-level summary across policy, rollouts, knowledge, and pending changes for this agent.">
          <div className="section-grid">
            <KeyValueList
              entries={[
                ["Pending changes", Array.isArray((controlState.recent_changes as unknown[]) ?? null) ? ((controlState.recent_changes as unknown[]) ?? []).length : 0],
                ["Control groups", Object.keys((controlState.control_groups as Record<string, unknown> | undefined) ?? {}).length],
                ["Knowledge snapshot", ((controlState.knowledge as Record<string, unknown> | undefined)?.snapshot as Record<string, unknown> | undefined)?.id?.toString() ?? "n/a"],
                ["Teaching outputs", Object.keys((((controlState.teaching as Record<string, unknown> | undefined)?.aggregated_outputs as Record<string, unknown> | undefined) ?? {})).length],
              ]}
            />
            <InspectPanel title="Control-state payload" summary="Raw control-state for scope debugging and governance review." value={controlState} />
          </div>
        </Section>
      </div>
    </>
  );
}

function Metric({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="metric">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}
