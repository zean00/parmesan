export type JSONValue =
  | string
  | number
  | boolean
  | null
  | JSONValue[]
  | { [key: string]: JSONValue };

export type JSONObject = { [key: string]: JSONValue };

export type SessionFilters = {
  agentId: string;
  customerId: string;
  assignedOperatorId: string;
  channel: string;
  activeOnly: boolean;
  pendingApprovalOnly: boolean;
  failedMediaOnly: boolean;
  unresolvedLintOnly: boolean;
};

export type SessionView = {
  id: string;
  channel: string;
  customer_id?: string;
  agent_id?: string;
  mode?: string;
  status?: string;
  title?: string;
  metadata?: JSONObject;
  labels?: string[];
  created_at?: string;
  assigned_operator_id?: string;
  last_activity_at?: string;
  closed_at?: string;
  close_reason?: string;
  pending_approval_count?: number;
  failed_media_count?: number;
  unresolved_lint_count?: number;
  pending_preference_count?: number;
  summary?: JSONObject;
};

export type SessionEvent = {
  id: string;
  session_id: string;
  source: string;
  kind: string;
  trace_id?: string;
  offset?: number;
  created_at: string;
  content?: Array<{ type: string; text?: string }>;
  data?: JSONObject;
  metadata?: JSONObject;
};

export type AgentProfile = {
  id: string;
  name: string;
  description?: string;
  status: string;
  default_policy_bundle_id?: string;
  default_knowledge_scope_kind?: string;
  default_knowledge_scope_id?: string;
  soul_hash?: string;
  active_session_count?: number;
  metadata?: JSONObject;
  created_at?: string;
  updated_at?: string;
};

export type AgentStats = {
  agent_id: string;
  window: string;
  window_started_at: string;
  window_finished_at: string;
  active_session_count: number;
  sessions_created: number;
  failed_executions: number;
  pending_approvals: number;
  takeovers: number;
  operator_replies: number;
  average_first_response_seconds: number;
};

export type OperatorNotification = {
  id: string;
  kind: string;
  severity: string;
  title: string;
  session_id?: string;
  execution_id?: string;
  agent_id?: string;
  trace_id?: string;
  created_at: string;
  status: string;
  payload?: JSONObject;
};

export type QueueSummary = Record<string, number>;

export type ExecutionPayload = {
  execution?: JSONObject;
  steps?: JSONObject[];
};

export type TraceTimelineEntry = {
  kind: string;
  id: string;
  session_id?: string;
  execution_id?: string;
  trace_id?: string;
  when: string;
  payload?: JSONValue;
};

export type TraceTimeline = {
  trace_id: string;
  session_id?: string;
  execution_id?: string;
  entries: TraceTimelineEntry[];
};

export type SessionTraceSummary = {
  trace_id: string;
  session_id?: string;
  execution_id?: string;
  started_at?: string;
  updated_at?: string;
  status?: string;
  headline?: string;
  group_counts?: Record<string, number>;
};
