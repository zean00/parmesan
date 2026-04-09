export type JSONValue =
  | string
  | number
  | boolean
  | null
  | JSONValue[]
  | { [key: string]: JSONValue };

export type JSONObject = { [key: string]: JSONValue };

export type ControlFormState = {
  agentId: string;
  channel: string;
  customerId: string;
  sessionId: string;
  sessionKey: string;
  scopeKind: string;
  scopeId: string;
};
