import { HttpAgent, type Message, type BaseEvent } from "@ag-ui/client";
import { ExportedMessageRepository, type ThreadHistoryAdapter, type ChatModelRunResult } from "@assistant-ui/react";
import { fromAgUiMessages } from "@assistant-ui/react-ag-ui";

export type Snapshot = {
  messages: Message[];
  cursor: number;
  activeRun: { threadId: string; runId: string; boundaryHostSeq: number } | null;
};
export type ToolOutcome = { status: string; exitCode?: number };
export type RunNotice = { kind: "cancelled" | "error"; message: string };
export function runNotice(event: { code?: string; message?: string }): RunNotice {
  return event.code === "run.cancelled"
    ? { kind: "cancelled", message: "Run cancelled." }
    : { kind: "error", message: `${event.code ?? "run.error"}: ${event.message ?? "Run failed"}` };
}
export const repository = (messages: Message[]) =>
  ExportedMessageRepository.fromArray(fromAgUiMessages(messages, { showThinking: true }));

// Preserve the server's structured code/status in the runtime error banner.
export async function checkedFetch(url: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(url, init);
  if (!response.ok) {
    const body: unknown = await response.json();
    throw new Error(`HTTP ${response.status}: ${JSON.stringify(body)}`);
  }
  return response;
}
export function toolOutcome(event: BaseEvent): ToolOutcome | undefined {
  const value: unknown = event.metadata?.agentAdaptor;
  if (!value || typeof value !== "object" || !("status" in value) || typeof value.status !== "string") return;
  return { status: value.status, ...("exitCode" in value && typeof value.exitCode === "number" ? { exitCode: value.exitCode } : {}) };
}

// HttpAgent supplies the official SSE parser, verifier and message reducer.
// Only the endpoint changes for observation: this is a GET, never a replayed POST.
export function observer(snapshot: Snapshot, signal: AbortSignal, base = "") {
  if (!snapshot.activeRun) throw new Error("Snapshot has no active run");
  return new HttpAgent({
    url: `${base}/observe`, threadId: snapshot.activeRun.threadId,
    initialMessages: structuredClone(snapshot.messages),
    fetch: () => checkedFetch(`${base}/observe?thread_id=${encodeURIComponent(snapshot.activeRun!.threadId)}&after=${snapshot.cursor}`, { signal }),
  });
}

// The runtime resumes ONE assistant message using content/status updates. A
// coalescing mailbox bounds buffering while HttpAgent folds every wire event.
export function createHistory(
  agent: HttpAgent,
  onTool: (id: string, outcome: ToolOutcome) => void,
  onSettled: (messages: Message[]) => void,
  onNotice: (notice: RunNotice) => void,
  base = "",
): ThreadHistoryAdapter & { dispose(): void } {
  let abort = new AbortController();
  let pending: ChatModelRunResult | undefined;
  let wake: (() => void) | undefined;
  let done = false;
  let failure: unknown;
  let following: HttpAgent | undefined;
  let completion: Promise<unknown> | undefined;
  const push = (update: ChatModelRunResult) => { pending = update; wake?.(); };
  return {
    // The server records the user and assistant exactly once; append does no I/O.
    async append() {},
    async load() {
      const response = await checkedFetch(`${base}/snapshot?thread_id=${encodeURIComponent(agent.threadId)}`);
      const snapshot = await response.json() as Snapshot;
      if (!snapshot.activeRun) {
        // Message[] has no run lifecycle field. The ordered event view retains
        // authoritative cancellation/errors for the banner after reload.
        const records = await (await checkedFetch(`${base}/events?thread_id=${encodeURIComponent(agent.threadId)}`)).json() as { events: (BaseEvent & { code?: string; message?: string })[] | null }[];
        const terminal = records.flatMap((r) => r.events ?? []).filter((e) => e.type === "RUN_ERROR" || e.type === "RUN_FINISHED").at(-1);
        if (terminal?.type === "RUN_ERROR") onNotice(runNotice(terminal));
        return repository(snapshot.messages);
      }
      abort.abort(); abort = new AbortController(); done = false; failure = undefined; pending = undefined;
      following = observer(snapshot, abort.signal, base);
      const feed = following;
      let seed!: (messages: Message[]) => void;
      let rejectSeed!: (error: unknown) => void;
      const seeded = new Promise<Message[]>((resolve, reject) => { seed = resolve; rejectSeed = reject; });
      let hasSeed = false;
      let outcome: ChatModelRunResult["status"] = { type: "running" };
      completion = feed.runAgent({ runId: snapshot.activeRun.runId }, {
        onRunStartedEvent({ event }) {
          // The snapshot excludes the active user. Buffer the real boundary's
          // input seed before returning load(), so resume has the right parent.
          hasSeed = true;
          seed([...snapshot.messages, ...(event.input?.messages ?? [])]);
        },
        onToolCallResultEvent({ event }) {
          const status = toolOutcome(event); if (status) onTool(event.toolCallId, status);
        },
        onMessagesChanged({ messages }) {
          const active = messages.slice(snapshot.messages.length).filter((m) => m.role !== "user");
          const parts = fromAgUiMessages(active, { showThinking: true }).flatMap((m) =>
            m.role === "assistant" && Array.isArray(m.content) ? m.content : []);
          push({ content: parts, status: outcome });
        },
        onRunFinishedEvent() { outcome = { type: "complete", reason: "stop" }; },
        onRunErrorEvent({ event }) {
          onNotice(runNotice(event));
          outcome = event.code === "run.cancelled"
            ? { type: "incomplete", reason: "cancelled" }
            : { type: "incomplete", reason: "error", error: event.message };
        },
      }).catch((error: unknown) => { failure = error; rejectSeed(error); }).finally(() => {
        if (!hasSeed) rejectSeed(failure ?? new Error("Observation ended before RUN_STARTED"));
        if (outcome?.type === "running" && !abort.signal.aborted) {
          // EOF/revocation is not a successful terminal, even if the client
          // accepts the consumed prefix. Rejoin using a fresh Snapshot.
          failure ??= new Error("Observation ended before a terminal event; reload to rejoin");
          outcome = { type: "incomplete", reason: "error", error: String(failure) };
          onNotice({ kind: "error", message: String(failure) });
        }
        done = true;
        if (pending) pending = { ...pending, status: outcome };
        else push({ status: outcome });
        wake?.();
      });
      const messages = await seeded;
      return { ...repository(messages), unstable_resume: true };
    },
    async *resume(options) {
      const cancel = () => abort.abort();
      options.abortSignal.addEventListener("abort", cancel, { once: true });
      if (options.abortSignal.aborted) cancel();
      try {
        while (!done || pending) {
          if (!pending) await new Promise<void>((resolve) => { wake = resolve; });
          if (options.abortSignal.aborted) return;
          const update = pending; pending = undefined;
          if (update) yield update;
        }
        await completion;
        if (failure) throw failure;
        // resume() flattens a run into one UI message. Restore canonical wire
        // IDs afterwards, so the next POST passes the server history verifier.
        const response = await checkedFetch(`${base}/history?thread_id=${encodeURIComponent(agent.threadId)}`);
        onSettled(await response.json() as Message[]);
      } finally {
        options.abortSignal.removeEventListener("abort", cancel);
        abort.abort();
      }
    },
    dispose() { abort.abort(); following?.abortRun(); },
  };
}
