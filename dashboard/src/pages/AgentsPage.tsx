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
        summary="Browse active profiles, inspect their operating boundaries, and open either the detail view or live test console."
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
          <div className="data-list">
            <div className="data-list__head">
              <span>Name</span>
              <span>Status</span>
              <span>Policy bundle</span>
              <span>Knowledge scope</span>
              <span>Active sessions</span>
              <span>Updated</span>
            </div>
            {agents.map((agent) => (
              <div className="data-list__row" key={agent.id}>
                <div className="data-list__cell data-list__title">
                  <span className="data-list__label">Name</span>
                  <strong>{agent.name}</strong>
                  <small>{agent.id}</small>
                </div>
                <div className="data-list__cell">
                  <span className="data-list__label">Status</span>
                  <Pill label={agent.status || "unknown"} tone={agent.status === "ready" ? "positive" : "attention"} />
                </div>
                <div className="data-list__cell">
                  <span className="data-list__label">Policy bundle</span>
                  <span>{agent.default_policy_bundle_id || "n/a"}</span>
                </div>
                <div className="data-list__cell">
                  <span className="data-list__label">Knowledge scope</span>
                  <span>{[agent.default_knowledge_scope_kind, agent.default_knowledge_scope_id].filter(Boolean).join(":") || "n/a"}</span>
                </div>
                <div className="data-list__cell">
                  <span className="data-list__label">Active sessions</span>
                  <span>{agent.active_session_count ?? 0}</span>
                </div>
                <div className="data-list__cell">
                  <span className="data-list__label">Actions</span>
                  <div className="inline-actions">
                    <Link className="button button--ghost" to={`/agents/${agent.id}`}>
                      Inspect
                    </Link>
                    <Link className="button button--primary" to={`/agents/${agent.id}/test`}>
                      Test
                    </Link>
                  </div>
                  <span className="data-list__meta">{formatDate(agent.updated_at)}</span>
                </div>
              </div>
            ))}
            {agents.length === 0 ? <div className="data-list__empty">No agents available.</div> : null}
          </div>
        </div>
      </section>
    </>
  );
}
