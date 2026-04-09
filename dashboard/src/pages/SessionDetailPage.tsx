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
import type { ExecutionPayload, JSONObject, SessionEvent, SessionView } from "../types";

type ExecutionBundle = {
  payload: ExecutionPayload | null;
  resolvedPolicy: JSONObject | null;
  quality: JSONObject | null;
  explain: JSONObject | null;
  toolRuns: JSONObject[] | null;
  deliveryAttempts: JSONObject[] | null;
};

export function SessionDetailPage({ token }: { token: string }) {
  const { sessionId = "" } = useParams();
  const [session, setSession] = useState<SessionView | null>(null);
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [lifecycle, setLifecycle] = useState<JSONObject>({});
  const [teachingState, setTeachingState] = useState<JSONObject>({});
  const [latestExecutionID, setLatestExecutionID] = useState("");
  const [execution, setExecution] = useState<ExecutionBundle>({
    payload: null,
    resolvedPolicy: null,
    quality: null,
    explain: null,
    toolRuns: null,
    deliveryAttempts: null,
  });
  const [messageText, setMessageText] = useState("");
  const [noteText, setNoteText] = useState("");
  const [feedbackText, setFeedbackText] = useState("");
  const [sending, setSending] = useState("");
  const [error, setError] = useState("");
  const [streamStatus, setStreamStatus] = useState<"connecting" | "live" | "error">("connecting");

  async function loadSession() {
    if (!sessionId) return;
    setError("");
    try {
      const [sessionPayload, eventsPayload, lifecyclePayload, teachingPayload] = await Promise.all([
        getJSON<SessionView>(token, `/v1/operator/sessions/${sessionId}`),
        getJSON<SessionEvent[]>(token, `/v1/operator/sessions/${sessionId}/events`),
        getJSON<JSONObject>(token, `/v1/operator/sessions/${sessionId}/lifecycle`),
        getJSON<JSONObject>(token, `/v1/operator/sessions/${sessionId}/teaching-state`).catch(() => ({})),
      ]);
      setSession(sessionPayload);
      setEvents(eventsPayload);
      setLifecycle(lifecyclePayload);
      setTeachingState(teachingPayload);
      const summary = (sessionPayload.summary ?? {}) as JSONObject;
      const nextExecutionID = typeof summary.last_execution_id === "string" ? summary.last_execution_id : "";
      setLatestExecutionID(nextExecutionID);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function loadExecution(executionID: string) {
    if (!executionID) {
      setExecution({
        payload: null,
        resolvedPolicy: null,
        quality: null,
        explain: null,
        toolRuns: null,
        deliveryAttempts: null,
      });
      return;
    }
    try {
      const [payload, resolvedPolicy, quality, explain, toolRuns, deliveryAttempts] = await Promise.all([
        getJSON<ExecutionPayload>(token, `/v1/executions/${executionID}`),
        getJSON<JSONObject>(token, `/v1/executions/${executionID}/resolved-policy`),
        getJSON<JSONObject>(token, `/v1/executions/${executionID}/quality`),
        getJSON<JSONObject>(token, `/v1/executions/${executionID}/explain`),
        getJSON<JSONObject[]>(token, `/v1/executions/${executionID}/tool-runs`),
        getJSON<JSONObject[]>(token, `/v1/executions/${executionID}/delivery-attempts`),
      ]);
      setExecution({ payload, resolvedPolicy, quality, explain, toolRuns, deliveryAttempts });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function mutate(action: string, run: () => Promise<unknown>) {
    setSending(action);
    setError("");
    try {
      await run();
      await loadSession();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending("");
    }
  }

  useEffect(() => {
    void loadSession();
  }, [sessionId]);

  useEffect(() => {
    void loadExecution(latestExecutionID);
  }, [latestExecutionID]);

  useEffect(() => {
    if (!sessionId) return;
    const controller = new AbortController();
    setStreamStatus("connecting");
    streamSSE(token, `/v1/operator/sessions/${sessionId}/stream`, (message) => {
      try {
        const event = JSON.parse(message.data) as SessionEvent;
        setEvents((current) => {
          const next = [...current.filter((item) => item.id !== event.id), event];
          next.sort((left, right) => (left.offset ?? 0) - (right.offset ?? 0));
          return next;
        });
        setStreamStatus("live");
        void loadSession();
      } catch {
        setStreamStatus("error");
      }
    }, controller.signal).catch(() => {
      if (!controller.signal.aborted) {
        setStreamStatus("error");
      }
    });
    return () => controller.abort();
  }, [sessionId, token]);

  const summary = useMemo(() => (session?.summary ?? {}) as JSONObject, [session]);

  return (
    <>
      <PageHeader
        eyebrow="Session workspace"
        title={session?.title || sessionId || "Session detail"}
        summary="Transcript, lifecycle, execution traces, and operator actions in one place."
        actions={
          <>
            <Pill label={streamStatus === "live" ? "Live stream" : streamStatus === "connecting" ? "Connecting" : "Stream error"} tone={streamStatus === "error" ? "attention" : streamStatus === "live" ? "positive" : "neutral"} />
            <Pill label={session?.status || "unknown"} tone={session?.status === "closed" ? "neutral" : "positive"} />
          </>
        }
      />
      {error ? <div className="banner banner--error">{error}</div> : null}
      <div className="workspace-grid">
        <Section eyebrow="Session" title="Overview" summary="Identity, assignment, lifecycle, and last execution details.">
          <div className="section-grid">
            <KeyValueList
              entries={[
                ["Session", session?.id || sessionId || "n/a"],
                ["Agent", session?.agent_id || "n/a"],
                ["Customer", session?.customer_id || "n/a"],
                ["Mode", session?.mode || "n/a"],
                ["Status", session?.status || "n/a"],
                ["Assigned operator", session?.assigned_operator_id || "n/a"],
                ["Last execution", latestExecutionID || "n/a"],
                ["Last activity", formatDate(session?.last_activity_at)],
              ]}
            />
            <JsonBlock value={summary} />
          </div>
        </Section>

        <Section eyebrow="Actions" title="Operator controls" summary="Take over, resume, reply, keep or close, add notes, and compile session feedback.">
          <div className="action-grid">
            <div className="action-card">
              <h3>Takeover and resume</h3>
              <div className="inline-actions">
                <button className="button button--primary" disabled={sending !== ""} type="button" onClick={() => void mutate("takeover", () => postJSON(token, `/v1/operator/sessions/${sessionId}/takeover`, { reason: "dashboard takeover" }))}>
                  Take over
                </button>
                <button className="button button--ghost" disabled={sending !== ""} type="button" onClick={() => void mutate("resume", () => postJSON(token, `/v1/operator/sessions/${sessionId}/resume`, { reason: "dashboard resume" }))}>
                  Resume auto
                </button>
              </div>
            </div>
            <div className="action-card">
              <h3>Lifecycle</h3>
              <div className="inline-actions">
                <button className="button button--ghost" disabled={sending !== ""} type="button" onClick={() => void mutate("keep", () => postJSON(token, `/v1/operator/sessions/${sessionId}/keep`, { reason: "operator_keep" }))}>
                  Keep open
                </button>
                <button className="button button--ghost" disabled={sending !== ""} type="button" onClick={() => void mutate("close", () => postJSON(token, `/v1/operator/sessions/${sessionId}/close`, { reason: "operator_closed" }))}>
                  Close session
                </button>
              </div>
            </div>
            <div className="action-card">
              <h3>Visible reply</h3>
              <textarea value={messageText} onChange={(event) => setMessageText(event.target.value)} placeholder="Reply to the customer" rows={4} />
              <div className="inline-actions">
                <button
                  className="button button--primary"
                  disabled={sending !== "" || !messageText.trim()}
                  type="button"
                  onClick={() =>
                    void mutate("message", async () => {
                      await postJSON(token, `/v1/operator/sessions/${sessionId}/messages`, { text: messageText });
                      setMessageText("");
                    })
                  }
                >
                  Send operator reply
                </button>
                <button
                  className="button button--ghost"
                  disabled={sending !== "" || !messageText.trim()}
                  type="button"
                  onClick={() =>
                    void mutate("message", async () => {
                      await postJSON(token, `/v1/operator/sessions/${sessionId}/messages/on-behalf`, { text: messageText });
                      setMessageText("");
                    })
                  }
                >
                  Send on behalf of agent
                </button>
              </div>
            </div>
            <div className="action-card">
              <h3>Internal note</h3>
              <textarea value={noteText} onChange={(event) => setNoteText(event.target.value)} placeholder="Add an internal note" rows={4} />
              <div className="inline-actions">
                <button
                  className="button button--ghost"
                  disabled={sending !== "" || !noteText.trim()}
                  type="button"
                  onClick={() =>
                    void mutate("note", async () => {
                      await postJSON(token, `/v1/operator/sessions/${sessionId}/notes`, { text: noteText });
                      setNoteText("");
                    })
                  }
                >
                  Save note
                </button>
              </div>
            </div>
            <div className="action-card">
              <h3>Feedback</h3>
              <textarea value={feedbackText} onChange={(event) => setFeedbackText(event.target.value)} placeholder="Describe what the agent should learn from this session" rows={4} />
              <div className="inline-actions">
                <button
                  className="button button--primary"
                  disabled={sending !== "" || !feedbackText.trim()}
                  type="button"
                  onClick={() =>
                    void mutate("feedback", async () => {
                      await postJSON(token, `/v1/operator/sessions/${sessionId}/feedback`, { text: feedbackText });
                      setFeedbackText("");
                    })
                  }
                >
                  Submit feedback
                </button>
              </div>
            </div>
            <div className="action-card">
              <h3>Execution recovery</h3>
              <div className="inline-actions">
                <button
                  className="button button--ghost"
                  disabled={sending !== "" || !latestExecutionID}
                  type="button"
                  onClick={() => void mutate("retry", () => postJSON(token, `/v1/operator/executions/${latestExecutionID}/retry`, {}))}
                >
                  Retry
                </button>
                <button
                  className="button button--ghost"
                  disabled={sending !== "" || !latestExecutionID}
                  type="button"
                  onClick={() => void mutate("unblock", () => postJSON(token, `/v1/operator/executions/${latestExecutionID}/unblock`, {}))}
                >
                  Unblock
                </button>
                <button
                  className="button button--ghost"
                  disabled={sending !== "" || !latestExecutionID}
                  type="button"
                  onClick={() => void mutate("abandon", () => postJSON(token, `/v1/operator/executions/${latestExecutionID}/abandon`, {}))}
                >
                  Abandon
                </button>
              </div>
            </div>
          </div>
        </Section>

        <Section eyebrow="Transcript" title="Conversation timeline" summary="Live session event stream including operator actions and internal notes.">
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
                  {event.content?.length ? null : <p className="muted">No text body.</p>}
                </div>
              </article>
            ))}
            {events.length === 0 ? <div className="data-table__empty">No events loaded.</div> : null}
          </div>
        </Section>

        <Section eyebrow="Lifecycle" title="Session state and teaching" summary="Lifecycle watches, status, and feedback-derived outputs for this session.">
          <div className="section-grid">
            <JsonBlock value={lifecycle} />
            <JsonBlock value={teachingState} />
          </div>
        </Section>

        <Section eyebrow="Execution" title="Current execution trace" summary="Resolved policy, quality, explain payload, tool runs, and delivery attempts for the latest execution.">
          <div className="section-grid">
            <KeyValueList
              entries={[
                ["Execution", latestExecutionID || "n/a"],
                ["Trace", (execution.payload?.execution?.trace_id as string | undefined) || "n/a"],
                ["Status", (execution.payload?.execution?.status as string | undefined) || "n/a"],
                ["Policy snapshot", (execution.payload?.execution?.policy_snapshot_id as string | undefined) || "n/a"],
              ]}
            />
            <JsonBlock value={execution.payload} />
          </div>
          <div className="workspace-grid workspace-grid--compact">
            <Section title="Resolved policy">
              <JsonBlock value={execution.resolvedPolicy} />
            </Section>
            <Section title="Quality">
              <JsonBlock value={execution.quality} />
            </Section>
            <Section title="Explain">
              <JsonBlock value={execution.explain} />
            </Section>
            <Section title="Tool runs">
              <JsonBlock value={execution.toolRuns} />
            </Section>
            <Section title="Delivery attempts">
              <JsonBlock value={execution.deliveryAttempts} />
            </Section>
          </div>
        </Section>
      </div>
    </>
  );
}
