"use client";

import { useEffect, useState } from "react";
import {
  extractPlanFileArtifact,
  type PlanFileArtifact,
  type SubagentActivity,
  type SubagentToolCall,
} from "../lib/subagent";

const statusPalette: Record<string, { background: string; color: string }> = {
  started: { background: "#fef3c7", color: "#92400e" },
  running: { background: "#dbeafe", color: "#1e3a8a" },
  completed: { background: "#dcfce7", color: "#166534" },
  failed: { background: "#fee2e2", color: "#991b1b" },
  cancelled: { background: "#f3f4f6", color: "#6b7280" },
  input_required: { background: "#ede9fe", color: "#5b21b6" },
};

export function SubagentCard({ content }: { content: SubagentActivity }) {
  const terminal = ["completed", "failed", "cancelled"].includes(content.status);
  const [collapsed, setCollapsed] = useState(false);
  const [, refreshClock] = useState(0);
  const displayName = content.agentName ?? content.agentKey;
  const palette = statusPalette[content.status] ?? statusPalette.started;
  const provider = providerBase(content);
  const planFile = extractPlanFileArtifact(content);
  const roleName = provider ? stripProviderPrefix(displayName) : displayName;

  useEffect(() => {
    if (terminal || !content.startedAt) return;
    const timer = window.setInterval(() => refreshClock(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [terminal, content.startedAt]);

  return (
    <article className="subagent-card" aria-label={`subagent ${displayName}`}>
      <button
        type="button"
        className="subagent-card-header"
        onClick={() => setCollapsed((value) => !value)}
        aria-expanded={!collapsed}
      >
        <span className="subagent-card-identity">
          <span className="subagent-role-tag">subagent</span>
          {provider && <span className="subagent-provider" style={provider.palette}>{provider.label}</span>}
          <strong title={displayName}>{roleName}</strong>
        </span>
        <span className="subagent-card-state">
          <span className="subagent-status" style={palette}>{content.status}</span>
          <span aria-hidden="true">{collapsed ? "▸" : "▾"}</span>
        </span>
      </button>

      {!collapsed && (
        <div className="subagent-card-body">
          {content.description && <p className="subagent-description">{content.description}</p>}
          {!content.description && !content.text && content.toolCalls.length === 0 && !terminal && (
            <p className="subagent-muted">Waiting for provider activity…</p>
          )}
          {planFile && <PlanFileAttachment file={planFile} />}
          {content.text && !planFile && (
            <div className="subagent-output">
              {content.text}
              {!terminal && <span className="subagent-cursor" aria-hidden="true">▍</span>}
            </div>
          )}
          {content.reasoning && (
            <details className="subagent-reasoning">
              <summary>Reasoning</summary>
              <div>{content.reasoning}</div>
            </details>
          )}
          {content.toolCalls.length > 0 && (
            <section className="subagent-tools" aria-label="Subagent tool calls">
              <span className="subagent-section-label">Tool calls ({content.toolCalls.length})</span>
              {content.toolCalls.map((call) => <ToolCallRow key={call.id} call={call} />)}
            </section>
          )}
          {content.error !== undefined && (
            <div className="subagent-error" role="alert">{formatValue(content.error)}</div>
          )}
          <footer className="subagent-footer">
            <span>{formatDuration(content.durationMs, content.startedAt)}</span>
            <span>{content.agentKey}</span>
          </footer>
        </div>
      )}
    </article>
  );
}

function PlanFileAttachment({ file }: { file: PlanFileArtifact }) {
  const download = () => {
    const blob = new Blob([file.content], { type: file.mediaType });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = file.filename;
    anchor.click();
    URL.revokeObjectURL(url);
  };

  return (
    <section className="plan-file" aria-label={`Plan file ${file.filename}`}>
      <header>
        <span className="plan-file-icon" aria-hidden="true">▤</span>
        <span className="plan-file-name">
          <strong>{file.filename}</strong>
          <small>{file.mediaType} · structured output</small>
        </span>
        <button type="button" onClick={download}>Download</button>
      </header>
      <p>{file.summary}</p>
      <pre>{file.content}</pre>
    </section>
  );
}

function providerBase(content: SubagentActivity): {
  label: string;
  palette: { background: string; color: string };
} | null {
  const value = `${content.agentName ?? ""} ${content.agentKey}`.toLowerCase();
  if (value.includes("claude")) return { label: "Claude Code", palette: { background: "#fdece5", color: "#c2410c" } };
  if (value.includes("codex")) return { label: "Codex", palette: { background: "#e5e7eb", color: "#111827" } };
  if (value.includes("cursor")) return { label: "Cursor", palette: { background: "#e0f2fe", color: "#075985" } };
  if (value.includes("codebuddy")) return { label: "CodeBuddy", palette: { background: "#dcfce7", color: "#166534" } };
  return null;
}

function stripProviderPrefix(name: string): string {
  return name.replace(/^(claude\s+code|claude|codex|cursor|codebuddy)\s+/i, "").trim() || name;
}

function ToolCallRow({ call }: { call: SubagentToolCall }) {
  const [open, setOpen] = useState(false);
  const hasDetail = call.args !== undefined || call.result !== undefined;
  return (
    <div className="subagent-tool">
      <button type="button" onClick={() => hasDetail && setOpen((value) => !value)} disabled={!hasDetail}>
        <span>{call.name}</span>
        <span>{call.status}{hasDetail ? (open ? " ▾" : " ▸") : ""}</span>
      </button>
      {open && <pre>{formatValue({ args: call.args, result: call.result })}</pre>}
    </div>
  );
}

function formatValue(value: unknown): string {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function formatDuration(durationMs: number | undefined, startedAt: string | undefined): string {
  let milliseconds = durationMs;
  if (milliseconds === undefined && startedAt) {
    const parsed = Date.parse(startedAt);
    if (!Number.isNaN(parsed)) milliseconds = Math.max(Date.now() - parsed, 0);
  }
  if (milliseconds === undefined) return "live";
  if (milliseconds < 1000) return `${milliseconds}ms`;
  return `${(milliseconds / 1000).toFixed(1)}s`;
}
