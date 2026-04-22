// Client library for the Go backend's out-of-band HITL endpoints.
// The main /agent endpoint is driven by CopilotRuntime's HttpAgent; these
// helpers hit the secondary endpoints documented in
// docs/workstream-hitl-v2.md §4.3.1 directly from the browser.

const DEFAULT_BACKEND =
  process.env.NEXT_PUBLIC_AGENT_BACKEND_BASE ??
  "http://localhost:8080";

function base(): string {
  // Allow runtime override via window attribute for simpler Cypress / dev setups.
  if (typeof window !== "undefined") {
    const attr = (window as unknown as { __AGENT_BACKEND_BASE?: string }).__AGENT_BACKEND_BASE;
    if (attr) return attr;
  }
  return DEFAULT_BACKEND;
}

// ---------- types ----------

export type DecisionKind = "permission" | "plan_review" | "question";

export type DecisionChoice = {
  key: string;
  label: string;
  description?: string;
};

export type PendingDecision = {
  RequestID: string;
  RunID: string;
  ThreadID: string;
  Kind: DecisionKind;
  Source: string;
  ToolCallID?: string;
  Prompt: string;
  Payload?: Record<string, unknown>;
  Choices?: DecisionChoice[] | null;
  CreatedAt: string;
  Deadline: string;
  RetryAttempt?: number;
};

export type HITLRequestedPayload = {
  RequestID: string;
  Kind: DecisionKind;
  Source: string;
  ToolCallID?: string;
  Prompt: string;
  Payload?: Record<string, unknown>;
  Choices?: DecisionChoice[] | null;
  CreatedAt: string;
  Deadline: string;
  RetryAttempt?: number;
};

export type HITLResolvedPayload = {
  RequestID: string;
  Kind: DecisionKind;
  Source: string;
  RetryAttempt?: number;
  Result: "approved" | "rejected" | "answered" | "timed_out" | "aborted";
  Choice?: string;
  Answer?: Record<string, unknown>;
  ResolvedAt: string;
  Latency: number;
};

export type StreamEvent = {
  Kind: string;
  Seq: number;
  RunID: string;
  ThreadID: string;
  MessageID?: string;
  ToolCallID?: string;
  Name?: string;
  Delta?: string;
  Args?: Record<string, unknown>;
  Result?: Record<string, unknown>;
  HITLRequested?: HITLRequestedPayload | null;
  HITLResolved?: HITLResolvedPayload | null;
  Timestamp?: string;
};

export type SessionSnapshot = {
  thread_id: string;
  after: number;
  events: StreamEvent[];
  last_seq: number;
  run_active: boolean;
};

// ---------- API ----------

export async function fetchSessionEvents(
  threadId: string,
  afterSeq = 0,
): Promise<SessionSnapshot> {
  const url = `${base()}/session/events?thread_id=${encodeURIComponent(threadId)}&after=${afterSeq}`;
  const res = await fetch(url, { cache: "no-store" });
  if (!res.ok) {
    throw new Error(`fetchSessionEvents ${res.status}: ${await res.text()}`);
  }
  return res.json();
}

export async function fetchPendingDecisions(
  threadId: string,
): Promise<PendingDecision[]> {
  const url = `${base()}/decision/pending?thread_id=${encodeURIComponent(threadId)}`;
  const res = await fetch(url, { cache: "no-store" });
  if (!res.ok) {
    throw new Error(`fetchPendingDecisions ${res.status}: ${await res.text()}`);
  }
  const body = (await res.json()) as { pending?: PendingDecision[] };
  return body.pending ?? [];
}

export type ResolveInput = {
  threadId: string;
  requestId: string;
  result: "approved" | "rejected" | "answered";
  choice?: string;
  answer?: Record<string, unknown>;
  text?: string;
};

export async function resolveDecision(input: ResolveInput): Promise<void> {
  const res = await fetch(`${base()}/decision/resolve`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      run_id: input.threadId, // backend overloads RunID as thread-id in the body
      request_id: input.requestId,
      result: input.result,
      choice: input.choice,
      answer: input.answer,
      text: input.text,
    }),
  });
  if (res.status === 410) {
    throw new Error("decision expired (already resolved or timed out)");
  }
  if (res.status === 409) {
    throw new Error("run already ended");
  }
  if (!res.ok) {
    throw new Error(`resolveDecision ${res.status}: ${await res.text()}`);
  }
}
