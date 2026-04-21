import { useEffect, useMemo, useRef, useState } from "react";
import { HttpAgent } from "@ag-ui/client";

// A Row models one on-screen card. Text content is NOT stored here — it lives
// in refs (see below) so streaming tokens accumulate in event-arrival order
// regardless of React's concurrent scheduler or StrictMode double-render.
type Row =
  | { id: string; kind: "user" }
  | { id: string; kind: "assistant"; done: boolean }
  | { id: string; kind: "reasoning"; done: boolean }
  | {
      id: string;
      kind: "tool";
      name: string;
      done: boolean;
      hasResult: boolean;
    }
  | { id: string; kind: "error" };

function uid() {
  return Math.random().toString(36).slice(2, 10);
}

export default function App() {
  const agent = useMemo(
    () =>
      new HttpAgent({
        url: __AGENT_BACKEND_URL__,
      }),
    [],
  );

  // Row layout lives in React state. Row.id is stable; order changes only
  // when new messages arrive.
  const [rows, setRows] = useState<Row[]>([]);
  const [draft, setDraft] = useState("");
  const [status, setStatus] = useState("");
  const [running, setRunning] = useState(false);

  // Content buffers live in refs so delta append is synchronous. React
  // batching / StrictMode cannot reorder these writes. A monotonically
  // increasing tick triggers re-renders that re-read from refs.
  const userText = useRef(new Map<string, string>());
  const assistantText = useRef(new Map<string, string>());
  const reasoningText = useRef(new Map<string, string>());
  const toolArgs = useRef(new Map<string, string>());
  const toolResult = useRef(new Map<string, string>());
  const errorText = useRef(new Map<string, string>());
  const [, setTick] = useState(0);
  const bump = () => setTick((t) => t + 1);

  const transcriptRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (transcriptRef.current) {
      transcriptRef.current.scrollTop = transcriptRef.current.scrollHeight;
    }
  }, [rows]);

  async function send() {
    const text = draft.trim();
    if (!text || running) return;
    setDraft("");
    setRunning(true);
    setStatus("streaming…");

    // Seed the user row synchronously so the UI reflects the outgoing
    // message the moment Send is clicked.
    const userMessageId = uid();
    userText.current.set(userMessageId, text);
    setRows((prev) => [...prev, { id: userMessageId, kind: "user" }]);

    const sub = agent.subscribe({
      onRunStartedEvent() {
        setStatus("run started");
      },

      onTextMessageStartEvent({ event }) {
        assistantText.current.set(event.messageId, "");
        setRows((prev) =>
          prev.some((r) => r.id === event.messageId && r.kind === "assistant")
            ? prev
            : [
                ...prev,
                { id: event.messageId, kind: "assistant", done: false },
              ],
        );
        bump();
      },
      onTextMessageContentEvent({ event }) {
        // Synchronous append in the ref — order is guaranteed to match
        // delta arrival order because JS is single-threaded and RxJS
        // dispatches synchronously from the subject.
        const prev = assistantText.current.get(event.messageId) ?? "";
        assistantText.current.set(event.messageId, prev + event.delta);
        // Lazy-create the row if onTextMessageStartEvent didn't fire.
        setRows((rows) =>
          rows.some((r) => r.id === event.messageId && r.kind === "assistant")
            ? rows
            : [
                ...rows,
                { id: event.messageId, kind: "assistant", done: false },
              ],
        );
        bump();
      },
      onTextMessageEndEvent({ event }) {
        setRows((prev) =>
          prev.map((r) =>
            r.id === event.messageId && r.kind === "assistant"
              ? { ...r, done: true }
              : r,
          ),
        );
      },

      onReasoningMessageStartEvent({ event }) {
        reasoningText.current.set(event.messageId, "");
        setRows((prev) =>
          prev.some((r) => r.id === event.messageId && r.kind === "reasoning")
            ? prev
            : [
                ...prev,
                { id: event.messageId, kind: "reasoning", done: false },
              ],
        );
        bump();
      },
      onReasoningMessageContentEvent({ event }) {
        const prev = reasoningText.current.get(event.messageId) ?? "";
        reasoningText.current.set(event.messageId, prev + event.delta);
        setRows((rows) =>
          rows.some((r) => r.id === event.messageId && r.kind === "reasoning")
            ? rows
            : [
                ...rows,
                { id: event.messageId, kind: "reasoning", done: false },
              ],
        );
        bump();
      },
      onReasoningMessageEndEvent({ event }) {
        setRows((prev) =>
          prev.map((r) =>
            r.id === event.messageId && r.kind === "reasoning"
              ? { ...r, done: true }
              : r,
          ),
        );
      },

      onToolCallStartEvent({ event }) {
        toolArgs.current.set(event.toolCallId, "");
        setRows((prev) => [
          ...prev,
          {
            id: event.toolCallId,
            kind: "tool",
            name: event.toolCallName ?? "tool",
            done: false,
            hasResult: false,
          },
        ]);
        bump();
      },
      onToolCallArgsEvent({ event }) {
        const prev = toolArgs.current.get(event.toolCallId) ?? "";
        toolArgs.current.set(event.toolCallId, prev + event.delta);
        bump();
      },
      onToolCallEndEvent({ event }) {
        setRows((prev) =>
          prev.map((r) =>
            r.id === event.toolCallId && r.kind === "tool"
              ? { ...r, done: true }
              : r,
          ),
        );
      },
      onToolCallResultEvent({ event }) {
        toolResult.current.set(event.toolCallId, event.content);
        setRows((prev) =>
          prev.map((r) =>
            r.id === event.toolCallId && r.kind === "tool"
              ? { ...r, done: true, hasResult: true }
              : r,
          ),
        );
        bump();
      },

      onRunFinishedEvent() {
        setStatus("done");
      },
      onRunErrorEvent({ event }) {
        const id = uid();
        errorText.current.set(id, event.message ?? "run error");
        setRows((prev) => [...prev, { id, kind: "error" }]);
        setStatus("error");
        bump();
      },
    });

    try {
      agent.addMessage({
        id: userMessageId,
        role: "user",
        content: text,
      });
      await agent.runAgent();
    } catch (err) {
      const id = uid();
      errorText.current.set(id, String(err));
      setRows((prev) => [...prev, { id, kind: "error" }]);
      setStatus("error");
    } finally {
      sub.unsubscribe();
      setRunning(false);
    }
  }

  return (
    <div className="chat">
      <header>
        <h1>agent-adaptor · @ag-ui/client · codex</h1>
        <p className="meta">
          Browser speaks AG-UI directly to{" "}
          <code>{__AGENT_BACKEND_URL__}</code>. No CopilotKit, no Next.js.
        </p>
      </header>

      <div className="transcript" ref={transcriptRef}>
        {rows.length === 0 && (
          <p className="meta">
            Ask anything. Text, reasoning and tool-call events stream live.
          </p>
        )}
        {rows.map((r) => (
          <div key={r.id} className={`row ${r.kind}`}>
            <div className="role">
              {r.kind === "tool"
                ? `tool · ${(r as Extract<Row, { kind: "tool" }>).name}`
                : r.kind}
            </div>
            <div className="text">
              {r.kind === "user" && (userText.current.get(r.id) ?? "")}
              {r.kind === "assistant" &&
                (assistantText.current.get(r.id) ?? "")}
              {r.kind === "reasoning" &&
                (reasoningText.current.get(r.id) ?? "")}
              {r.kind === "tool" && (
                <>
                  {`args: ${toolArgs.current.get(r.id) ?? ""}`}
                  {(r as Extract<Row, { kind: "tool" }>).hasResult &&
                    `\n\nresult: ${toolResult.current.get(r.id) ?? ""}`}
                  {(r as Extract<Row, { kind: "tool" }>).done &&
                    !(r as Extract<Row, { kind: "tool" }>).hasResult &&
                    "\n\n[completed]"}
                </>
              )}
              {r.kind === "error" && (errorText.current.get(r.id) ?? "")}
            </div>
          </div>
        ))}
      </div>

      <div className="status">{status}</div>

      <form
        className="composer"
        onSubmit={(e) => {
          e.preventDefault();
          send();
        }}
      >
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Type a message…"
          disabled={running}
        />
        <button type="submit" disabled={running || draft.trim() === ""}>
          {running ? "…" : "Send"}
        </button>
      </form>
    </div>
  );
}
