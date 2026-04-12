import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { InspectPanel } from "../components/InspectPanel";
import { KeyValueList } from "../components/KeyValueList";
import { PageHeader } from "../components/PageHeader";
import { Pill } from "../components/Pill";
import { Section } from "../components/Section";
import { getJSON } from "../lib/api";
import { formatDate, titleCase } from "../lib/format";
import { groupTraceEntries, orderedTraceGroups, summarizeTrace, traceEntryFields, traceEntryStatus, traceGroup, tracePayload, traceSummaryLine, traceTitle, traceTone, type TraceGroup } from "../lib/trace";
import { streamSSE } from "../lib/sse";
import type { SessionEvent, SessionTraceSummary, SessionView, TraceTimeline } from "../types";

export function TraceWorkspacePage({ token }: { token: string }) {
  const navigate = useNavigate();
  const { sessionId = "", traceId = "" } = useParams();
  const [session, setSession] = useState<SessionView | null>(null);
  const [traces, setTraces] = useState<SessionTraceSummary[]>([]);
  const [timeline, setTimeline] = useState<TraceTimeline | null>(null);
  const [selectedEntryID, setSelectedEntryID] = useState("");
  const [error, setError] = useState("");
  const [streamStatus, setStreamStatus] = useState<"connecting" | "live" | "error">("connecting");
  const traceRequestID = useRef(0);

  async function loadSessionState() {
    if (!sessionId) {
      return { latestTraceID: "" };
    }
    const [sessionPayload, tracePayload] = await Promise.all([
      getJSON<SessionView>(token, `/v1/operator/sessions/${sessionId}`),
      getJSON<SessionTraceSummary[]>(token, `/v1/operator/sessions/${sessionId}/traces`),
    ]);
    setSession(sessionPayload);
    setTraces(tracePayload);
    const latestTraceID = tracePayload[0]?.trace_id ?? "";
    return { latestTraceID };
  }

  async function loadTrace(targetTraceID: string, preserveSelection = false) {
    const requestID = traceRequestID.current + 1;
    traceRequestID.current = requestID;
    if (!targetTraceID) {
      if (requestID === traceRequestID.current) {
        setTimeline(null);
        setSelectedEntryID("");
      }
      return;
    }
    const payload = await getJSON<TraceTimeline>(token, `/v1/traces/${targetTraceID}`);
    if (requestID !== traceRequestID.current) {
      return;
    }
    setTimeline(payload);
    setSelectedEntryID((current) => {
      if (preserveSelection && current && payload.entries.some((entry) => entry.id === current)) {
        return current;
      }
      return payload.entries[payload.entries.length - 1]?.id ?? "";
    });
  }

  useEffect(() => {
    let cancelled = false;
    setError("");
    void (async () => {
      try {
        const { latestTraceID } = await loadSessionState();
        const resolvedTraceID = traceId || latestTraceID;
        if (!resolvedTraceID) {
          setTimeline(null);
          return;
        }
        if (!traceId && latestTraceID && !cancelled) {
          navigate(`/sessions/${sessionId}/traces/${latestTraceID}`, { replace: true });
          return;
        }
        await loadTrace(resolvedTraceID);
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err));
        }
      }
    })();
    return () => {
      cancelled = true;
      traceRequestID.current += 1;
    };
  }, [navigate, sessionId, token, traceId]);

  useEffect(() => {
    if (!sessionId) {
      return undefined;
    }
    const controller = new AbortController();
    setStreamStatus("connecting");
    streamSSE(token, `/v1/operator/sessions/${sessionId}/stream`, (message) => {
      try {
        JSON.parse(message.data) as SessionEvent;
        setStreamStatus("live");
        void (async () => {
          try {
            const { latestTraceID } = await loadSessionState();
            if (traceId && latestTraceID && traceId === latestTraceID) {
              await loadTrace(traceId, true);
            }
          } catch (err) {
            setError(err instanceof Error ? err.message : String(err));
          }
        })();
      } catch {
        setStreamStatus("error");
      }
    }, controller.signal).catch(() => {
      if (!controller.signal.aborted) {
        setStreamStatus("error");
      }
    });
    return () => controller.abort();
  }, [sessionId, token, traceId]);

  const selectedEntry = useMemo(() => {
    if (!timeline) {
      return null;
    }
    return timeline.entries.find((entry) => entry.id === selectedEntryID) ?? timeline.entries[timeline.entries.length - 1] ?? null;
  }, [selectedEntryID, timeline]);
  const groupedEntries = useMemo(() => groupTraceEntries(timeline?.entries ?? []), [timeline]);
  const traceCounts = useMemo(() => summarizeTrace(timeline), [timeline]);
  const newestTraceID = traces[0]?.trace_id ?? "";
  const newerTraceAvailable = newestTraceID !== "" && traceId !== "" && newestTraceID !== traceId;

  return (
    <>
      <PageHeader
        eyebrow="Trace workspace"
        title={session?.title ? `${session.title} trace` : traceId || "Trace detail"}
        summary="Follow one trace in sequence, jump to neighboring traces in the same session, and inspect the currently selected trace event without falling back to raw JSON first."
        actions={
          <>
            <Pill label={streamStatus === "live" ? "Live stream" : streamStatus === "connecting" ? "Connecting" : "Stream error"} tone={streamStatus === "error" ? "attention" : streamStatus === "live" ? "positive" : "neutral"} />
            <Pill label={timeline?.entries.length ? `${timeline.entries.length} entries` : "No timeline"} tone={timeline?.entries.length ? "positive" : "neutral"} />
            <Link className="button button--ghost" to={`/sessions/${sessionId}`}>
              Back to session
            </Link>
            {newerTraceAvailable ? (
              <Link className="button button--primary" to={`/sessions/${sessionId}/traces/${newestTraceID}`}>
                Open newer trace
              </Link>
            ) : null}
          </>
        }
      />
      {error ? <div className="banner banner--error">{error}</div> : null}
      <div className="trace-workspace">
        <div className="stack">
          <Section eyebrow="Session" title="Neighboring traces" summary="Recent traces for this session. Newest traces stay at the top without forcing navigation.">
            <div className="trace-rail">
              {traces.map((item) => (
                <button
                  className={`trace-rail__item${item.trace_id === traceId ? " trace-rail__item--active" : ""}`}
                  key={item.trace_id}
                  type="button"
                  onClick={() => navigate(`/sessions/${sessionId}/traces/${item.trace_id}`)}
                >
                  <div className="trace-rail__meta">
                    <Pill label={item.status || "unknown"} tone={traceSummaryTone(item.status)} />
                    <span>{formatDate(item.updated_at)}</span>
                  </div>
                  <strong>{item.headline || item.trace_id}</strong>
                  <small>{item.execution_id || "No execution id"}</small>
                  <div className="pill-group">
                    {traceSummaryPills(item.group_counts).map(([label, count]) => (
                      <Pill key={label} label={`${label} ${count}`} tone="neutral" />
                    ))}
                  </div>
                </button>
              ))}
              {traces.length === 0 ? <div className="data-table__empty">No traces are available for this session.</div> : null}
            </div>
          </Section>
        </div>

        <div className="stack">
          <Section eyebrow="Timeline" title="Causal sequence" summary="Trigger, execution, response, tools, approvals, delivery, and audit flow in one ordered workspace.">
            <div className="pill-group">
              {traceCounts.map(([label, count]) => (
                <Pill key={label} label={`${label} ${count}`} tone={traceCountTone(label)} />
              ))}
            </div>
            <div className="trace-sections">
              {groupedEntries.map(([group, entries]) => (
                <section className="trace-stage" key={group}>
                  <header className="trace-stage__header">
                    <div>
                      <p className="section__eyebrow">{titleCase(group)}</p>
                      <h3>{traceGroupSummary(group)}</h3>
                    </div>
                    <Pill label={`${entries.length} items`} tone={traceCountTone(group)} />
                  </header>
                  <div className="trace-stage__body">
                    {entries.map((entry) => (
                      <button
                        className={`trace-entry${selectedEntry?.id === entry.id ? " trace-entry--active" : ""}`}
                        key={entry.id}
                        type="button"
                        onClick={() => setSelectedEntryID(entry.id)}
                      >
                        <div className="trace-entry__meta">
                          <Pill label={traceGroup(entry.kind)} tone={traceTone(entry.kind)} />
                          <span>{entry.kind}</span>
                          <span>{formatDate(entry.when)}</span>
                        </div>
                        <strong>{traceTitle(entry)}</strong>
                        <small>{traceSummaryLine(entry)}</small>
                      </button>
                    ))}
                  </div>
                </section>
              ))}
              {groupedEntries.length === 0 ? <div className="data-table__empty">No trace timeline is available for this selection.</div> : null}
            </div>
          </Section>
        </div>

        <div className="stack">
          <Section eyebrow="Inspect" title="Selected trace event" summary="Curated fields first, with raw payload inspectors behind explicit disclosure.">
            {selectedEntry ? (
              <div className="stack">
                <KeyValueList
                  entries={[
                    ["Group", traceGroup(selectedEntry.kind)],
                    ["Kind", selectedEntry.kind],
                    ["When", formatDate(selectedEntry.when)],
                    ["Session", selectedEntry.session_id || sessionId || "n/a"],
                    ["Execution", selectedEntry.execution_id || timeline?.execution_id || "n/a"],
                    ["Trace", selectedEntry.trace_id || timeline?.trace_id || traceId || "n/a"],
                    ["Status", traceEntryStatus(selectedEntry) || "n/a"],
                  ]}
                />
                <div className="surface-panel">
                  <div className="stack-heading">
                    <p className="stack-heading__eyebrow">Summary</p>
                    <h3>{traceTitle(selectedEntry)}</h3>
                    <p>{traceSummaryLine(selectedEntry)}</p>
                  </div>
                </div>
                <InspectPanel title="Entry fields" summary="Structured fields extracted from the selected payload." value={traceEntryFields(selectedEntry)} defaultOpen />
                <InspectPanel title="Entry payload" summary="Raw trace payload for the selected event." value={tracePayload(selectedEntry)} />
              </div>
            ) : (
              <div className="data-table__empty">Select a trace entry to inspect it.</div>
            )}
          </Section>
        </div>
      </div>
    </>
  );
}

function traceSummaryPills(groupCounts?: Record<string, number>): Array<[string, number]> {
  if (!groupCounts) {
    return [];
  }
  return orderedTraceGroups
    .map((group) => [group, groupCounts[group] ?? 0] as [string, number])
    .filter(([, count]) => count > 0)
    .slice(0, 4);
}

function traceSummaryTone(status?: string): "neutral" | "positive" | "attention" | "danger" {
  switch ((status || "").toLowerCase()) {
    case "succeeded":
    case "ready":
      return "positive";
    case "blocked":
    case "pending":
      return "attention";
    case "failed":
    case "abandoned":
    case "canceled":
      return "danger";
    default:
      return "neutral";
  }
}

function traceCountTone(group: TraceGroup): "neutral" | "positive" | "attention" | "danger" {
  switch (group) {
    case "execution":
    case "response":
      return "positive";
    case "tool":
    case "approval":
      return "attention";
    case "audit":
      return "danger";
    default:
      return "neutral";
  }
}

function traceGroupSummary(group: TraceGroup): string {
  switch (group) {
    case "session":
      return "Trigger and operator events";
    case "execution":
      return "Execution lifecycle";
    case "response":
      return "Response generation";
    case "tool":
      return "Tools and side effects";
    case "approval":
      return "Approvals and blocks";
    case "delivery":
      return "Delivery attempts";
    case "audit":
      return "Audit and failure trail";
    default:
      return "Other trace entries";
  }
}
