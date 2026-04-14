import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { Pill } from "../components/Pill";
import { getJSON } from "../lib/api";
import { formatDate } from "../lib/format";
import { streamSSE } from "../lib/sse";
import type { OperatorNotification } from "../types";

const browserAlertsStorageKey = "parmesan.notifications.browser.enabled";

export function NotificationsPage({ token }: { token: string }) {
  const [items, setItems] = useState<OperatorNotification[]>([]);
  const [status, setStatus] = useState<"connecting" | "live" | "error">("connecting");
  const [error, setError] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [severityFilter, setSeverityFilter] = useState("");
  const [kindFilter, setKindFilter] = useState("");
  const [browserAlertsEnabled, setBrowserAlertsEnabled] = useState<boolean>(() => localStorage.getItem(browserAlertsStorageKey) === "true");
  const [permission, setPermission] = useState<NotificationPermission>(() => notificationPermission());
  const seenNotificationIDs = useRef<Set<string>>(new Set());

  async function load() {
    setError("");
    try {
      const payload = await getJSON<OperatorNotification[]>(token, "/v1/operator/notifications", { limit: 100 });
      setItems(payload);
      seenNotificationIDs.current = new Set(payload.map((item) => item.id));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setStatus("error");
    }
  }

  async function enableBrowserAlerts() {
    if (typeof window === "undefined" || !("Notification" in window)) {
      setError("Browser notifications are not supported in this browser.");
      return;
    }
    const next = await Notification.requestPermission();
    setPermission(next);
    if (next === "granted") {
      localStorage.setItem(browserAlertsStorageKey, "true");
      setBrowserAlertsEnabled(true);
      setError("");
      return;
    }
    localStorage.removeItem(browserAlertsStorageKey);
    setBrowserAlertsEnabled(false);
  }

  function disableBrowserAlerts() {
    localStorage.removeItem(browserAlertsStorageKey);
    setBrowserAlertsEnabled(false);
  }

  function sendTestBrowserAlert() {
    if (!browserAlertsEnabled || permission !== "granted" || typeof window === "undefined" || !("Notification" in window)) {
      return;
    }
    const notification = new Notification("Parmesan operator alert test", {
      body: "Desktop alerts are active for new operator notifications.",
      tag: "parmesan-browser-alert-test",
    });
    notification.onclick = () => window.focus();
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
        if (!seenNotificationIDs.current.has(item.id)) {
          seenNotificationIDs.current.add(item.id);
          maybeShowBrowserNotification(item, browserAlertsEnabled, permission);
        }
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
  }, [token, browserAlertsEnabled, permission]);

  useEffect(() => {
    if (typeof window === "undefined" || !("Notification" in window)) {
      return undefined;
    }
    const syncPermission = () => setPermission(Notification.permission);
    window.addEventListener("focus", syncPermission);
    document.addEventListener("visibilitychange", syncPermission);
    return () => {
      window.removeEventListener("focus", syncPermission);
      document.removeEventListener("visibilitychange", syncPermission);
    };
  }, []);

  const filteredItems = useMemo(() => {
    return items.filter((item) => {
      if (statusFilter && item.status !== statusFilter) {
        return false;
      }
      if (severityFilter && item.severity !== severityFilter) {
        return false;
      }
      if (kindFilter && !item.kind.toLowerCase().includes(kindFilter.toLowerCase())) {
        return false;
      }
      return true;
    });
  }, [items, statusFilter, severityFilter, kindFilter]);

  const grouped = useMemo(() => {
    const buckets = new Map<string, OperatorNotification[]>();
    for (const item of filteredItems) {
      const key = item.status || "active";
      buckets.set(key, [...(buckets.get(key) ?? []), item]);
    }
    return Array.from(buckets.entries());
  }, [filteredItems]);
  const openCount = filteredItems.filter((item) => item.status === "open").length;
  const criticalCount = filteredItems.filter((item) => {
    const severity = (item.severity || "").toLowerCase();
    return severity === "critical" || severity === "error";
  }).length;
  const browserSupported = typeof window !== "undefined" && "Notification" in window;
  const kinds = Array.from(new Set(items.map((item) => item.kind))).sort();
  const browserStateLabel = browserSupported ? permission : "unsupported";
  const browserStateSummary = !browserSupported
    ? "This browser cannot display desktop alerts."
    : permission === "denied"
      ? "Desktop alerts are blocked in browser settings."
      : browserAlertsEnabled
        ? "Desktop alerts are enabled for new feed items while the tab is in the background."
        : "Desktop alerts are currently off.";

  return (
    <>
      <PageHeader
        eyebrow="Notifications"
        title="Operator attention feed"
        summary="Approvals, failed executions, lifecycle alerts, and takeovers in one operator feed."
        actions={
          <>
            <Pill label={status === "live" ? "Live stream" : status === "connecting" ? "Connecting" : "Stream error"} tone={status === "error" ? "attention" : status === "live" ? "positive" : "neutral"} />
            <Pill label={`${openCount} open`} tone={openCount > 0 ? "attention" : "neutral"} />
            <button className="button button--primary" type="button" onClick={() => void load()}>
              Refresh
            </button>
          </>
        }
      />
      {error ? <div className="banner banner--error">{error}</div> : null}
      <div className="panel-form">
        <div className="stack-heading">
          <p className="stack-heading__eyebrow">Delivery</p>
          <h3>Feed controls</h3>
          <p>Filter the live feed and optionally raise browser notifications while the dashboard is open.</p>
        </div>
        <div className="filters-grid">
          <label>
            <span>Status</span>
            <select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}>
              <option value="">All states</option>
              <option value="open">Open</option>
              <option value="resolved">Resolved</option>
              <option value="active">Active</option>
            </select>
          </label>
          <label>
            <span>Severity</span>
            <select value={severityFilter} onChange={(event) => setSeverityFilter(event.target.value)}>
              <option value="">All severities</option>
              <option value="critical">Critical</option>
              <option value="attention">Attention</option>
              <option value="warning">Warning</option>
              <option value="error">Error</option>
              <option value="info">Info</option>
              <option value="neutral">Neutral</option>
            </select>
          </label>
          <label>
            <span>Kind</span>
            <input list="notification-kind-options" value={kindFilter} onChange={(event) => setKindFilter(event.target.value)} placeholder="moderation, approval, media" />
          </label>
          <div className="surface-panel">
            <div className="stack-heading">
              <p className="stack-heading__eyebrow">Browser</p>
              <h3>{browserStateLabel}</h3>
              <p>{browserStateSummary}</p>
            </div>
          </div>
          <div className="surface-panel">
            <div className="stack-heading">
              <p className="stack-heading__eyebrow">Actions</p>
              <h3>Notification delivery</h3>
            </div>
            <div className="inline-actions">
              <button className="button button--primary" type="button" onClick={() => void enableBrowserAlerts()} disabled={!browserSupported || permission === "denied"}>
                Enable browser alerts
              </button>
              <button className="button button--ghost" type="button" onClick={disableBrowserAlerts} disabled={!browserAlertsEnabled}>
                Disable alerts
              </button>
              <button className="button button--ghost" type="button" onClick={sendTestBrowserAlert} disabled={!browserAlertsEnabled || permission !== "granted"}>
                Send test alert
              </button>
              <button
                className="button button--ghost"
                type="button"
                onClick={() => {
                  setStatusFilter("");
                  setSeverityFilter("");
                  setKindFilter("");
                }}
                disabled={!statusFilter && !severityFilter && !kindFilter}
              >
                Clear filters
              </button>
            </div>
          </div>
        </div>
      </div>
      <datalist id="notification-kind-options">
        {kinds.map((kind) => (
          <option key={kind} value={kind} />
        ))}
      </datalist>
      <div className="metric-strip">
        <div className="metric">
          <span>Total</span>
          <strong>{filteredItems.length}</strong>
        </div>
        <div className="metric">
          <span>Open</span>
          <strong>{openCount}</strong>
        </div>
        <div className="metric">
          <span>Critical</span>
          <strong>{criticalCount}</strong>
        </div>
        <div className="metric">
          <span>Browser alerts</span>
          <strong>{browserAlertsEnabled && permission === "granted" ? "On" : "Off"}</strong>
        </div>
      </div>
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
                      <Pill label={item.severity || "info"} tone={notificationTone(item.severity)} />
                      <Pill label={item.kind} tone="neutral" />
                      <span>{formatDate(item.created_at)}</span>
                    </div>
                    <h3>{item.title}</h3>
                    {notificationSummary(item) ? <p className="muted">{notificationSummary(item)}</p> : null}
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
      {grouped.length === 0 ? <div className="data-table__empty">No notifications match the current filters.</div> : null}
    </>
  );
}

function notificationTone(severity: string): "neutral" | "positive" | "attention" | "danger" {
  switch ((severity || "").toLowerCase()) {
    case "critical":
    case "error":
      return "danger";
    case "warning":
    case "attention":
      return "attention";
    case "info":
    case "neutral":
      return "neutral";
    default:
      return "positive";
  }
}

function maybeShowBrowserNotification(
  item: OperatorNotification,
  enabled: boolean,
  permission: NotificationPermission,
) {
  if (!enabled || permission !== "granted" || typeof window === "undefined" || !("Notification" in window)) {
    return;
  }
  if (!document.hidden) {
    return;
  }
  const notification = new Notification(item.title, {
    body: notificationSummary(item) || item.kind,
    tag: item.id,
  });
  notification.onclick = () => {
    window.focus();
    if (item.session_id) {
      window.location.assign(`/sessions/${item.session_id}`);
      return;
    }
    if (item.agent_id) {
      window.location.assign(`/agents/${item.agent_id}`);
    }
  };
}

function notificationPermission(): NotificationPermission {
  if (typeof window === "undefined" || !("Notification" in window)) {
    return "denied";
  }
  return Notification.permission;
}

function notificationSummary(item: OperatorNotification): string {
  const payload = item.payload ?? {};
  if (typeof payload.error === "string" && payload.error) {
    return payload.error;
  }
  if (typeof payload.blocked_reason === "string" && payload.blocked_reason) {
    return `Blocked: ${payload.blocked_reason}`;
  }
  if (typeof payload.reason === "string" && payload.reason) {
    return payload.reason;
  }
  if (Array.isArray(payload.categories) && payload.categories.length > 0) {
    return `Categories: ${payload.categories.map((entry) => String(entry)).join(", ")}`;
  }
  if (typeof payload.decision === "string" && payload.decision) {
    return `Decision: ${payload.decision}`;
  }
  return "";
}
