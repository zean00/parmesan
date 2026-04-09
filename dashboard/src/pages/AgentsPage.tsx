import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { Pill } from "../components/Pill";
import { getJSON } from "../lib/api";
import { formatDate } from "../lib/format";
import type { AgentProfile } from "../types";

export function AgentsPage({ token }: { token: string }) {
  const [agents, setAgents] = useState<AgentProfile[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  async function load() {
    setLoading(true);
    setError("");
    try {
      setAgents(await getJSON<AgentProfile[]>(token, "/v1/operator/agents"));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  return (
    <>
      <PageHeader
        eyebrow="Agents"
        title="Agent profiles"
        summary="Browse available agents, inspect their default policy and knowledge scopes, and jump into live testing."
        actions={
          <>
            {loading ? <Pill label="Loading" tone="attention" /> : <Pill label={`${agents.length} agents`} tone="positive" />}
            <button className="button button--primary" type="button" onClick={() => void load()} disabled={loading}>
              Refresh
            </button>
          </>
        }
      />
      {error ? <div className="banner banner--error">{error}</div> : null}
      <section className="section">
        <header className="section__header">
          <div>
            <p className="section__eyebrow">Directory</p>
            <h2>Available agents</h2>
            <p className="section__summary">Each profile has its own default policy bundle, knowledge scope, and capability boundary.</p>
          </div>
        </header>
        <div className="section__body">
          <div className="data-table">
            <div className="data-table__head">
              <span>Name</span>
              <span>Status</span>
              <span>Policy bundle</span>
              <span>Knowledge scope</span>
              <span>Active sessions</span>
              <span>Updated</span>
            </div>
            {agents.map((agent) => (
              <Link className="data-table__row" key={agent.id} to={`/agents/${agent.id}`}>
                <span>
                  <strong>{agent.name}</strong>
                  <small>{agent.id}</small>
                </span>
                <span>
                  <Pill label={agent.status || "unknown"} />
                </span>
                <span>{agent.default_policy_bundle_id || "n/a"}</span>
                <span>{[agent.default_knowledge_scope_kind, agent.default_knowledge_scope_id].filter(Boolean).join(":") || "n/a"}</span>
                <span>{agent.active_session_count ?? 0}</span>
                <span>{formatDate(agent.updated_at)}</span>
              </Link>
            ))}
            {agents.length === 0 ? <div className="data-table__empty">No agents available.</div> : null}
          </div>
        </div>
      </section>
    </>
  );
}
