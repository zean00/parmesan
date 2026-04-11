import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { InspectPanel } from "../components/InspectPanel";
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
  title?: string;
  summary?: JSONObject;
};

type ScenarioPreset = {
  id: string;
  label: string;
  title: string;
  customerId: string;
  channel: string;
  message: string;
  metadata?: JSONObject;
  meta?: JSONObject;
};

const scenarioPresets: ScenarioPreset[] = [
  {
    id: "baseline",
    label: "Baseline support",
    title: "Baseline web support check",
    customerId: "dashboard-test",
    channel: "web",
    message: "Hi, I need help understanding my current order status.",
  },
  {
    id: "vip",
    label: "VIP context",
    title: "VIP customer policy check",
    customerId: "vip-customer",
    channel: "web",
    message: "I need a refund and I expect priority handling.",
    meta: {
      customer_context: {
        tier: "vip",
        region: "apac",
      },
    },
  },
  {
    id: "handover",
    label: "Operator handover",
    title: "Escalation and handover validation",
    customerId: "handover-check",
    channel: "chat",
    message: "Please hand me over to a human operator now.",
    metadata: {
      test_case: "handover",
    },
  },
  {
    id: "policy-edge",
    label: "Policy edge",
    title: "Out-of-policy exception test",
    customerId: "policy-edge",
    channel: "web",
    message: "My return is outside the normal window, but the package arrived damaged. What can you do?",
    metadata: {
      test_case: "exception-path",
    },
  },
];

export function AgentTestPage({ token }: { token: string }) {
  const { agentId = "" } = useParams();
  const [session, setSession] = useState<ACPSession | null>(null);
  const [events, setEvents] = useState<ACPEvent[]>([]);
  const [message, setMessage] = useState("");
  const [title, setTitle] = useState("Dashboard test session");
  const [customerId, setCustomerId] = useState("dashboard-test");
  const [channel, setChannel] = useState("web");
  const [metadataText, setMetadataText] = useState("");
  const [metaText, setMetaText] = useState("");
  const [eventSourceFilter, setEventSourceFilter] = useState("");
  const [eventKindFilter, setEventKindFilter] = useState("");
  const [streamStatus, setStreamStatus] = useState<"idle" | "connecting" | "live" | "error">("idle");
  const [executionID, setExecutionID] = useState("");
  const [resolvedPolicy, setResolvedPolicy] = useState<JSONObject>({});
  const [quality, setQuality] = useState<JSONObject>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  async function createSession() {
    setBusy("create");
    setError("");
    try {
      const metadata = parseOptionalJSONObject(metadataText, "Session metadata");
      const meta = parseOptionalJSONObject(metaText, "ACP _meta");
      const created = await postJSON<ACPSession>(token, agentId ? `/v1/acp/agents/${agentId}/sessions` : "/v1/acp/sessions", {
        agent_id: agentId || undefined,
        customer_id: customerId,
        channel,
        title: title.trim() || `Dashboard test ${new Date().toISOString()}`,
        metadata,
        _meta: meta,
      });
      setSession(created);
      setEvents([]);
      setExecutionID("");
      setResolvedPolicy({});
      setQuality({});
      setStreamStatus("connecting");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function sendMessage(nextMessage?: string) {
    const outgoing = (nextMessage ?? message).trim();
    if (!session || !outgoing) return;
    setBusy("send");
    setError("");
    try {
      await postJSON(token, `/v1/acp/sessions/${session.id}/messages`, { text: outgoing, source: "customer" });
      if (!nextMessage) {
        setMessage("");
      }
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
    setStreamStatus("connecting");
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
        setStreamStatus("live");
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        setStreamStatus("error");
      }
    }, controller.signal).catch((err) => {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : String(err));
        setStreamStatus("error");
      }
    });
    return () => controller.abort();
  }, [session, token]);

  useEffect(() => {
    void loadExecutionDetails(executionID);
  }, [executionID, token]);

  const summary = useMemo(() => (session?.summary ?? {}) as JSONObject, [session]);
  const filteredEvents = useMemo(() => {
    return events.filter((event) => {
      if (eventSourceFilter && event.source !== eventSourceFilter) {
        return false;
      }
      if (eventKindFilter && !event.kind.toLowerCase().includes(eventKindFilter.toLowerCase())) {
        return false;
      }
      return true;
    });
  }, [eventKindFilter, eventSourceFilter, events]);
  const latestEvent = filteredEvents[filteredEvents.length - 1];
  const assistantCount = events.filter((event) => event.source === "assistant").length;
  const customerCount = events.filter((event) => event.source === "customer").length;
  const systemCount = events.length - assistantCount - customerCount;

  function applyPreset(preset: ScenarioPreset) {
    setTitle(preset.title);
    setCustomerId(preset.customerId);
    setChannel(preset.channel);
    setMessage(preset.message);
    setMetadataText(preset.metadata ? JSON.stringify(preset.metadata, null, 2) : "");
    setMetaText(preset.meta ? JSON.stringify(preset.meta, null, 2) : "");
  }

  return (
    <>
      <PageHeader
        eyebrow="Test console"
        title={agentId ? `Chat with ${agentId}` : "Agent test console"}
        summary="Create an isolated ACP test session, shape the starting context, and inspect runtime policy and quality signals while the event stream is live."
        actions={
          <>
            <Pill label={session ? "Session ready" : "No session"} tone={session ? "positive" : "neutral"} />
            <Pill label={streamStatus === "live" ? "Stream live" : streamStatus === "connecting" ? "Connecting" : streamStatus === "error" ? "Stream error" : "Idle"} tone={streamStatus === "error" ? "danger" : streamStatus === "live" ? "positive" : "neutral"} />
            <button className="button button--primary" disabled={busy !== ""} type="button" onClick={() => void createSession()}>
              New test session
            </button>
          </>
        }
      />
      {error ? <div className="banner banner--error">{error}</div> : null}
      <div className="metric-strip">
        <div className="metric">
          <span>Total events</span>
          <strong>{events.length}</strong>
        </div>
        <div className="metric">
          <span>Assistant</span>
          <strong>{assistantCount}</strong>
        </div>
        <div className="metric">
          <span>Customer</span>
          <strong>{customerCount}</strong>
        </div>
        <div className="metric">
          <span>System</span>
          <strong>{systemCount}</strong>
        </div>
      </div>
      <div className="workspace-columns">
        <div className="stack">
          <Section eyebrow="Scenarios" title="Session setup" summary="Choose a scenario, adjust session context, and create an isolated ACP session without touching the live operator inbox.">
            <div className="stack">
              <div className="preset-grid">
                {scenarioPresets.map((preset) => (
                  <button className="button button--ghost preset-button" key={preset.id} type="button" onClick={() => applyPreset(preset)}>
                    {preset.label}
                  </button>
                ))}
              </div>
              <div className="input-cluster input-cluster--triple">
                <label>
                  <span>Session title</span>
                  <input value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Dashboard test session" />
                </label>
                <label>
                  <span>Customer</span>
                  <input value={customerId} onChange={(event) => setCustomerId(event.target.value)} />
                </label>
                <label>
                  <span>Channel</span>
                  <input value={channel} onChange={(event) => setChannel(event.target.value)} />
                </label>
              </div>
              <div className="input-cluster input-cluster--double">
                <label>
                  <span>Session metadata JSON</span>
                  <textarea className="code-field" value={metadataText} onChange={(event) => setMetadataText(event.target.value)} placeholder='{"test_case":"vip-policy"}' rows={7} spellCheck={false} />
                </label>
                <label>
                  <span>ACP _meta JSON</span>
                  <textarea className="code-field" value={metaText} onChange={(event) => setMetaText(event.target.value)} placeholder='{"customer_context":{"tier":"vip"}}' rows={7} spellCheck={false} />
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
              {session ? (
                <div className="inline-actions">
                  <Link className="button button--ghost" to={`/sessions/${session.id}`}>
                    Open operator session view
                  </Link>
                </div>
              ) : null}
            </div>
          </Section>
          <Section eyebrow="Chat" title="Conversation" summary="Send customer messages into the live runtime, replay common prompts, and keep the transcript focused on the events you care about.">
            <div className="action-card">
              <textarea value={message} onChange={(event) => setMessage(event.target.value)} placeholder="Ask the agent something" rows={4} />
              <div className="preset-grid">
                {scenarioPresets.map((preset) => (
                  <button className="button button--ghost preset-button" key={`${preset.id}:message`} type="button" onClick={() => setMessage(preset.message)}>
                    {preset.label}
                  </button>
                ))}
              </div>
              <div className="inline-actions">
                <button className="button button--primary" disabled={!session || !message.trim() || busy !== ""} type="button" onClick={() => void sendMessage()}>
                  Send message
                </button>
              </div>
            </div>
            <div className="filters-grid">
              <label>
                <span>Source</span>
                <select value={eventSourceFilter} onChange={(event) => setEventSourceFilter(event.target.value)}>
                  <option value="">All sources</option>
                  <option value="customer">Customer</option>
                  <option value="assistant">Assistant</option>
                  <option value="system">System</option>
                  <option value="operator">Operator</option>
                </select>
              </label>
              <label>
                <span>Kind</span>
                <input value={eventKindFilter} onChange={(event) => setEventKindFilter(event.target.value)} placeholder="message, tool, approval" />
              </label>
              <div className="surface-panel">
                <div className="stack-heading">
                  <p className="stack-heading__eyebrow">Latest</p>
                  <h3>{latestEvent?.kind || "No events"}</h3>
                  <p>{latestEvent ? formatDate(latestEvent.created_at) : "Waiting for the first event."}</p>
                </div>
              </div>
              <div className="surface-panel">
                <div className="stack-heading">
                  <p className="stack-heading__eyebrow">Filters</p>
                  <h3>{filteredEvents.length} visible</h3>
                  <p>{events.length === filteredEvents.length ? "Showing the full event stream." : `Filtered from ${events.length} total events.`}</p>
                </div>
              </div>
            </div>
            <div className="timeline">
              {filteredEvents.map((event) => (
                <article className={`timeline__item timeline__item--${event.source === "assistant" ? "assistant" : event.source === "customer" ? "operator" : "system"}`} key={event.id}>
                  <div className="timeline__meta">
                    <Pill label={event.source || "event"} />
                    <span>{event.kind}</span>
                    <span>{formatDate(event.created_at)}</span>
                  </div>
                  <div className="timeline__body">
                    {event.content?.some((part) => part.type === "text" && part.text) ? (
                      event.content?.map((part, index) =>
                        part.type === "text" && part.text ? (
                          <p key={`${event.id}:${index}`}>{part.text}</p>
                        ) : null,
                      )
                    ) : (
                      <div className="timeline__body--system">Structured event payload available in the inspector below.</div>
                    )}
                  </div>
                </article>
              ))}
              {filteredEvents.length === 0 ? <div className="data-list__empty">No ACP events match the current filters.</div> : null}
            </div>
          </Section>
        </div>
        <div className="stack">
          <Section eyebrow="Inspect" title="Runtime inspectors" summary="Execution side panels for policy testing, session shape, and event payload debugging.">
            <div className="stack">
              <InspectPanel title="Resolved policy" summary="Policy resolution payload for the latest execution." value={resolvedPolicy} defaultOpen />
              <InspectPanel title="Quality" summary="Quality evaluation payload for the latest execution." value={quality} />
              <InspectPanel title="Current session summary" summary="ACP session summary fields returned by the API." value={summary} />
              <InspectPanel title="Session metadata" summary="Current test inputs for metadata and ACP _meta." value={{ metadata: parsePreviewJSONObject(metadataText), _meta: parsePreviewJSONObject(metaText) }} />
              <InspectPanel title="Event payloads" summary="Structured ACP event data for the current transcript." value={filteredEvents.map((event) => ({ id: event.id, kind: event.kind, data: event.data ?? {} }))} />
            </div>
          </Section>
        </div>
      </div>
    </>
  );
}

function parseOptionalJSONObject(input: string, label: string): JSONObject | undefined {
  if (!input.trim()) {
    return undefined;
  }
  const parsed = JSON.parse(input) as unknown;
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON object.`);
  }
  return parsed as JSONObject;
}

function parsePreviewJSONObject(input: string): JSONObject {
  try {
    return parseOptionalJSONObject(input, "preview") ?? {};
  } catch {
    return { invalid: input };
  }
}
