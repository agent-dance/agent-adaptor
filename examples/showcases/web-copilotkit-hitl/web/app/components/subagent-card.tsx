"use client";

import React, { useEffect, useId, useState } from "react";
import type { ActivityMessage } from "@ag-ui/core";
import type { AbstractAgent } from "@ag-ui/client";
import type {
  SubagentActivityContent,
  SubagentToolCall,
} from "../lib/subagent-schema";

// -----------------------------------------------------------------------------
// Styling — mirrors the card palette used in cards.tsx
// -----------------------------------------------------------------------------

const cardStyle: React.CSSProperties = {
  border: "1px solid #e5e7eb",
  borderRadius: 8,
  padding: "0.9rem 1rem",
  background: "#fff",
  margin: "0.5rem 0",
  display: "grid",
  gap: "0.5rem",
  boxShadow: "0 1px 2px rgba(0,0,0,0.04)",
};

const headerStyle: React.CSSProperties = {
  display: "flex",
  justifyContent: "space-between",
  alignItems: "center",
  gap: "0.5rem",
};

function tagStyle(bg: string, fg: string): React.CSSProperties {
  return {
    background: bg,
    color: fg,
    borderRadius: 999,
    padding: "0.12rem 0.5rem",
    fontSize: "0.7rem",
    fontWeight: 600,
    textTransform: "uppercase" as const,
    letterSpacing: 0,
    whiteSpace: "nowrap",
  };
}

const codeBlockStyle: React.CSSProperties = {
  background: "#0f172a",
  color: "#e2e8f0",
  borderRadius: 8,
  padding: "0.75rem",
  fontSize: "0.82rem",
  whiteSpace: "pre-wrap",
  overflowX: "auto",
  lineHeight: 1.45,
};

// -----------------------------------------------------------------------------
// Status badge palette
// -----------------------------------------------------------------------------

type StatusPalette = { bg: string; fg: string };

const STATUS_PALETTE: Record<string, StatusPalette> = {
  started: { bg: "#fef3c7", fg: "#92400e" },
  running: { bg: "#dbeafe", fg: "#1e3a8a" },
  completed: { bg: "#dcfce7", fg: "#166534" },
  failed: { bg: "#fee2e2", fg: "#991b1b" },
  cancelled: { bg: "#f3f4f6", fg: "#6b7280" },
  input_required: { bg: "#ede9fe", fg: "#5b21b6" },
};

function statusColor(status: string): StatusPalette {
  return STATUS_PALETTE[status] ?? { bg: "#f3f4f6", fg: "#6b7280" };
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

function formatJSON(v: unknown): string {
  if (typeof v === "string") {
    try {
      return JSON.stringify(JSON.parse(v), null, 2);
    } catch {
      return v;
    }
  }
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

function formatDuration(ms: number | undefined, startedAt: string | undefined): string | null {
  if (ms !== undefined) {
    return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
  }
  if (startedAt) {
    const start = Date.parse(startedAt);
    if (!isNaN(start)) {
      const elapsed = Date.now() - start;
      return elapsed < 1000 ? `${elapsed}ms` : `${(elapsed / 1000).toFixed(1)}s`;
    }
  }
  return null;
}

// -----------------------------------------------------------------------------
// ToolCallRow — a single internal tool call inside the subagent
// -----------------------------------------------------------------------------

function ToolCallRow({ tc }: { tc: SubagentToolCall }) {
  const [open, setOpen] = useState(false);
  const statusPalette =
    tc.status === "completed"
      ? { bg: "#dcfce7", fg: "#166534" }
      : tc.status === "executing" || tc.status === "running"
        ? { bg: "#dbeafe", fg: "#1e3a8a" }
        : { bg: "#fef3c7", fg: "#92400e" };

  const hasDetail = tc.args !== undefined || tc.result !== undefined;

  return (
    <div
      style={{
        border: "1px solid #f0f0f0",
        borderRadius: 6,
        padding: "0.4rem 0.6rem",
        fontSize: "0.8rem",
        background: "#fafafa",
      }}
      aria-label={`tool call: ${tc.name}, status: ${tc.status}`}
    >
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          gap: "0.5rem",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "0.4rem" }}>
          <span style={tagStyle("#f3f4f6", "#374151")}>tool</span>
          <code style={{ fontWeight: 600 }}>{tc.name}</code>
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: "0.4rem" }}>
          <span style={tagStyle(statusPalette.bg, statusPalette.fg)}>{tc.status}</span>
          {hasDetail && (
            <button
              type="button"
              aria-expanded={open}
              aria-label={open ? "collapse tool detail" : "expand tool detail"}
              onClick={() => setOpen((v) => !v)}
              style={{
                border: "1px solid #d1d5db",
                background: "#f9fafb",
                borderRadius: 4,
                padding: "0.1rem 0.4rem",
                fontSize: "0.7rem",
                cursor: "pointer",
                lineHeight: 1.4,
              }}
            >
              {open ? "▲" : "▼"}
            </button>
          )}
        </div>
      </div>
      {open && hasDetail && (
        <div style={{ marginTop: "0.4rem", display: "grid", gap: "0.3rem" }}>
          {tc.args !== undefined && (
            <details open>
              <summary
                style={{ cursor: "pointer", color: "#6b7280", fontSize: "0.78rem" }}
              >
                args
              </summary>
              <pre style={codeBlockStyle}>{formatJSON(tc.args)}</pre>
            </details>
          )}
          {tc.result !== undefined && (
            <details open>
              <summary
                style={{ cursor: "pointer", color: "#6b7280", fontSize: "0.78rem" }}
              >
                result
              </summary>
              <pre style={codeBlockStyle}>{formatJSON(tc.result)}</pre>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

// -----------------------------------------------------------------------------
// SubagentCard — main component
// -----------------------------------------------------------------------------

export interface SubagentCardProps {
  activityType: string;
  content: SubagentActivityContent;
  message: ActivityMessage;
  agent: AbstractAgent | undefined;
}

export function SubagentCard({ content }: SubagentCardProps) {
  const [toolCallsOpen, setToolCallsOpen] = useState(true);
  const [, setClock] = useState(0);
  const toolCallsID = useId();

  const status = content.status ?? "started";
  const pal = statusColor(status);
  const displayName = content.agentName ?? content.agentKey;
  const toolCalls = content.toolCalls ?? [];
  const duration = formatDuration(content.durationMs, content.startedAt);
  const isTerminal =
    status === "completed" || status === "failed" || status === "cancelled";

  useEffect(() => {
    if (isTerminal) return;
    const timer = window.setInterval(() => setClock(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [isTerminal]);

  // Compute usage summary line
  const usageParts: string[] = [];
  if (content.usage) {
    const u = content.usage as Record<string, unknown>;
    if (typeof u["input_tokens"] === "number")
      usageParts.push(`in: ${u["input_tokens"]}`);
    if (typeof u["output_tokens"] === "number")
      usageParts.push(`out: ${u["output_tokens"]}`);
    if (typeof u["total_tokens"] === "number" && usageParts.length === 0)
      usageParts.push(`tokens: ${u["total_tokens"]}`);
  }

  return (
    <div
      style={cardStyle}
      role="region"
      aria-label={`subagent: ${displayName}`}
    >
      {/* Header */}
      <div style={headerStyle}>
        <div style={{ display: "flex", alignItems: "center", gap: "0.5rem", minWidth: 0 }}>
          <span style={tagStyle("#e0e7ff", "#3730a3")}>subagent</span>
          <span
            style={{
              fontWeight: 600,
              fontSize: "0.9rem",
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
            }}
            title={displayName}
          >
            {displayName}
          </span>
          {content.kind && (
            <span style={tagStyle("#f3f4f6", "#6b7280")}>{content.kind}</span>
          )}
        </div>
        <span style={tagStyle(pal.bg, pal.fg)} aria-live="polite">
          {status}
        </span>
      </div>

      {/* Description */}
      {content.description && (
        <p style={{ margin: 0, color: "#374151", fontSize: "0.88rem" }}>
          {content.description}
        </p>
      )}
      {!isTerminal && !content.description && !content.text && toolCalls.length === 0 && (
        <p style={{ margin: 0, color: "#6b7280", fontSize: "0.82rem" }}>
          Waiting for provider activity…
        </p>
      )}

      {/* Streaming / final text */}
      {content.text && (
        <div
          style={{
            background: "#f8fafc",
            border: "1px solid #e2e8f0",
            borderRadius: 6,
            padding: "0.6rem 0.75rem",
            fontSize: "0.85rem",
            color: "#1e293b",
            whiteSpace: "pre-wrap",
            lineHeight: 1.55,
          }}
          aria-label="subagent output text"
        >
          {content.text}
          {/* blinking cursor while running */}
          {!isTerminal && (
            <span
              style={{ display: "inline-block", marginLeft: 2, animation: "blink 1s step-end infinite" }}
              aria-hidden="true"
            >
              ▍
            </span>
          )}
        </div>
      )}

      {/* Reasoning stays visually and semantically separate from assistant text. */}
      {content.reasoning && (
        <details
          style={{
            border: "1px solid #e5e7eb",
            borderRadius: 6,
            background: "#fafafa",
            padding: "0.45rem 0.6rem",
          }}
        >
          <summary
            style={{
              cursor: "pointer",
              color: "#6b7280",
              fontSize: "0.78rem",
              fontWeight: 600,
              textTransform: "uppercase",
            }}
          >
            reasoning
          </summary>
          <div
            aria-label="subagent reasoning"
            style={{
              marginTop: "0.45rem",
              color: "#4b5563",
              fontSize: "0.82rem",
              fontStyle: "italic",
              lineHeight: 1.5,
              whiteSpace: "pre-wrap",
            }}
          >
            {content.reasoning}
          </div>
        </details>
      )}

      {/* Internal tool calls */}
      {toolCalls.length > 0 && (
        <section aria-label="internal tool calls">
          <div
            style={{
              display: "flex",
              justifyContent: "space-between",
              alignItems: "center",
              marginBottom: "0.3rem",
            }}
          >
            <span
              style={{ fontSize: "0.75rem", color: "#6b7280", textTransform: "uppercase" }}
            >
              tool calls ({toolCalls.length})
            </span>
            <button
              type="button"
              aria-expanded={toolCallsOpen}
              aria-controls={toolCallsID}
              onClick={() => setToolCallsOpen((v) => !v)}
              style={{
                border: "1px solid #d1d5db",
                background: "#f9fafb",
                borderRadius: 4,
                padding: "0.1rem 0.4rem",
                fontSize: "0.7rem",
                cursor: "pointer",
              }}
            >
              {toolCallsOpen ? "hide" : "show"}
            </button>
          </div>
          {toolCallsOpen && (
            <div
              id={toolCallsID}
              style={{ display: "grid", gap: "0.25rem" }}
            >
              {toolCalls.map((tc) => (
                <ToolCallRow key={tc.id} tc={tc} />
              ))}
            </div>
          )}
        </section>
      )}

      {/* Error */}
      {content.error && (
        <div
          role="alert"
          style={{
            background: "#fee2e2",
            border: "1px solid #fca5a5",
            borderRadius: 6,
            padding: "0.5rem 0.75rem",
            fontSize: "0.82rem",
            color: "#991b1b",
          }}
        >
          <span style={{ fontWeight: 600 }}>error: </span>
          {formatJSON(content.error)}
        </div>
      )}

      {/* Footer: duration + usage */}
      {(duration || usageParts.length > 0) && (
        <div
          style={{
            display: "flex",
            gap: "0.75rem",
            fontSize: "0.75rem",
            color: "#9ca3af",
            borderTop: "1px solid #f3f4f6",
            paddingTop: "0.35rem",
            flexWrap: "wrap",
          }}
        >
          {duration && (
            <span title="elapsed time">⏱ {duration}</span>
          )}
          {usageParts.length > 0 && (
            <span title="token usage">tokens: {usageParts.join(", ")}</span>
          )}
        </div>
      )}
    </div>
  );
}
