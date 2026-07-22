"use client";

import { useEffect, useMemo, useState } from "react";
import { CopilotChat } from "@copilotkit/react-core/v2";
import {
  useCopilotAction,
  useCopilotContext,
  type CatchAllActionRenderProps,
} from "@copilotkit/react-core";
import { SessionPanel } from "./components/session-panel";
import { renderDecisionCard, ToolCallCard } from "./components/cards";

// -----------------------------------------------------------------------------
// threadId: stable per-browser identifier so history / pending decisions stay
// addressable across reloads. The effect below also pushes the persisted id
// into CopilotKit's internal thread state so every POST to /agent carries
// the same thread marker.
// -----------------------------------------------------------------------------

const THREAD_ID_STORAGE_KEY = "agent-adaptor-copilotkit-thread";

function useStableThreadId(): string {
  const [id, setId] = useState<string>("");
  const { threadId: kitThreadId, setThreadId } = useCopilotContext();

  useEffect(() => {
    if (typeof window === "undefined") return;
    let existing = window.localStorage.getItem(THREAD_ID_STORAGE_KEY);
    if (!existing) {
      existing = `agui-${Math.random().toString(36).slice(2, 10)}-${Date.now().toString(36)}`;
      window.localStorage.setItem(THREAD_ID_STORAGE_KEY, existing);
    }
    setId(existing);
  }, []);

  useEffect(() => {
    if (!id) return;
    if (kitThreadId !== id && typeof setThreadId === "function") {
      setThreadId(id);
    }
  }, [id, kitThreadId, setThreadId]);

  return id;
}

// -----------------------------------------------------------------------------
// HomePage
// -----------------------------------------------------------------------------

export default function HomePage() {
  const threadId = useStableThreadId();

  // Wildcard action catches every tool_call that the runtime doesn't already
  // own. For `dec.*` we render our interactive decision cards; for anything
  // else (native Bash / Read / Write) we show a generic tool-call card so
  // users still see arguments and results.
  //
  // CopilotKit passes args already parsed from the ToolCallArgs.Delta JSON.
  // See docs/workstream-hitl-v2.md §6.1 for the envelope shape.
  useCopilotAction({
    name: "*",
    render: (props: CatchAllActionRenderProps) => {
      const name = props.name ?? "(tool_call)";
      const status = props.status;
      const args = "args" in props ? (props.args as Record<string, unknown> | undefined) : undefined;
      const result = "result" in props ? props.result : undefined;
      if (name.startsWith("dec.")) {
        return (
          <DecisionCardFromAction
            threadId={threadId}
            name={name}
            status={status}
            args={args}
            result={result}
          />
        );
      }
      return (
        <ToolCallCard
          name={name}
          status={status}
          args={args}
          result={result}
        />
      );
    },
  });

  const hint = useMemo(() => chooseHint(), []);

  if (!threadId) {
    return null;
  }

  return (
    <main className="agent-app-shell">
      <header>
        <h1 style={{ margin: 0, fontSize: "1.4rem" }}>
          agent-adaptor · CopilotKit · HITL v2
        </h1>
        <p style={{ margin: "0.25rem 0 0", color: "#555", fontSize: "0.9rem" }}>
          AG-UI over SSE. Tool-calls, plan-review, question, and permission
          gates render as interactive cards. History + pending decisions
          survive page reloads (per-browser <code>thread_id</code>). Try: “
          <em>{hint}</em>”.
        </p>
      </header>

      <div className="agent-workspace-grid">
        <section
          className="agent-chat-panel"
          style={{
            background: "#fff",
            border: "1px solid #e5e7eb",
            borderRadius: 8,
            padding: "0.5rem",
            minHeight: "60vh",
            display: "flex",
            flexDirection: "column",
          }}
        >
          <CopilotChat
            agentId="codex"
            threadId={threadId}
            throttleMs={0}
            labels={{
              modalHeaderTitle: "agent",
              welcomeMessageText:
                "Ask anything. Every tool_call, plan-review, and question streams live.",
              chatInputPlaceholder: "Type a message…",
            }}
          />
        </section>

        <SessionPanel threadId={threadId} />
      </div>
    </main>
  );
}

// -----------------------------------------------------------------------------
// DecisionCardFromAction wires the tool-call render props into the shared
// decision renderer. It tracks local resolution state so a user click updates
// the card immediately while the backend POST is in flight.
// -----------------------------------------------------------------------------

function DecisionCardFromAction(props: {
  threadId: string;
  name: string;
  status: "inProgress" | "executing" | "complete";
  args: Record<string, unknown> | undefined;
  result: unknown;
}) {
  const { threadId, name, status, args, result } = props;

  // The AG-UI bridge embeds RequestID inside the ToolCallID via
  // "dec-<RequestID>". The useCopilotAction layer surfaces the tool name
  // (e.g. "dec.plan_review.exit_plan_mode") but not the raw
  // ToolCallID; we accept request_id from args when present, falling back
  // to the ToolCallID extracted via CopilotKit's internal message store is
  // an overkill for the demo, so we require the backend to include
  // tool_call_id in args.
  const requestId = extractRequestId(args);
  if (!requestId) {
    return (
      <div
        style={{
          padding: "0.75rem",
          border: "1px dashed #e5e7eb",
          borderRadius: 8,
          color: "#6b7280",
          fontSize: "0.85rem",
        }}
      >
        decision card: missing request id in args — rendering raw payload:
        <pre
          style={{
            margin: "0.5rem 0 0",
            background: "#f3f4f6",
            padding: "0.5rem",
            borderRadius: 6,
            fontSize: "0.78rem",
            whiteSpace: "pre-wrap",
          }}
        >
          {JSON.stringify({ name, args, status, result }, null, 2)}
        </pre>
      </div>
    );
  }

  // When status is "complete" the run has already resolved the decision via
  // the out-of-band POST (or a vendor tool_result). Surface the outcome
  // derived from the result payload.
  let already: "approved" | "rejected" | "answered" | undefined;
  if (status === "complete") {
    already = deriveOutcome(result);
  }

  return (
    <>
      {renderDecisionCard({
        threadId,
        name,
        requestId,
        args: args,
        alreadyResolved: already,
      })}
    </>
  );
}

function extractRequestId(args: Record<string, unknown> | undefined): string | undefined {
  if (!args) return undefined;
  if (typeof args["request_id"] === "string") return args["request_id"] as string;
  if (typeof args["requestId"] === "string") return args["requestId"] as string;
  if (typeof args["tool_call_id"] === "string") {
    const id = args["tool_call_id"] as string;
    return id.startsWith("dec-") ? id.slice("dec-".length) : id;
  }
  return undefined;
}

function deriveOutcome(result: unknown): "approved" | "rejected" | "answered" | undefined {
  let payload = result;
  if (typeof payload === "string") {
    try {
      payload = JSON.parse(payload);
    } catch {
      return undefined;
    }
  }
  if (!payload || typeof payload !== "object") return undefined;
  const p = payload as Record<string, unknown>;
  const r = typeof p["result"] === "string" ? (p["result"] as string) : undefined;
  if (r === "approved" || r === "rejected" || r === "answered") return r;
  return undefined;
}

function chooseHint(): string {
  const hints = [
    "Plan a migration",
    "Run ls -la for me",
    "Ask me a clarifying question",
  ];
  return hints[Math.floor(Math.random() * hints.length)];
}
