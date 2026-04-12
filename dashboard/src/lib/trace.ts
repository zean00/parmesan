import type { JSONObject, TraceTimeline, TraceTimelineEntry } from "../types";

export type TraceGroup = "execution" | "response" | "tool" | "delivery" | "approval" | "session" | "audit" | "other";

export const orderedTraceGroups: TraceGroup[] = ["session", "execution", "response", "tool", "approval", "delivery", "audit", "other"];

export function summarizeTrace(trace: TraceTimeline | null): Array<[TraceGroup, number]> {
  if (!trace) {
    return [];
  }
  const counts = new Map<TraceGroup, number>();
  for (const entry of trace.entries) {
    const key = traceGroup(entry.kind);
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return orderedTraceGroups
    .map((group) => [group, counts.get(group) ?? 0] as [TraceGroup, number])
    .filter(([, count]) => count > 0);
}

export function traceGroup(kind: string): TraceGroup {
  if (kind === "execution" || kind.startsWith("execution.")) {
    return "execution";
  }
  if (kind === "response" || kind.startsWith("response.")) {
    return "response";
  }
  if (kind.startsWith("tool.")) {
    return "tool";
  }
  if (kind.startsWith("delivery.")) {
    return "delivery";
  }
  if (kind === "approval") {
    return "approval";
  }
  if (kind === "session.event" || kind.startsWith("operator.")) {
    return "session";
  }
  if (kind.startsWith("audit.") || kind.startsWith("media.")) {
    return "audit";
  }
  return "other";
}

export function traceTone(kind: string): "neutral" | "positive" | "attention" | "danger" {
  const group = traceGroup(kind);
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

export function groupTraceEntries(entries: TraceTimelineEntry[]): Array<[TraceGroup, TraceTimelineEntry[]]> {
  return orderedTraceGroups
    .map((group) => [group, entries.filter((entry) => traceGroup(entry.kind) === group)] as [TraceGroup, TraceTimelineEntry[]])
    .filter(([, items]) => items.length > 0);
}

export function traceTitle(entry: TraceTimelineEntry): string {
  const payload = tracePayload(entry);
  switch (entry.kind) {
    case "execution":
      return `Execution ${valueString(payload.id) || entry.execution_id || entry.id}`;
    case "response":
      return `Response ${valueString(payload.id) || entry.id}`;
    case "approval":
      return `Approval ${valueString(payload.status) || entry.id}`;
    case "tool.run":
      return valueString(payload.tool_id) || valueString(payload.name) || "Tool run";
    case "delivery.attempt":
      return valueString(payload.channel) ? `Delivery via ${valueString(payload.channel)}` : "Delivery attempt";
    default:
      if (entry.kind.startsWith("execution.step")) {
        return valueString(payload.name) || valueString(payload.step_name) || "Execution step";
      }
      if (entry.kind.startsWith("response.trace_span")) {
        return valueString(payload.name) || valueString(payload.kind) || "Response trace span";
      }
      if (entry.kind === "session.event" || entry.kind.startsWith("operator.")) {
        return valueString(payload.kind) || entry.kind;
      }
      if (entry.kind.startsWith("audit.")) {
        return valueString(payload.message) || entry.kind;
      }
      return entry.kind;
  }
}

export function traceSummaryLine(entry: TraceTimelineEntry): string {
  const payload = tracePayload(entry);
  const parts: string[] = [];
  const status = valueString(payload.status);
  const reason = valueString(payload.reason) || valueString(payload.message);
  const source = valueString(payload.source);
  const kind = valueString(payload.kind);
  if (status) {
    parts.push(`status ${status}`);
  }
  if (source && source !== "system") {
    parts.push(`source ${source}`);
  }
  if (kind && kind !== entry.kind) {
    parts.push(`kind ${kind}`);
  }
  if (reason) {
    parts.push(reason);
  }
  if (parts.length === 0) {
    const fields = objectField(payload, "fields");
    const fieldKeys = Object.keys(fields ?? {});
    if (fieldKeys.length > 0) {
      parts.push(`fields: ${fieldKeys.slice(0, 3).join(", ")}`);
    }
  }
  return parts.join(" • ") || "Structured trace payload available in the inspector.";
}

export function tracePayload(entry: TraceTimelineEntry): JSONObject {
  const payload = entry.payload;
  if (payload && typeof payload === "object" && !Array.isArray(payload)) {
    return payload as JSONObject;
  }
  return {};
}

export function traceEntryStatus(entry: TraceTimelineEntry): string {
  return valueString(tracePayload(entry).status);
}

export function traceEntryFields(entry: TraceTimelineEntry): JSONObject {
  return objectField(tracePayload(entry), "fields") ?? {};
}

export function objectField(value: JSONObject, key: string): JSONObject | null {
  const field = value[key];
  if (field && typeof field === "object" && !Array.isArray(field)) {
    return field as JSONObject;
  }
  return null;
}

function valueString(value: unknown): string {
  return typeof value === "string" ? value : "";
}
