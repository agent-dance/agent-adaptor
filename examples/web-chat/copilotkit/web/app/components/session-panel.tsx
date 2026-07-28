"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  fetchPendingDecisions,
  fetchSessionEvents,
  type PendingDecision,
  type SessionRecord,
  type StreamEvent,
} from "../lib/backend";
import { renderDecisionCard } from "./cards";

// SessionPanel renders the host-side persistence view:
//
//   1. Pending decisions  (click-to-resolve cards, backed by /decision/resolve)
//   2. History replay     (flat list of past StreamPayloads for this thread_id)
//
// # Refresh strategy
//
// The panel pulls with an *incremental cursor* (sessionrecorder.HostSeq) and
// reacts to user-visible lifecycle events rather than polling on a fixed
// interval:
//
//   - On mount: full snapshot (after=0) + pending list.
//   - On `visibilitychange` when the tab returns to foreground: incremental
//     `after=lastHostSeq` pull plus a fresh pending list.
//   - On `window.focus` and `online`: same incremental pull — catches the
//     "laptop resumed from sleep / wifi reconnected" cases.
//   - A 30s keep-alive interval runs only while the tab is visible, to
//     cover the edge case of a long-running run emitting events while the
//     user is reading this very panel. It's a backstop, not the primary
//     refresh path.
//
// # Why not SSE here?
//
// The main chat stream (CopilotChat / CopilotRuntime) already consumes live
// AG-UI events over SSE. This panel is the host-side audit/recovery view —
// not the live chat surface — so a second long-lived connection would be
// unnecessary for the demo.
export function SessionPanel({ threadId }: { threadId: string }) {
  // A keyed child gives every thread an isolated cursor and snapshot without
  // a synchronous state-reset effect when the caller switches threads.
  return <SessionPanelForThread key={threadId} threadId={threadId} />;
}

function SessionPanelForThread({ threadId }: { threadId: string }) {
  const [records, setRecords] = useState<SessionRecord[]>([]);
  const [pending, setPending] = useState<PendingDecision[]>([]);
  const [lastHostSeq, setLastHostSeq] = useState<number>(0);
  const [runActive, setRunActive] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [hidden, setHidden] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [refreshRequest, setRefreshRequest] = useState(0);

  // Keep the latest cursor in a ref so refresh() (memoised on threadId) can
  // read it without invalidating its identity on every pull. This avoids
  // tearing down and re-registering the window listeners below every frame.
  const lastHostSeqRef = useRef(0);
  useEffect(() => {
    lastHostSeqRef.current = lastHostSeq;
  }, [lastHostSeq]);

  const refresh = useCallback(
    async (mode: "full" | "incremental" = "incremental") => {
      // Always yield before updating React state. Besides coalescing multiple
      // lifecycle signals, this keeps effect setup free of synchronous state
      // transitions.
      await Promise.resolve();
      setSyncing(true);
      try {
        const after = mode === "full" ? 0 : lastHostSeqRef.current;
        const [snapshot, pendingList] = await Promise.all([
          fetchSessionEvents(threadId, after),
          fetchPendingDecisions(threadId),
        ]);
        setRunActive(!!snapshot.run_active);
        setPending(pendingList ?? []);
        const incoming = snapshot.events ?? [];
        if (mode === "full") {
          setRecords(incoming);
        } else if (incoming.length > 0) {
          // Append delta — `after=lastHostSeq` already filters by cursor,
          // but guard against out-of-order responses just in case.
          setRecords((prev) => {
            const seen = new Set(prev.map((r) => r.host_seq));
            const merged = prev.slice();
            for (const rec of incoming) {
              if (!seen.has(rec.host_seq)) merged.push(rec);
            }
            merged.sort((a, b) => a.host_seq - b.host_seq);
            return merged;
          });
        }
        if (snapshot.last_seq && snapshot.last_seq > lastHostSeqRef.current) {
          setLastHostSeq(snapshot.last_seq);
        }
        setError(null);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setSyncing(false);
      }
    },
    [threadId],
  );

  // Initial full fetch. A thread switch remounts this keyed child above.
  useEffect(() => {
    const timer = window.setTimeout(() => void refresh("full"), 0);
    return () => window.clearTimeout(timer);
  }, [refresh]);

  // Decision cards request a refresh by incrementing a token. Keeping the
  // callback state-only avoids passing the cursor-reading ref through render.
  const requestRefresh = useCallback(() => {
    setRefreshRequest((value) => value + 1);
  }, []);
  useEffect(() => {
    if (refreshRequest === 0) return;
    const timer = window.setTimeout(() => void refresh("incremental"), 0);
    return () => window.clearTimeout(timer);
  }, [refresh, refreshRequest]);

  // Event-driven incremental refresh: visibility, focus, online.
  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState === "visible") void refresh("incremental");
    };
    const onFocus = () => void refresh("incremental");
    const onOnline = () => void refresh("incremental");
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("focus", onFocus);
    window.addEventListener("online", onOnline);
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("focus", onFocus);
      window.removeEventListener("online", onOnline);
    };
  }, [refresh]);

  // Backstop keep-alive: 30s incremental pull while the tab is visible.
  // Cheap now that we only send delta (`after=lastHostSeq`).
  useEffect(() => {
    const tick = () => {
      if (document.visibilityState === "visible") void refresh("incremental");
    };
    const id = setInterval(tick, 30_000);
    return () => clearInterval(id);
  }, [refresh]);

  const pendingCards = pending.map((p) => {
    const card = renderDecisionCard({
      threadId,
      name: `dec.${p.Kind}.${p.Source}`,
      requestId: p.RequestID,
      args: {
        kind: p.Kind,
        source: p.Source,
        prompt: p.Prompt,
        payload: p.Payload,
        choices: p.Choices ?? undefined,
        tool_call_id: p.ToolCallID,
        retry_attempt: p.RetryAttempt,
      },
      onResolved: requestRefresh,
    });
    return <div key={p.RequestID}>{card}</div>;
  });

  const historyCount = records.length;

  return (
    <aside
      style={{
        background: "#fff",
        border: "1px solid #e5e7eb",
        borderRadius: 12,
        padding: "1rem",
        display: "grid",
        gap: "1rem",
        alignSelf: "start",
        minWidth: 320,
        maxHeight: "80vh",
        overflow: "auto",
      }}
    >
      <header
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          gap: "0.5rem",
          position: "sticky",
          top: 0,
          background: "#fff",
          paddingBottom: "0.25rem",
          borderBottom: "1px solid #e5e7eb",
        }}
      >
        <div>
          <h2 style={{ margin: 0, fontSize: "1rem" }}>Session</h2>
          <div style={{ fontSize: "0.75rem", color: "#6b7280" }}>
            thread: <code>{threadId}</code>
          </div>
        </div>
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: "0.4rem",
            fontSize: "0.7rem",
          }}
        >
          <span
            style={{
              width: 8,
              height: 8,
              borderRadius: 999,
              background: runActive ? "#16a34a" : "#9ca3af",
            }}
          />
          <span style={{ color: runActive ? "#166534" : "#6b7280" }}>
            {runActive ? "run active" : "idle"}
          </span>
          <button
            type="button"
            onClick={() => void refresh("full")}
            disabled={syncing}
            title="Force full reload"
            style={{
              padding: "0.2rem 0.5rem",
              border: "1px solid #d1d5db",
              background: syncing ? "#f3f4f6" : "#f9fafb",
              borderRadius: 6,
              cursor: syncing ? "default" : "pointer",
              fontSize: "0.7rem",
            }}
          >
            {syncing ? "…" : "↻"}
          </button>
        </div>
      </header>

      {error && (
        <div
          style={{
            padding: "0.5rem 0.75rem",
            background: "#fee2e2",
            color: "#991b1b",
            borderRadius: 6,
            fontSize: "0.8rem",
          }}
        >
          backend error: {error}
        </div>
      )}

      <section>
        <h3
          style={{
            margin: "0 0 0.5rem",
            fontSize: "0.85rem",
            color: "#374151",
            textTransform: "uppercase",
            letterSpacing: "0.04em",
          }}
        >
          Pending decisions ({pending.length})
        </h3>
        {pending.length === 0 ? (
          <p style={{ margin: 0, color: "#9ca3af", fontSize: "0.82rem" }}>
            No pending decisions.
          </p>
        ) : (
          pendingCards
        )}
      </section>

      <section>
        <header
          style={{
            display: "flex",
            justifyContent: "space-between",
            alignItems: "center",
          }}
        >
          <h3
            style={{
              margin: "0 0 0.5rem",
              fontSize: "0.85rem",
              color: "#374151",
              textTransform: "uppercase",
              letterSpacing: "0.04em",
            }}
          >
            History ({historyCount}, host_seq ≤ {lastHostSeq})
          </h3>
          <button
            type="button"
            onClick={() => setHidden((v) => !v)}
            style={{
              border: "1px solid #d1d5db",
              background: "#f9fafb",
              borderRadius: 6,
              padding: "0.15rem 0.45rem",
              fontSize: "0.72rem",
              cursor: "pointer",
            }}
          >
            {hidden ? "show" : "hide"}
          </button>
        </header>
        {!hidden && <HistoryList records={records} />}
      </section>
    </aside>
  );
}

// ---- History rendering (memoised) ----

function HistoryList({ records }: { records: SessionRecord[] }) {
  const items = useMemo(
    () =>
      records.map((r) => (
        <li
          key={r.host_seq}
          style={{
            padding: "0.35rem 0.5rem",
            background: "#f9fafb",
            borderRadius: 6,
            display: "grid",
            gap: "0.15rem",
          }}
        >
          <div
            style={{
              display: "flex",
              justifyContent: "space-between",
              gap: "0.5rem",
            }}
          >
            <code style={{ color: "#1e3a8a" }}>{r.payload.Kind}</code>
            <span
              style={{ color: "#9ca3af" }}
              title={`run ${r.payload.RunID} / seq ${r.payload.Seq}`}
            >
              #{r.host_seq}
            </span>
          </div>
          {renderEventSummary(r.payload)}
        </li>
      )),
    [records],
  );

  return (
    <ul
      style={{
        listStyle: "none",
        padding: 0,
        margin: 0,
        display: "grid",
        gap: "0.35rem",
        fontSize: "0.78rem",
        color: "#374151",
      }}
    >
      {items}
      {records.length === 0 && (
        <li style={{ color: "#9ca3af" }}>No events yet.</li>
      )}
    </ul>
  );
}

function renderEventSummary(ev: StreamEvent): React.ReactNode {
  if (ev.HITLRequested) {
    return (
      <div>
        <span style={{ color: "#6b21a8", fontWeight: 600 }}>
          {ev.HITLRequested.Kind}
        </span>{" "}
        request <code>{ev.HITLRequested.Source}</code>
      </div>
    );
  }
  if (ev.HITLResolved) {
    return (
      <div>
        <span style={{ color: "#065f46", fontWeight: 600 }}>
          {ev.HITLResolved.Result}
        </span>{" "}
        {ev.HITLResolved.Kind} / <code>{ev.HITLResolved.Source}</code>
      </div>
    );
  }
  if (ev.Kind === "text.content" && ev.Delta) {
    return (
      <span style={{ color: "#374151" }}>
        “{truncate(ev.Delta, 60)}”
      </span>
    );
  }
  if (ev.Kind === "tool_call.start" && ev.Name) {
    return (
      <span>
        tool <code>{ev.Name}</code> <span style={{ color: "#9ca3af" }}>({ev.ToolCallID})</span>
      </span>
    );
  }
  if (ev.Kind === "tool_call.args" && ev.Delta) {
    return <code style={{ color: "#4b5563" }}>{truncate(ev.Delta, 80)}</code>;
  }
  return null;
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n - 1) + "…";
}
