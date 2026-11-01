import { createContext, useContext, useEffect, useMemo, useRef, useState } from "react";
import { HttpAgent, type Message } from "@ag-ui/client";
import { AssistantRuntimeProvider, ComposerPrimitive, MessagePrimitive, ThreadPrimitive, groupPartByType, useAuiState, type ToolCallMessagePartProps } from "@assistant-ui/react";
import { useAgUiRuntime } from "@assistant-ui/react-ag-ui";
import { MarkdownTextPrimitive } from "@assistant-ui/react-markdown";
import { checkedFetch, createHistory, repository, toolOutcome, type ToolOutcome, runNotice, type RunNotice } from "./session";

export const Outcomes = createContext<ReadonlyMap<string, ToolOutcome>>(new Map());
export function Shell({ toolCallId, toolName, argsText, result, isError, status }: ToolCallMessagePartProps) {
  const live = useContext(Outcomes).get(toolCallId);
  const failed = isError || live?.status === "failed" || status.type === "incomplete";
  const label = failed ? "Failed" : live?.status === "cancelled" ? "Cancelled" : result !== undefined ? "Succeeded" : "Running";
  return <section className={`tool ${failed ? "failed" : ""}`} data-status={label.toLowerCase()}>
    <strong>{toolName} · {label}{live?.exitCode !== undefined ? ` · exit ${live.exitCode}` : ""}</strong>
    <pre>{argsText}</pre>{result !== undefined && <pre>{typeof result === "string" ? result : JSON.stringify(result, null, 2)}</pre>}
  </section>;
}
export function Activity({ data }: { data: unknown }) {
  const value = data && typeof data === "object" ? data as Record<string, unknown> : {};
  return <aside className="activity" role="status" data-tool-call-id={typeof value.parentToolCallId === "string" ? value.parentToolCallId : undefined}>
    <strong>{typeof value.title === "string" ? value.title : "Command review"} · {typeof value.status === "string" ? value.status : "running"}</strong>
    {typeof value.text === "string" && <p>{value.text}</p>}
  </aside>;
}
export function ProtocolData({ name, data }: { name: string; data: unknown }) {
  if (name === "agui-activity/command-review") return <Activity data={data} />;
  if (!["agentAdaptor.input.warning", "lifecycle", "stream.dropped", "hitl.requested", "hitl.resolved"].includes(name)) return null;
  const value = data && typeof data === "object" ? data as Record<string, unknown> : {};
  const text = typeof value.message === "string" ? value.message : typeof value.text === "string" ? value.text : name;
  return <aside className="protocol-notice" role="status">{text}</aside>;
}
export type Health = { mode: "fake" | "real"; agent: string; unattended: boolean; observation: boolean };
export function modeCopy(health: Health | undefined) {
  if (!health) return { intro: "Loading agent configuration…", footer: "Local demo" };
  return health.mode === "fake"
    ? { intro: "Ask anything. The fake agent checks 13 commands; command 13 fails. Send a second message to verify continuation.", footer: "Local demo · scripted commands are simulated · real drivers are opt-in" }
    : { intro: `Talk to your ${health.agent} CLI. Send a second message to continue this Thread.`, footer: `Local demo · ${health.agent} CLI · operator profile` };
}
export function RunNoticeView({ notice, dismiss }: { notice: RunNotice; dismiss?: () => void }) {
  return <div role={notice.kind === "cancelled" ? "status" : "alert"} className={notice.kind === "cancelled" ? "cancelled" : "error"}>
    {notice.message}{dismiss && <button onClick={dismiss}>Dismiss</button>}
  </div>;
}
const groupBy = groupPartByType({ "tool-call": ["group-tools"], reasoning: ["group-reasoning"] });
function ChatMessage() {
  const role = useAuiState((s) => s.message.role);
  const content = useAuiState((s) => s.message.content);
  const outcomes = useContext(Outcomes);
  const failed = content.some((p) => p.type === "tool-call" && (p.isError || outcomes.get(p.toolCallId)?.status === "failed"));
  return <MessagePrimitive.Root className={`message ${role}`}>
    <div className="role">{role}</div>
    <MessagePrimitive.GroupedParts groupBy={groupBy}>
      {({ part, children }) => {
        switch (part.type) {
          case "group-tools": return <details className="tool-group"><summary>{part.indices.length} commands{failed ? " · failed command" : ""} · expand for results</summary>{children}</details>;
          case "group-reasoning": return <details className="reasoning"><summary>Reasoning</summary>{children}</details>;
          case "text": return <MarkdownTextPrimitive />;
          case "reasoning": return <p>{part.text}</p>;
          case "tool-call": return <Shell {...part} />;
          case "data": return <ProtocolData name={part.name} data={part.data} />;
          case "indicator": return <span className="muted">Working…</span>;
          default: return null;
        }
      }}
    </MessagePrimitive.GroupedParts>
  </MessagePrimitive.Root>;
}
export function App({ threadId }: { threadId: string }) {
  const [notice, setNotice] = useState<RunNotice>();
  const receiveNotice = (value: RunNotice) => setNotice(value);
  const showError = (message: string) => setNotice({ kind: "error", message });
  const [health, setHealth] = useState<Health>();
  const unattended = health?.unattended ?? false;
  const copy = modeCopy(health);
  const [outcomes, setOutcomes] = useState(new Map<string, ToolOutcome>());
  const canonical = useRef<Message[] | undefined>(undefined);
  const agent = useMemo(() => new HttpAgent({ url: "/agent", threadId, fetch: checkedFetch }), [threadId]);
  const history = useMemo(() => createHistory(agent,
    (id, value) => setOutcomes((old) => new Map(old).set(id, value)),
    (messages) => { canonical.current = messages; }, receiveNotice), [agent]);
  const runtime = useAgUiRuntime({ agent, showThinking: true, adapters: { history }, onError: (e) => {
    receiveNotice(runNotice({ code: "code" in e && typeof e.code === "string" ? e.code : undefined, message: e.message }));
  } });
  useEffect(() => {
    const subscription = agent.subscribe({
      onRunInitialized() { setNotice(undefined); },
      onToolCallResultEvent({ event }) {
        const value = toolOutcome(event);
        if (value) setOutcomes((old) => new Map(old).set(event.toolCallId, value));
      },
      onRunErrorEvent({ event }) { receiveNotice(runNotice(event)); },
    });
    return () => { subscription.unsubscribe(); history.dispose(); };
  }, [agent, history]);
  useEffect(() => runtime.thread.subscribe(() => {
    if (canonical.current && !runtime.thread.getState().isRunning) {
      const messages = canonical.current; canonical.current = undefined;
      runtime.thread.import(repository(messages));
    }
  }), [runtime]);
  useEffect(() => { void checkedFetch("/health").then((r) => r.json()).then((c: Health) => setHealth(c)).catch((e: unknown) => showError(String(e))); }, []);
  async function cancel() {
    try { await checkedFetch(`/cancel?thread_id=${encodeURIComponent(threadId)}`, { method: "POST" }); }
    catch (e) { showError(String(e)); }
  }
  function newThread() { const url = new URL(location.href); url.searchParams.set("thread", crypto.randomUUID()); location.assign(url); }
  return <AssistantRuntimeProvider runtime={runtime}><Outcomes.Provider value={outcomes}>
    <main><header><div><p className="eyebrow">agent-adaptor · assistant-ui</p><h1>A conversation that stays.</h1><p className="muted">Thread <code>{threadId}</code> · reload to restore history{unattended ? " or join the active run" : ""}.</p></div><button onClick={newThread}>New thread</button></header>
      {notice && <RunNoticeView notice={notice} dismiss={() => setNotice(undefined)} />}
      <ThreadPrimitive.Root><ThreadPrimitive.Viewport>
        <ThreadPrimitive.Empty><p className="empty">{copy.intro}</p></ThreadPrimitive.Empty>
        <ThreadPrimitive.Messages components={{ UserMessage: ChatMessage, AssistantMessage: ChatMessage }} />
      </ThreadPrimitive.Viewport>
      <ComposerPrimitive.Root className="composer"><ComposerPrimitive.Input placeholder="Write a message…" aria-label="Message" /><div className="actions"><ComposerPrimitive.Send>Send</ComposerPrimitive.Send>{unattended ? <button type="button" onClick={() => void cancel()}>Cancel run on host</button> : <ComposerPrimitive.Cancel>Cancel</ComposerPrimitive.Cancel>}</div></ComposerPrimitive.Root>
      </ThreadPrimitive.Root>
      <footer>{copy.footer}</footer>
    </main>
  </Outcomes.Provider></AssistantRuntimeProvider>;
}
