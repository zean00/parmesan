import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { Pill } from "../components/Pill";
import { getJSON } from "../lib/api";
import { formatDate } from "../lib/format";
import { streamSSE } from "../lib/sse";
import type { OperatorNotification } from "../types";

export function NotificationsPage({ token }: { token: string }) {
  const [items, setItems] = useState<OperatorNotification[]>([]);
  const [status, setStatus] = useState<"connecting" | "live" | "error">("connecting");
  const [error, setError] = useState("");

  async function load() {
    setError("");
    try {
      const payload = await getJSON<OperatorNotification[]>(token, "/v1/operator/notifications", { limit: 100 });
      setItems(payload);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setStatus("error");
    }
  }

  useEffect(() => {
    void load();
    const controller = new AbortController();
    setStatus("connecting");
    streamSSE(token, "/v1/operator/notifications/stream", (message) => {
      if (message.event !== "notification") {
        return;
      }
      try {
        const item = JSON.parse(message.data) as OperatorNotification;
        setItems((current) => {
          const next = [item, ...current.filter((existing) => existing.id !== item.id)];
          next.sort((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at));
          return next.slice(0, 100);
        });
        setStatus("live");
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        setStatus("error");
      }
    }, controller.signal).catch((err) => {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : String(err));
        setStatus("error");
      }
    });
    return () => controller.abort();
  }, [token]);

  const grouped = useMemo(() => {
    const buckets = new Map<string, OperatorNotification[]>();
    for (const item of items) {
      const key = item.status || "active";
      buckets.set(key, [...(buckets.get(key) ?? []), item]);
    }
    return Array.from(buckets.entries());
  }, [items]);

  return (
    <>
      <PageHeader
        eyebrow="Notifications"
        title="Operator attention feed"
        summary="Approvals, failed executions, lifecycle alerts, and takeover events stream into a single operator inbox."
        actions={
          <>
            <Pill label={status === "live" ? "Live stream" : status === "connecting" ? "Connecting" : "Stream error"} tone={status === "error" ? "attention" : status === "live" ? "positive" : "neutral"} />
            <button className="button button--primary" type="button" onClick={() => void load()}>
              Refresh
            </button>
          </>
        }
      />
      {error ? <div className="banner banner--error">{error}</div> : null}
      <div className="workspace-grid">
        {grouped.map(([statusKey, notifications]) => (
          <section className="section" key={statusKey}>
            <header className="section__header">
              <div>
                <p className="section__eyebrow">Status</p>
                <h2>{statusKey}</h2>
                <p className="section__summary">{notifications.length} notification items.</p>
              </div>
            </header>
            <div className="section__body">
              <div className="notification-list">
                {notifications.map((item) => (
                  <div className="notification-card" key={item.id}>
                    <div className="notification-card__meta">
                      <Pill label={item.severity || "info"} tone={item.severity === "error" ? "attention" : item.severity === "warning" ? "attention" : "positive"} />
                      <span>{formatDate(item.created_at)}</span>
                    </div>
                    <h3>{item.title}</h3>
                    <p className="muted">{item.kind}</p>
                    <div className="notification-card__links">
                      {item.session_id ? (
                        <Link className="button button--ghost" to={`/sessions/${item.session_id}`}>
                          Open session
                        </Link>
                      ) : null}
                      {item.agent_id ? (
                        <Link className="button button--ghost" to={`/agents/${item.agent_id}`}>
                          Open agent
                        </Link>
                      ) : null}
                    </div>
                  </div>
                ))}
                {notifications.length === 0 ? <div className="data-table__empty">No notifications in this bucket.</div> : null}
              </div>
            </div>
          </section>
        ))}
      </div>
    </>
  );
}
