import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { getJSON } from "../lib/api";
import { formatDate } from "../lib/format";
import type { AgentProfile, QueueSummary, SessionFilters, SessionView } from "../types";
import { PageHeader } from "../components/PageHeader";
import { Pill } from "../components/Pill";

const defaultFilters: SessionFilters = {
  agentId: "",
  customerId: "",
  assignedOperatorId: "",
  channel: "",
  activeOnly: false,
  pendingApprovalOnly: false,
  failedMediaOnly: false,
  unresolvedLintOnly: false,
};

export function SessionsPage({ token }: { token: string }) {
  const [filters, setFilters] = useState<SessionFilters>(defaultFilters);
  const [agents, setAgents] = useState<AgentProfile[]>([]);
  const [sessions, setSessions] = useState<SessionView[]>([]);
  const [queue, setQueue] = useState<QueueSummary>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  async function load() {
    setLoading(true);
    setError("");
    try {
      const params = {
        agent_id: filters.agentId || undefined,
        customer_id: filters.customerId || undefined,
        assigned_operator_id: filters.assignedOperatorId || undefined,
        active: filters.activeOnly || undefined,
        pending_approval: filters.pendingApprovalOnly || undefined,
        failed_media: filters.failedMediaOnly || undefined,
        unresolved_lint: filters.unresolvedLintOnly || undefined,
      };
      const [agentItems, sessionItems, queueSummary] = await Promise.all([
        getJSON<AgentProfile[]>(token, "/v1/operator/agents"),
        getJSON<SessionView[]>(token, "/v1/operator/sessions", params),
        getJSON<QueueSummary>(token, "/v1/operator/queue/summary", { agent_id: filters.agentId || undefined }),
      ]);
      setAgents(agentItems);
      setSessions(
        sessionItems.filter((item) => (filters.channel ? item.channel === filters.channel : true)),
      );
      setQueue(queueSummary);
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
        eyebrow="Session ops"
        title="Session inbox"
        summary="Monitor the active queue, narrow the operator inbox, and open a live session workspace when intervention is needed."
        actions={
          <>
            {loading ? <Pill label="Loading" tone="attention" /> : <Pill label={`${sessions.length} sessions`} tone="positive" />}
            <button className="button button--primary" type="button" onClick={() => void load()} disabled={loading}>
              Refresh
            </button>
          </>
        }
      />
      <div className="page-stack">
        {error ? <div className="banner banner--error">{error}</div> : null}
        <div className="page-band">
          <section className="panel-form">
            <div className="stack-heading">
              <p className="stack-heading__eyebrow">Filters</p>
              <h3>Queue filters</h3>
              <p>Agent, customer, assignment, and operational attention flags.</p>
            </div>
            <div className="filters-grid">
              <label>
                <span>Agent</span>
                <select value={filters.agentId} onChange={(event) => setFilters((current) => ({ ...current, agentId: event.target.value }))}>
                  <option value="">All agents</option>
                  {agents.map((item) => (
                    <option key={item.id} value={item.id}>
                      {item.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                <span>Customer</span>
                <input value={filters.customerId} onChange={(event) => setFilters((current) => ({ ...current, customerId: event.target.value }))} />
              </label>
              <label>
                <span>Assigned operator</span>
                <input
                  value={filters.assignedOperatorId}
                  onChange={(event) => setFilters((current) => ({ ...current, assignedOperatorId: event.target.value }))}
                />
              </label>
              <label>
                <span>Channel</span>
                <input value={filters.channel} onChange={(event) => setFilters((current) => ({ ...current, channel: event.target.value }))} placeholder="web" />
              </label>
              <label className="checkbox">
                <input type="checkbox" checked={filters.activeOnly} onChange={(event) => setFilters((current) => ({ ...current, activeOnly: event.target.checked }))} />
                <span>Active or assigned only</span>
              </label>
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={filters.pendingApprovalOnly}
                  onChange={(event) => setFilters((current) => ({ ...current, pendingApprovalOnly: event.target.checked }))}
                />
                <span>Pending approval</span>
              </label>
              <label className="checkbox">
                <input type="checkbox" checked={filters.failedMediaOnly} onChange={(event) => setFilters((current) => ({ ...current, failedMediaOnly: event.target.checked }))} />
                <span>Failed media</span>
              </label>
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={filters.unresolvedLintOnly}
                  onChange={(event) => setFilters((current) => ({ ...current, unresolvedLintOnly: event.target.checked }))}
                />
                <span>Unresolved lint</span>
              </label>
            </div>
            <div className="inline-actions">
              <button className="button button--primary" type="button" onClick={() => void load()}>
                Apply filters
              </button>
              <button className="button button--ghost" type="button" onClick={() => setFilters(defaultFilters)}>
                Reset
              </button>
            </div>
          </section>

          <section className="section">
            <div className="section__body">
              <div className="metric-strip">
                {Object.entries(queue).map(([key, value]) => (
                  <div className="metric" key={key}>
                    <span>{key.split("_").join(" ")}</span>
                    <strong>{value}</strong>
                  </div>
                ))}
              </div>
            </div>
          </section>
        </div>

        <section className="section">
          <header className="section__header">
            <div>
              <p className="section__eyebrow">Sessions</p>
              <h2>Live inbox</h2>
              <p className="section__summary">Open the session workspace when a thread needs approval handling, takeover, or recovery.</p>
            </div>
          </header>
          <div className="section__body">
            <div className="data-list">
              <div className="data-list__head">
                <span>Session</span>
                <span>Agent</span>
                <span>Customer</span>
                <span>Status</span>
                <span>Attention</span>
                <span>Last activity</span>
              </div>
              {sessions.map((item) => {
                const attention = attentionFlags(item);
                return (
                  <Link className="data-list__row" key={item.id} to={`/sessions/${item.id}`}>
                    <div className="data-list__cell data-list__title">
                      <span className="data-list__label">Session</span>
                      <strong>{item.title || item.id}</strong>
                      <small>{item.id}</small>
                    </div>
                    <div className="data-list__cell">
                      <span className="data-list__label">Agent</span>
                      <span>{item.agent_id || "n/a"}</span>
                    </div>
                    <div className="data-list__cell">
                      <span className="data-list__label">Customer</span>
                      <span>{item.customer_id || "n/a"}</span>
                    </div>
                    <div className="data-list__cell">
                      <span className="data-list__label">Status</span>
                      <Pill label={item.status || "unknown"} tone={item.status === "failed" ? "danger" : item.status === "closed" ? "neutral" : "positive"} />
                    </div>
                    <div className="data-list__cell attention-cell">
                      <span className="data-list__label">Attention</span>
                      {attention.length ? (
                        <div className="pill-group">
                          {attention.map((flag) => (
                            <Pill key={flag.label} label={flag.label} tone={flag.tone} />
                          ))}
                        </div>
                      ) : (
                        <Pill label="Clear" tone="neutral" />
                      )}
                    </div>
                    <div className="data-list__cell">
                      <span className="data-list__label">Last activity</span>
                      <span>{formatDate(item.last_activity_at)}</span>
                    </div>
                  </Link>
                );
              })}
              {sessions.length === 0 ? <div className="data-list__empty">No sessions matched the current filter set.</div> : null}
            </div>
          </div>
        </section>
      </div>
    </>
  );
}

function attentionFlags(item: SessionView): Array<{ label: string; tone: "neutral" | "positive" | "attention" | "danger" }> {
  const flags: Array<{ label: string; tone: "neutral" | "positive" | "attention" | "danger" }> = [];
  if (item.pending_approval_count) {
    flags.push({ label: `${item.pending_approval_count} approval`, tone: "attention" });
  }
  if (item.failed_media_count) {
    flags.push({ label: `${item.failed_media_count} media`, tone: "danger" });
  }
  if (item.unresolved_lint_count) {
    flags.push({ label: `${item.unresolved_lint_count} lint`, tone: "attention" });
  }
  return flags;
}
