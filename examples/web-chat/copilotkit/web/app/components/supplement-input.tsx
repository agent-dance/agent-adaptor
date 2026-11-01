"use client";

import { useId, useRef, useState } from "react";
import type { FormEvent } from "react";

export function SupplementInput({ threadId }: { threadId: string }) {
  const id = useId();
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [status, setStatus] = useState<{ error: boolean; text: string } | null>(null);
  const inFlight = useRef(false);
  const pending = useRef<{ text: string; messageId: string } | null>(null);

  const send = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const message = text.trim();
    if (!message || inFlight.current) return;
    inFlight.current = true;
    setSending(true);
    setStatus(null);
    // If acceptance was uncertain, an unchanged retry keeps its original ID.
    if (pending.current?.text !== message) {
      pending.current = { text: message, messageId: crypto.randomUUID() };
    }
    try {
      const response = await fetch("/api/supplement", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ threadId, messageId: pending.current.messageId, text: message }),
      });
      const outcome: unknown = await response.json().catch(() => null);
      const accepted = outcome !== null && typeof outcome === "object"
        && "state" in outcome && outcome.state === "accepted"
        && "messageId" in outcome && outcome.messageId === pending.current.messageId;
      if (accepted) {
        setText("");
        pending.current = null;
        setStatus(response.ok
          ? { error: false, text: "Accepted for the active run. Delivery is pending." }
          : { error: true, text: "Accepted for the active run, but saving chat history failed. Delivery is pending." });
        return;
      }
      if (response.status === 409) {
        setStatus({ error: true, text: "There is no active run for this conversation. Start a new message in the chat above." });
        return;
      }
      if (response.status === 422) {
        setStatus({ error: true, text: "The active run rejected this guidance. Check whether additional input is enabled, or try a shorter message." });
        return;
      }
      setStatus({ error: true, text: "Acceptance could not be confirmed. Your text is kept; retrying will reuse its message ID." });
    } catch {
      setStatus({ error: true, text: "Acceptance could not be confirmed. Your text is kept; retrying will reuse its message ID." });
    } finally {
      inFlight.current = false;
      setSending(false);
    }
  };

  return (
    <form onSubmit={(event) => void send(event)} style={{ margin: "0.5rem 1rem 1rem" }}>
      <label htmlFor={id} style={{ display: "block", fontSize: "0.85rem", fontWeight: 600 }}>
        Add guidance to the active run
      </label>
      <p id={`${id}-hint`} style={{ margin: "0.3rem 0 0.5rem", color: "#64748b", fontSize: "0.78rem" }}>
        Use this while the agent is working. This keeps the current task running.
      </p>
      <textarea
        id={id}
        aria-describedby={`${id}-hint`}
        value={text}
        onChange={(event) => setText(event.target.value)}
        disabled={sending}
        rows={2}
        placeholder="Additional constraints or details…"
        style={{ width: "100%", resize: "vertical", padding: "0.6rem", border: "1px solid #d1d5db", borderRadius: 8, boxSizing: "border-box", font: "inherit", fontSize: "0.85rem" }}
      />
      <button
        type="submit"
        disabled={sending || !text.trim()}
        style={{ marginTop: "0.4rem", padding: "0.45rem 0.7rem", border: "1px solid #d1d5db", borderRadius: 7, background: "#f8fafc", color: "#1e293b", cursor: sending ? "wait" : "pointer" }}
      >
        {sending ? "Sending…" : "Send guidance"}
      </button>
      <p role="status" aria-live="polite" style={{ margin: "0.4rem 0 0", color: status?.error ? "#991b1b" : "#475569", fontSize: "0.78rem" }}>
        {status?.text}
      </p>
    </form>
  );
}
