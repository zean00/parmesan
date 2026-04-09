import { useEffect, useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { JsonBlock } from "../components/JsonBlock";
import { KeyValueList } from "../components/KeyValueList";
import { PageHeader } from "../components/PageHeader";
import { Pill } from "../components/Pill";
import { Section } from "../components/Section";
import { getJSON, postJSON } from "../lib/api";
import { formatDate } from "../lib/format";
import { streamSSE } from "../lib/sse";
import type { JSONObject } from "../types";

type ACPEvent = {
  id: string;
  source: string;
  kind: string;
  execution_id?: string;
  created_at: string;
  content?: Array<{ type: string; text?: string }>;
  data?: JSONObject;
};

type ACPSession = {
  id: string;
  agent_id?: string;
  customer_id?: string;
  channel?: string;
  summary?: JSONObject;
};

export function AgentTestPage({ token }: { token: string }) {
  const { agentId = "" } = useParams();
  const [session, setSession] = useState<ACPSession | null>(null);
  const [events, setEvents] = useState<ACPEvent[]>([]);
  const [message, setMessage] = useState("");
  const [customerId, setCustomerId] = useState("dashboard-test");
  const [channel, setChannel] = useState("web");
  const [executionID, setExecutionID] = useState("");
  const [resolvedPolicy, setResolvedPolicy] = useState<JSONObject>({});
  const [quality, setQuality] = useState<JSONObject>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  async function createSession() {
    setBusy("create");
    setError("");
    try {
      const created = await postJSON<ACPSession>(token, "/v1/acp/sessions", {
        agent_id: agentId,
        customer_id: customerId,
        channel,
        title: `Dashboard test ${new Date().toISOString()}`,
      });
      setSession(created);
      setEvents([]);
      setExecutionID("");
      setResolvedPolicy({});
      setQuality({});
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function sendMessage() {
    if (!session || !message.trim()) return;
    setBusy("send");
    setError("");
    try {
      await postJSON(token, `/v1/acp/sessions/${session.id}/messages`, { text: message.trim(), source: "customer" });
      setMessage("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function loadExecutionDetails(nextExecutionID: string) {
    if (!nextExecutionID) return;
    try {
      const [resolvedPayload, qualityPayload] = await Promise.all([
        getJSON<JSONObject>(token, `/v1/executions/${nextExecutionID}/resolved-policy`),
        getJSON<JSONObject>(token, `/v1/executions/${nextExecutionID}/quality`),
      ]);
      setResolvedPolicy(resolvedPayload);
      setQuality(qualityPayload);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  useEffect(() => {
    if (!session) return;
    const controller = new AbortController();
    streamSSE(token, `/v1/acp/sessions/${session.id}/events/stream`, (messageEvent) => {
      try {
        const event = JSON.parse(messageEvent.data) as ACPEvent;
        setEvents((current) => {
          const next = [...current.filter((item) => item.id !== event.id), event];
          next.sort((left, right) => Date.parse(left.created_at) - Date.parse(right.created_at));
          return next;
        });
        const nextExecutionID = event.execution_id ?? (typeof event.data?.execution_id === "string" ? event.data.execution_id : "");
        if (nextExecutionID) {
          setExecutionID(nextExecutionID);
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    }, controller.signal).catch((err) => {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : String(err));
      }
    });
    return () => controller.abort();
  }, [session, token]);

  useEffect(() => {
    void loadExecutionDetails(executionID);
  }, [executionID]);

  const summary = useMemo(() => (session?.summary ?? {}) as JSONObject, [session]);

  return (
    <>
      <PageHeader
        eyebrow="Test console"
        title={agentId ? `Chat with ${agentId}` : "Agent test console"}
        summary="Create an ACP session, send messages, and inspect resolved policy and quality for the active execution."
        actions={
          <>
            {session ? <Pill label="Session ready" tone="positive" /> : <Pill label="No session" />}
            <button className="button button--primary" disabled={busy !== ""} type="button" onClick={() => void createSession()}>
              New test session
            </button>
          </>
        }
      />
      {error ? <div className="banner banner--error">{error}</div> : null}
      <div className="workspace-grid">
        <Section eyebrow="Session" title="Test session" summary="Operator-owned ACP session for validating policy behavior.">
          <div className="section-grid">
            <div className="query-form">
              <label>
                <span>Customer</span>
                <input value={customerId} onChange={(event) => setCustomerId(event.target.value)} />
              </label>
              <label>
                <span>Channel</span>
                <input value={channel} onChange={(event) => setChannel(event.target.value)} />
              </label>
            </div>
            <KeyValueList
              entries={[
                ["Session", session?.id || "n/a"],
                ["Agent", agentId || "n/a"],
                ["Execution", executionID || "n/a"],
                ["Last trace", (summary.last_trace_id as string | undefined) || "n/a"],
              ]}
            />
          </div>
        </Section>
        <Section eyebrow="Chat" title="Conversation" summary="Send a customer message into the live runtime and watch ACP events arrive in order.">
          <div className="action-card">
            <textarea value={message} onChange={(event) => setMessage(event.target.value)} placeholder="Ask the agent something" rows={4} />
            <div className="inline-actions">
              <button className="button button--primary" disabled={!session || !message.trim() || busy !== ""} type="button" onClick={() => void sendMessage()}>
                Send message
              </button>
            </div>
          </div>
          <div className="timeline">
            {events.map((event) => (
              <article className="timeline__item" key={event.id}>
                <div className="timeline__meta">
                  <Pill label={event.source || "event"} />
                  <span>{event.kind}</span>
                  <span>{formatDate(event.created_at)}</span>
                </div>
                <div className="timeline__body">
                  {event.content?.map((part, index) =>
                    part.type === "text" && part.text ? (
                      <p key={`${event.id}:${index}`}>{part.text}</p>
                    ) : null,
                  )}
                  {event.content?.length ? null : <JsonBlock value={event.data ?? {}} />}
                </div>
              </article>
            ))}
            {events.length === 0 ? <div className="data-table__empty">No ACP events yet.</div> : null}
          </div>
        </Section>
        <Section eyebrow="Inspect" title="Resolved policy and quality" summary="Current execution side panels for fast policy testing.">
          <div className="section-grid">
            <JsonBlock value={resolvedPolicy} />
            <JsonBlock value={quality} />
          </div>
        </Section>
      </div>
    </>
  );
}
