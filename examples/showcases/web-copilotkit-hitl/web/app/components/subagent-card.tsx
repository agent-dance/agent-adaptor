"use client";

import React, { useEffect, useId, useState } from "react";
import type { ActivityMessage } from "@ag-ui/core";
import type { AbstractAgent } from "@ag-ui/client";
import {
  extractCodingPlan,
  type CodingPlan,
  type SubagentActivityContent,
  type SubagentToolCall,
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
  // minmax(0, 1fr) lets the single column shrink below its content's
  // min-content width so wide descendants (dark <pre> code blocks, tool-call
  // rows) scroll/wrap inside the card instead of blowing it past a narrow
  // container (the ~360px side panel).
  gridTemplateColumns: "minmax(0, 1fr)",
  gap: "0.5rem",
  minWidth: 0,
  maxWidth: "100%",
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
  overflowWrap: "anywhere",
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

// providerBase derives the underlying agent "base" (Codex / Claude Code / …)
// for the header tag. The bridge does not carry an explicit provider field, but
// hosts name delegated roles after their base (e.g. "Codex planner", "Claude
// Code implementer"), so we match the provider keyword in agentName/agentKey.
type BasePalette = { label: string; bg: string; fg: string };

function providerBase(content: SubagentActivityContent): BasePalette | null {
  const hay = `${content.agentName ?? ""} ${content.agentKey ?? ""}`.toLowerCase();
  if (hay.includes("claude")) return { label: "Claude Code", bg: "#fdece5", fg: "#c2410c" };
  if (hay.includes("codex")) return { label: "Codex", bg: "#e5e7eb", fg: "#111827" };
  if (hay.includes("cursor")) return { label: "Cursor", bg: "#e0f2fe", fg: "#075985" };
  if (hay.includes("codebuddy")) return { label: "CodeBuddy", bg: "#dcfce7", fg: "#166534" };
  if (hay.includes("gemini")) return { label: "Gemini", bg: "#e0e7ff", fg: "#3730a3" };
  return null;
}

// stripBasePrefix drops the leading provider word(s) from a role name once the
// base is shown as its own tag, so "Codex planner" renders as "planner" and
// "Claude Code implementer" as "implementer". Falls back to the original name.
function stripBasePrefix(name: string): string {
  const stripped = name
    .replace(/^(claude\s+code|claude|codex|cursor|codebuddy|gemini)\s+/i, "")
    .trim();
  return stripped || name;
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
        <div
          style={{
            marginTop: "0.4rem",
            display: "grid",
            gridTemplateColumns: "minmax(0, 1fr)",
            gap: "0.3rem",
          }}
        >
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
  const base = providerBase(content);
  // Structured-output deliverable: if this role returned a validated coding
  // plan, render it as an attachment instead of dumping the raw JSON as text.
  const plan = extractCodingPlan(content);

  useEffect(() => {
    if (isTerminal) return;
    const timer = window.setInterval(() => setClock(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [isTerminal]);

  // Auto-collapse once the subagent finishes so completed cards stay compact;
  // the user can still re-expand by clicking the header. The effect only fires
  // on the transition into a terminal status, so a manual re-expand sticks.
  const [collapsed, setCollapsed] = useState(false);
  const hasPlan = plan !== null;
  // Auto-collapse finished cards to stay compact — EXCEPT plan cards carrying a
  // coding-plan attachment, which is the demo's key deliverable and should stay
  // visible.
  useEffect(() => {
    if (isTerminal && !hasPlan) setCollapsed(true);
  }, [isTerminal, hasPlan]);
  // When the plan attachment first appears (it lands around completion), make
  // sure the card is expanded so the attachment is visible.
  useEffect(() => {
    if (hasPlan) setCollapsed(false);
  }, [hasPlan]);

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
      {/* Header (click to collapse/expand; auto-collapses when terminal) */}
      <div
        style={{ ...headerStyle, cursor: "pointer" }}
        onClick={() => setCollapsed((v) => !v)}
        role="button"
        tabIndex={0}
        aria-expanded={!collapsed}
        aria-label={`${collapsed ? "expand" : "collapse"} subagent ${displayName}`}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            setCollapsed((v) => !v);
          }
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "0.5rem", minWidth: 0 }}>
          <span style={tagStyle("#e0e7ff", "#3730a3")}>subagent</span>
          {base && (
            <span style={tagStyle(base.bg, base.fg)} title={`base: ${base.label}`}>
              {base.label}
            </span>
          )}
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
            {base ? stripBasePrefix(displayName) : displayName}
          </span>
          {content.kind && (
            <span style={tagStyle("#f3f4f6", "#6b7280")}>{content.kind}</span>
          )}
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: "0.4rem", flexShrink: 0 }}>
          <span style={tagStyle(pal.bg, pal.fg)} aria-live="polite">
            {status}
          </span>
          <span aria-hidden="true" style={{ color: "#9ca3af", fontSize: "0.8rem" }}>
            {collapsed ? "▸" : "▾"}
          </span>
        </div>
      </div>

      {!collapsed && (
        <>
      {/* Description */}
      {content.description && (
        <p
          style={{
            margin: 0,
            color: "#374151",
            fontSize: "0.88rem",
            overflowWrap: "anywhere",
          }}
        >
          {content.description}
        </p>
      )}
      {!isTerminal && !content.description && !content.text && toolCalls.length === 0 && (
        <p style={{ margin: 0, color: "#6b7280", fontSize: "0.82rem" }}>
          Waiting for provider activity…
        </p>
      )}

      {/* Structured coding plan (attachment). Rendered instead of raw text. */}
      {plan && <CodingPlanAttachment plan={plan} name={displayName} />}

      {/* Streaming / final text (suppressed when the text is the plan JSON). */}
      {content.text && !plan && (
        <div
          style={{
            background: "#f8fafc",
            border: "1px solid #e2e8f0",
            borderRadius: 6,
            padding: "0.6rem 0.75rem",
            fontSize: "0.85rem",
            color: "#1e293b",
            whiteSpace: "pre-wrap",
            overflowWrap: "anywhere",
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
              overflowWrap: "anywhere",
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
              style={{
                display: "grid",
                gridTemplateColumns: "minmax(0, 1fr)",
                gap: "0.25rem",
              }}
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
            overflowWrap: "anywhere",
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
        </>
      )}
    </div>
  );
}

// -----------------------------------------------------------------------------
// CodingPlanAttachment renders a validated codingPlan (the plan role's
// structured-output deliverable) as a file-style attachment: a header with a
// download link plus a readable rendering of the summary, ordered steps, and
// acceptance checks.
// -----------------------------------------------------------------------------
function CodingPlanAttachment({ plan, name }: { plan: CodingPlan; name: string }) {
  const download = () => {
    const blob = new Blob([JSON.stringify(plan, null, 2)], {
      type: "application/json",
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "coding-plan.json";
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <section
      aria-label="coding plan attachment"
      style={{
        border: "1px solid #c7d2fe",
        borderRadius: 8,
        background: "#f5f7ff",
        padding: "0.6rem 0.75rem",
        display: "grid",
        gridTemplateColumns: "minmax(0, 1fr)",
        gap: "0.5rem",
        minWidth: 0,
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: "0.5rem",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "0.4rem", minWidth: 0 }}>
          <span aria-hidden="true">📎</span>
          <strong style={{ fontSize: "0.85rem", color: "#3730a3" }}>coding-plan.json</strong>
          <span style={tagStyle("#e0e7ff", "#3730a3")}>structured output</span>
        </div>
        <button
          type="button"
          onClick={download}
          aria-label={`download coding plan from ${name}`}
          style={{
            border: "1px solid #c7d2fe",
            background: "#fff",
            borderRadius: 4,
            padding: "0.1rem 0.5rem",
            fontSize: "0.72rem",
            color: "#4338ca",
            cursor: "pointer",
            flexShrink: 0,
          }}
        >
          download
        </button>
      </div>

      {plan.summary && (
        <p
          style={{
            margin: 0,
            fontSize: "0.85rem",
            color: "#1e293b",
            overflowWrap: "anywhere",
          }}
        >
          {plan.summary}
        </p>
      )}

      {plan.steps.length > 0 && (
        <ol
          style={{
            margin: 0,
            paddingLeft: "1.1rem",
            display: "grid",
            gap: "0.3rem",
            fontSize: "0.83rem",
            color: "#1f2937",
          }}
        >
          {plan.steps.map((step, i) => (
            <li key={i} style={{ overflowWrap: "anywhere" }}>
              <span style={{ fontWeight: 600 }}>{step.title}</span>
              {step.detail && (
                <div style={{ color: "#4b5563", marginTop: "0.1rem" }}>{step.detail}</div>
              )}
            </li>
          ))}
        </ol>
      )}

      {plan.acceptance_checks.length > 0 && (
        <div>
          <div
            style={{
              fontSize: "0.72rem",
              textTransform: "uppercase",
              color: "#6b7280",
              marginBottom: "0.2rem",
            }}
          >
            acceptance checks
          </div>
          <ul
            style={{
              margin: 0,
              paddingLeft: "1.1rem",
              fontSize: "0.82rem",
              color: "#374151",
              display: "grid",
              gap: "0.15rem",
            }}
          >
            {plan.acceptance_checks.map((check, i) => (
              <li key={i} style={{ overflowWrap: "anywhere" }}>
                {check}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
