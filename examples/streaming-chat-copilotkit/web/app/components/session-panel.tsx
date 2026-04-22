"use client";

import { useCallback, useEffect, useState } from "react";
import {
  fetchPendingDecisions,
  fetchSessionEvents,
  type PendingDecision,
  type StreamEvent,
} from "../lib/backend";
import { renderDecisionCard } from "./cards";

// SessionPanel renders the host-side persistence view:
//   1. Pending decisions (click-to-resolve cards, backed by /decision/resolve)
//   2. History replay (flat list of past StreamPayloads for this thread_id)
//
// Recovery protocol (docs/workstream-hitl-v2.md §4.3.1):
//   - On mount, fetch /session/events?after=0 + /decision/pending
//   - Poll every 3s for new entries while the run is active so the panel
//     reflects the live backend state even when CopilotChat is busy
export function SessionPanel({ threadId }: { threadId: string }) {
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const [pending, setPending] = useState<PendingDecision[]>([]);
  const [lastSeq, setLastSeq] = useState<number>(0);
  const [runActive, setRunActive] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [hidden, setHidden] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const [snapshot, pendingList] = await Promise.all([
        fetchSessionEvents(threadId, 0),
        fetchPendingDecisions(threadId),
      ]);
      setEvents(snapshot.events ?? []);
      setLastSeq(snapshot.last_seq ?? 0);
      setRunActive(!!snapshot.run_active);
      setPending(pendingList ?? []);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [threadId]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  useEffect(() => {
    const interval = setInterval(refresh, 3000);
    return () => clearInterval(interval);
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
      onResolved: () => refresh(),
    });
    return <div key={p.RequestID}>{card}</div>;
  });

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
            onClick={refresh}
            style={{
              padding: "0.2rem 0.5rem",
              border: "1px solid #d1d5db",
              background: "#f9fafb",
              borderRadius: 6,
              cursor: "pointer",
              fontSize: "0.7rem",
            }}
          >
            ↻
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
            History ({events.length}, last seq = {lastSeq})
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
        {!hidden && (
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
            {events.map((ev) => (
              <li
                key={`${ev.RunID}-${ev.Seq}`}
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
                  <code style={{ color: "#1e3a8a" }}>{ev.Kind}</code>
                  <span style={{ color: "#9ca3af" }}>#{ev.Seq}</span>
                </div>
                {renderEventSummary(ev)}
              </li>
            ))}
            {events.length === 0 && (
              <li style={{ color: "#9ca3af" }}>No events yet.</li>
            )}
          </ul>
        )}
      </section>
    </aside>
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
