// Client library for the Go backend's out-of-band HITL endpoints. The main
// /agent endpoint is driven by CopilotRuntime's HttpAgent; these helpers call
// the host recovery and decision endpoints directly from the browser.

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

// These fields mirror adaptor.ApprovalRequest's public JSON representation.
export type PendingDecision = {
  ID: string;
  RunID: string;
  Kind: DecisionKind;
  Title: string;
  Source: string;
  ToolCallID?: string;
  Details?: Record<string, unknown>;
  Choices?: { Key: string; Label: string; Description?: string }[] | null;
  CreatedAt: string;
  Deadline: string;
  Attempt: number;
};

// sessionrecorder.EventRecord's stable typed envelope, not a StreamPayload.
export type SessionRecord = {
  host_seq: number;
  recorded_at: string;
  kind: string;
  meta: { run_id?: string; sequence?: number };
  event: {
    ID?: string;
    Kind?: string;
    Phase?: string;
    Name?: string;
    Text?: string;
    Title?: string;
    Source?: string;
    ArgsDelta?: string;
    Data?: Record<string, unknown>;
  };
};

export type SessionSnapshot = {
  thread_id: string;
  after: number;
  events: SessionRecord[];
  last_seq: number;
  run_active: boolean;
};

// ---------- API ----------

// fetchSessionEvents returns the records with host_seq strictly greater
// than afterHostSeq. Pass 0 for a full snapshot (first mount); afterwards
// pass the last host_seq you've seen to pull only the delta.
export async function fetchSessionEvents(
  threadId: string,
  afterHostSeq = 0,
): Promise<SessionSnapshot> {
  const url = `${base()}/session/events?thread_id=${encodeURIComponent(threadId)}&after=${afterHostSeq}`;
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
