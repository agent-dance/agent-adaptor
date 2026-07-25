"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { CopilotChat, useAgent } from "@copilotkit/react-core/v2";
import {
  useCopilotAction,
  useCopilotContext,
  type CatchAllActionRenderProps,
} from "@copilotkit/react-core";
import { SessionPanel } from "./components/session-panel";
import { LiveSubagentPanel } from "./components/live-subagent-panel";
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
// Auto-prefill: on first open of a fresh thread, drop NEXT_PUBLIC_DEFAULT_PROMPT
// into the chat input so the user can just hit send to see the demo. The
// team-agent-workflow start script sets this to "start"; other hosts leave it
// unset and get no prefill. CopilotChat's input is controlled internally, so we
// set the textarea via the native value setter + an "input" event, which drives
// CopilotChat's onChange (setInputValue). We never clobber an existing thread or
// text the user already typed, and only fire once per mount.
// -----------------------------------------------------------------------------

function setNativeTextareaValue(el: HTMLTextAreaElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    window.HTMLTextAreaElement.prototype,
    "value",
  )?.set;
  setter?.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

function useAutoPrefillPrompt(threadId: string) {
  const prompt = (process.env.NEXT_PUBLIC_DEFAULT_PROMPT ?? "").trim();
  const { agent } = useAgent({ agentId: "codex", threadId });
  const doneRef = useRef(false);

  useEffect(() => {
    if (!prompt || !threadId || doneRef.current) return;
    // Existing conversation (reload with history): never prefill.
    if (agent.messages.length > 0) {
      doneRef.current = true;
      return;
    }
    // The textarea mounts a tick after the chat; poll briefly for it.
    let tries = 0;
    const timer = window.setInterval(() => {
      if (doneRef.current || agent.messages.length > 0) {
        doneRef.current = true;
        window.clearInterval(timer);
        return;
      }
      const textarea = document.querySelector<HTMLTextAreaElement>(
        'textarea[data-testid="copilot-chat-textarea"]',
      );
      if (textarea && textarea.value === "") {
        setNativeTextareaValue(textarea, prompt);
        textarea.focus();
        doneRef.current = true;
        window.clearInterval(timer);
      } else if (++tries > 50) {
        window.clearInterval(timer);
      }
    }, 100);
    return () => window.clearInterval(timer);
  }, [prompt, threadId, agent]);
}

// -----------------------------------------------------------------------------
// HomePage
// -----------------------------------------------------------------------------

export default function HomePage() {
  const threadId = useStableThreadId();
  useAutoPrefillPrompt(threadId);

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

      <DemoBanner />

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

        <div
          style={{
            display: "flex",
            flexDirection: "column",
            gap: "1rem",
            minWidth: 0,
          }}
        >
          <LiveSubagentPanel threadId={threadId} />
          <SessionPanel threadId={threadId} />
        </div>
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

// -----------------------------------------------------------------------------
// DemoBanner declares, on the page itself, what the currently-opened example is
// demonstrating. The shared web-copilotkit-hitl frontend is reused by several
// backends, so the copy is host-provided via NEXT_PUBLIC_DEMO_TITLE /
// NEXT_PUBLIC_DEMO_DESC (the team-agent-workflow start script sets both). When
// unset, nothing renders and other hosts see the plain header. DESC lines are
// separated by "\n" and rendered as bullets.
// -----------------------------------------------------------------------------
function DemoBanner() {
  const title = (process.env.NEXT_PUBLIC_DEMO_TITLE ?? "").trim();
  const desc = (process.env.NEXT_PUBLIC_DEMO_DESC ?? "").trim();
  if (!title && !desc) return null;
  const lines = desc.split("\n").map((l) => l.trim()).filter(Boolean);
  return (
    <section
      role="note"
      aria-label="demo description"
      style={{
        marginTop: "0.75rem",
        background: "linear-gradient(90deg, #eef2ff 0%, #f5f3ff 100%)",
        border: "1px solid #c7d2fe",
        borderRadius: 10,
        padding: "0.75rem 1rem",
      }}
    >
      {title && (
        <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
          <span
            style={{
              fontSize: "0.7rem",
              fontWeight: 700,
              letterSpacing: "0.04em",
              textTransform: "uppercase",
              color: "#4338ca",
              background: "#e0e7ff",
              borderRadius: 999,
              padding: "0.1rem 0.5rem",
            }}
          >
            demo
          </span>
          <strong style={{ fontSize: "0.98rem", color: "#312e81" }}>{title}</strong>
        </div>
      )}
      {lines.length > 0 && (
        <ul
          style={{
            margin: title ? "0.5rem 0 0" : 0,
            paddingLeft: "1.1rem",
            color: "#3730a3",
            fontSize: "0.86rem",
            lineHeight: 1.55,
          }}
        >
          {lines.map((line, i) => (
            <li key={i}>{line}</li>
          ))}
        </ul>
      )}
    </section>
  );
}

function chooseHint(): string {
  const hints = [
    "Plan a migration",
    "Run ls -la for me",
    "Ask me a clarifying question",
  ];
  return hints[Math.floor(Math.random() * hints.length)];
}
