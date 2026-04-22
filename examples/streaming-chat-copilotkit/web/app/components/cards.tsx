"use client";

import { useState } from "react";
import {
  resolveDecision,
  type DecisionChoice,
  type DecisionKind,
} from "../lib/backend";

// -----------------------------------------------------------------------------
// Shared styling helpers.
// -----------------------------------------------------------------------------

const cardStyle: React.CSSProperties = {
  border: "1px solid #e5e7eb",
  borderRadius: 10,
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

const tagStyle = (bg: string, fg: string): React.CSSProperties => ({
  background: bg,
  color: fg,
  borderRadius: 999,
  padding: "0.12rem 0.5rem",
  fontSize: "0.7rem",
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: "0.03em",
});

const btnStyle = (
  variant: "primary" | "secondary" | "danger" | "muted",
): React.CSSProperties => {
  const palette: Record<string, { bg: string; fg: string; border: string }> = {
    primary: { bg: "#1d4ed8", fg: "#fff", border: "#1d4ed8" },
    secondary: { bg: "#fff", fg: "#374151", border: "#d1d5db" },
    danger: { bg: "#b91c1c", fg: "#fff", border: "#b91c1c" },
    muted: { bg: "#f3f4f6", fg: "#6b7280", border: "#e5e7eb" },
  };
  const c = palette[variant];
  return {
    padding: "0.35rem 0.85rem",
    borderRadius: 6,
    border: `1px solid ${c.border}`,
    background: c.bg,
    color: c.fg,
    cursor: "pointer",
    fontSize: "0.85rem",
    fontWeight: 500,
  };
};

const codeStyle: React.CSSProperties = {
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
// DecisionState: shared local UI state machine.
// -----------------------------------------------------------------------------

type Outcome = "approved" | "rejected" | "answered" | "error";

type State =
  | { phase: "idle" }
  | { phase: "submitting" }
  | { phase: "resolved"; outcome: Outcome; choice?: string; message?: string }
  | { phase: "error"; message: string };

function useDecisionState(initial?: Outcome): [State, (s: State) => void] {
  const [state, setState] = useState<State>(() =>
    initial ? { phase: "resolved", outcome: initial } : { phase: "idle" },
  );
  return [state, setState];
}

// -----------------------------------------------------------------------------
// Card: PlanReview.
// -----------------------------------------------------------------------------

type DecisionCardCommonProps = {
  threadId: string;
  requestId: string;
  source: string;
  retryAttempt?: number;
  alreadyResolved?: Outcome;
  onResolved?: (outcome: Outcome) => void;
};

export function PlanReviewCard(
  props: DecisionCardCommonProps & { prompt?: string; plan?: string },
) {
  const {
    threadId,
    requestId,
    source,
    retryAttempt = 0,
    prompt,
    plan,
    alreadyResolved,
    onResolved,
  } = props;
  const [state, setState] = useDecisionState(alreadyResolved);

  async function submit(result: "approved" | "rejected") {
    setState({ phase: "submitting" });
    try {
      await resolveDecision({ threadId, requestId, result });
      setState({ phase: "resolved", outcome: result });
      onResolved?.(result);
    } catch (err) {
      setState({
        phase: "error",
        message: err instanceof Error ? err.message : "unknown error",
      });
    }
  }

  return (
    <div style={cardStyle}>
      <div style={headerStyle}>
        <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
          <span style={tagStyle("#dbeafe", "#1e3a8a")}>plan review</span>
          <code style={{ fontSize: "0.78rem", color: "#6b7280" }}>{source}</code>
          {retryAttempt > 0 && (
            <span style={tagStyle("#fef3c7", "#92400e")}>
              retry #{retryAttempt}
            </span>
          )}
        </div>
        <OutcomeBadge state={state} />
      </div>
      {prompt && (
        <p style={{ margin: 0, color: "#111827" }}>{prompt}</p>
      )}
      {plan && (
        <pre style={codeStyle}>{plan}</pre>
      )}
      {state.phase !== "resolved" && (
        <div style={{ display: "flex", gap: "0.5rem", justifyContent: "flex-end" }}>
          <button
            type="button"
            style={btnStyle("secondary")}
            disabled={state.phase === "submitting"}
            onClick={() => submit("rejected")}
          >
            Reject
          </button>
          <button
            type="button"
            style={btnStyle("primary")}
            disabled={state.phase === "submitting"}
            onClick={() => submit("approved")}
          >
            Approve
          </button>
        </div>
      )}
      {state.phase === "error" && (
        <p style={{ margin: 0, color: "#b91c1c", fontSize: "0.85rem" }}>
          {state.message}
        </p>
      )}
    </div>
  );
}

// -----------------------------------------------------------------------------
// Card: Permission (tool-call gate).
// -----------------------------------------------------------------------------

export function PermissionCard(
  props: DecisionCardCommonProps & {
    prompt?: string;
    tool?: string;
    command?: string;
    args?: Record<string, unknown>;
  },
) {
  const {
    threadId,
    requestId,
    source,
    retryAttempt = 0,
    prompt,
    tool,
    command,
    args,
    alreadyResolved,
    onResolved,
  } = props;
  const [state, setState] = useDecisionState(alreadyResolved);

  async function submit(result: "approved" | "rejected") {
    setState({ phase: "submitting" });
    try {
      await resolveDecision({ threadId, requestId, result });
      setState({ phase: "resolved", outcome: result });
      onResolved?.(result);
    } catch (err) {
      setState({
        phase: "error",
        message: err instanceof Error ? err.message : "unknown error",
      });
    }
  }

  return (
    <div style={cardStyle}>
      <div style={headerStyle}>
        <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
          <span style={tagStyle("#fee2e2", "#991b1b")}>permission</span>
          <code style={{ fontSize: "0.78rem", color: "#6b7280" }}>{source}</code>
          {retryAttempt > 0 && (
            <span style={tagStyle("#fef3c7", "#92400e")}>
              retry #{retryAttempt}
            </span>
          )}
        </div>
        <OutcomeBadge state={state} />
      </div>
      {prompt && <p style={{ margin: 0, color: "#111827" }}>{prompt}</p>}
      {tool && (
        <div style={{ fontSize: "0.85rem", color: "#374151" }}>
          Tool: <code>{tool}</code>
        </div>
      )}
      {command && <pre style={codeStyle}>{command}</pre>}
      {args && !command && <pre style={codeStyle}>{JSON.stringify(args, null, 2)}</pre>}
      {state.phase !== "resolved" && (
        <div style={{ display: "flex", gap: "0.5rem", justifyContent: "flex-end" }}>
          <button
            type="button"
            style={btnStyle("danger")}
            disabled={state.phase === "submitting"}
            onClick={() => submit("rejected")}
          >
            Deny
          </button>
          <button
            type="button"
            style={btnStyle("primary")}
            disabled={state.phase === "submitting"}
            onClick={() => submit("approved")}
          >
            Allow
          </button>
        </div>
      )}
      {state.phase === "error" && (
        <p style={{ margin: 0, color: "#b91c1c", fontSize: "0.85rem" }}>
          {state.message}
        </p>
      )}
    </div>
  );
}

// -----------------------------------------------------------------------------
// Card: Question (structured ask-user-question).
// -----------------------------------------------------------------------------

export function QuestionCard(
  props: DecisionCardCommonProps & {
    prompt?: string;
    choices?: DecisionChoice[];
    schema?: Record<string, unknown>;
    // questionType mirrors claude's AskUserQuestion.questions[].type
    // ("freeText" | "multipleChoice" | "singleChoice" | ...). Unset
    // means "infer from choices presence".
    questionType?: string;
  },
) {
  const {
    threadId,
    requestId,
    source,
    retryAttempt = 0,
    prompt,
    choices,
    questionType,
    alreadyResolved,
    onResolved,
  } = props;
  const [state, setState] = useDecisionState(alreadyResolved);
  const [freeform, setFreeform] = useState("");

  async function submit(
    result: "answered" | "rejected",
    opts?: { choice?: string; answer?: Record<string, unknown>; text?: string },
  ) {
    setState({ phase: "submitting" });
    try {
      await resolveDecision({
        threadId,
        requestId,
        result,
        choice: opts?.choice,
        answer: opts?.answer,
        text: opts?.text,
      });
      setState({
        phase: "resolved",
        outcome: result,
        choice: opts?.choice,
      });
      onResolved?.(result);
    } catch (err) {
      setState({
        phase: "error",
        message: err instanceof Error ? err.message : "unknown error",
      });
    }
  }

  const hasChoices = !!choices && choices.length > 0;
  // Show a text input when the backend declared freeText, or when there
  // are no choices to click (default case for unknown question types).
  const showFreeText =
    questionType === "freeText" || (!hasChoices && !questionType) || (!hasChoices && questionType !== "");

  return (
    <div style={cardStyle}>
      <div style={headerStyle}>
        <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
          <span style={tagStyle("#ede9fe", "#5b21b6")}>question</span>
          <code style={{ fontSize: "0.78rem", color: "#6b7280" }}>{source}</code>
          {questionType && (
            <span style={tagStyle("#f3f4f6", "#6b7280")}>{questionType}</span>
          )}
          {retryAttempt > 0 && (
            <span style={tagStyle("#fef3c7", "#92400e")}>
              retry #{retryAttempt}
            </span>
          )}
        </div>
        <OutcomeBadge state={state} />
      </div>
      {prompt ? (
        <p style={{ margin: 0, color: "#111827", fontWeight: 500 }}>{prompt}</p>
      ) : (
        <p style={{ margin: 0, color: "#9ca3af", fontStyle: "italic" }}>
          (the agent didn&apos;t supply a prompt; it may be asking a generic
          clarifying question)
        </p>
      )}
      {state.phase !== "resolved" && (
        <>
          {hasChoices && (
            <div
              style={{
                display: "grid",
                gap: "0.4rem",
                gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))",
              }}
            >
              {choices!.map((c, idx) => {
                const choiceKey = c.key || c.label || `choice-${idx + 1}`;
                const choiceLabel = c.label || c.key || `Option ${idx + 1}`;
                return (
                  <button
                    key={choiceKey}
                    type="button"
                    disabled={state.phase === "submitting"}
                    onClick={() =>
                      submit("answered", {
                        choice: choiceKey,
                        text: choiceLabel,
                        answer: { choice: choiceKey, label: choiceLabel },
                      })
                    }
                    style={{
                      ...btnStyle("secondary"),
                      textAlign: "left",
                      padding: "0.6rem 0.75rem",
                    }}
                  >
                    <div style={{ fontWeight: 600 }}>{choiceLabel}</div>
                    {c.description && (
                      <div style={{ fontSize: "0.78rem", color: "#6b7280" }}>
                        {c.description}
                      </div>
                    )}
                  </button>
                );
              })}
            </div>
          )}
          {showFreeText && (
            <textarea
              placeholder="Type your answer…"
              value={freeform}
              onChange={(e) => setFreeform(e.target.value)}
              rows={3}
              style={{
                padding: "0.5rem 0.75rem",
                border: "1px solid #d1d5db",
                borderRadius: 6,
                fontSize: "0.9rem",
                fontFamily: "inherit",
                resize: "vertical",
              }}
            />
          )}
          <div style={{ display: "flex", gap: "0.5rem", justifyContent: "flex-end" }}>
            <button
              type="button"
              style={btnStyle("muted")}
              disabled={state.phase === "submitting"}
              onClick={() => submit("rejected")}
            >
              Skip
            </button>
            {showFreeText && (
              <button
                type="button"
                style={btnStyle("primary")}
                disabled={state.phase === "submitting" || freeform.trim() === ""}
                onClick={() =>
                  submit("answered", {
                    text: freeform.trim(),
                    answer: { text: freeform.trim() },
                  })
                }
              >
                Submit
              </button>
            )}
          </div>
        </>
      )}
      {state.phase === "error" && (
        <p style={{ margin: 0, color: "#b91c1c", fontSize: "0.85rem" }}>
          {state.message}
        </p>
      )}
    </div>
  );
}

// -----------------------------------------------------------------------------
// Generic tool-call card: shown for any native (non-dec.*) tool call so users
// can inspect arguments and (once completed) results.
// -----------------------------------------------------------------------------

export function ToolCallCard(props: {
  name: string;
  status: "inProgress" | "executing" | "complete";
  args?: unknown;
  result?: unknown;
}) {
  const { name, status, args, result } = props;
  const statusColor =
    status === "complete"
      ? tagStyle("#dcfce7", "#166534")
      : status === "executing"
        ? tagStyle("#dbeafe", "#1e3a8a")
        : tagStyle("#fef3c7", "#92400e");

  return (
    <div style={cardStyle}>
      <div style={headerStyle}>
        <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
          <span style={tagStyle("#f3f4f6", "#374151")}>tool_call</span>
          <code style={{ fontSize: "0.85rem", fontWeight: 600 }}>{name}</code>
        </div>
        <span style={statusColor}>{status}</span>
      </div>
      {args !== undefined && (
        <details>
          <summary style={{ cursor: "pointer", fontSize: "0.82rem", color: "#6b7280" }}>
            args
          </summary>
          <pre style={codeStyle}>{formatJSON(args)}</pre>
        </details>
      )}
      {result !== undefined && result !== null && (
        <details open>
          <summary style={{ cursor: "pointer", fontSize: "0.82rem", color: "#6b7280" }}>
            result
          </summary>
          <pre style={codeStyle}>{formatJSON(result)}</pre>
        </details>
      )}
    </div>
  );
}

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

// -----------------------------------------------------------------------------
// OutcomeBadge.
// -----------------------------------------------------------------------------

function OutcomeBadge({ state }: { state: State }) {
  if (state.phase === "resolved") {
    const palette: Record<Outcome, { bg: string; fg: string }> = {
      approved: { bg: "#dcfce7", fg: "#166534" },
      answered: { bg: "#e0e7ff", fg: "#3730a3" },
      rejected: { bg: "#fee2e2", fg: "#991b1b" },
      error: { bg: "#fef3c7", fg: "#92400e" },
    };
    const p = palette[state.outcome];
    return (
      <span style={tagStyle(p.bg, p.fg)}>
        {state.outcome}
        {state.choice ? `: ${state.choice}` : ""}
      </span>
    );
  }
  if (state.phase === "submitting") {
    return <span style={tagStyle("#f3f4f6", "#6b7280")}>submitting…</span>;
  }
  if (state.phase === "error") {
    return <span style={tagStyle("#fef3c7", "#92400e")}>error</span>;
  }
  return <span style={tagStyle("#fffbeb", "#92400e")}>pending</span>;
}

// -----------------------------------------------------------------------------
// DecisionRouter: picks the right card for a known dec.* tool name.
// -----------------------------------------------------------------------------

export type DecisionRouterArgs = {
  kind?: DecisionKind;
  source?: string;
  prompt?: string;
  payload?: Record<string, unknown>;
  choices?: DecisionChoice[];
  tool_call_id?: string;
  retry_attempt?: number;
};

export function renderDecisionCard(opts: {
  threadId: string;
  name: string;
  requestId: string;
  args: DecisionRouterArgs | undefined;
  alreadyResolved?: Outcome;
  onResolved?: (outcome: Outcome) => void;
}): React.ReactElement | null {
  const { threadId, name, requestId, args, alreadyResolved, onResolved } = opts;
  const kind = args?.kind ?? inferKindFromName(name);
  const source = args?.source ?? name;
  const prompt = args?.prompt;
  const payload = args?.payload ?? {};
  const choices = args?.choices;
  const retry = args?.retry_attempt ?? 0;

  switch (kind) {
    case "plan_review":
      return (
        <PlanReviewCard
          threadId={threadId}
          requestId={requestId}
          source={source}
          retryAttempt={retry}
          prompt={prompt}
          plan={typeof payload.plan === "string" ? (payload.plan as string) : undefined}
          alreadyResolved={alreadyResolved}
          onResolved={onResolved}
        />
      );
    case "permission":
      return (
        <PermissionCard
          threadId={threadId}
          requestId={requestId}
          source={source}
          retryAttempt={retry}
          prompt={prompt}
          tool={typeof payload.tool === "string" ? (payload.tool as string) : undefined}
          command={typeof payload.command === "string" ? (payload.command as string) : undefined}
          args={payload}
          alreadyResolved={alreadyResolved}
          onResolved={onResolved}
        />
      );
    case "question":
      return (
        <QuestionCard
          threadId={threadId}
          requestId={requestId}
          source={source}
          retryAttempt={retry}
          prompt={prompt ?? extractQuestionPrompt(payload)}
          choices={choices ?? undefined}
          schema={
            typeof payload.schema === "object"
              ? (payload.schema as Record<string, unknown>)
              : undefined
          }
          questionType={
            typeof payload.question_type === "string"
              ? (payload.question_type as string)
              : undefined
          }
          alreadyResolved={alreadyResolved}
          onResolved={onResolved}
        />
      );
  }
  return null;
}

// extractQuestionPrompt digs the question text out of claude's native
// AskUserQuestion payload shape when the backend didn't already surface it
// on the top-level `prompt` field. Accepts:
//   { "questions": [ { "question": "..." } ] }   (claude)
//   { "prompt": "..." }                            (already normalised)
function extractQuestionPrompt(payload: Record<string, unknown>): string | undefined {
  if (typeof payload.prompt === "string" && payload.prompt.trim() !== "") {
    return payload.prompt;
  }
  const questions = payload.questions;
  if (Array.isArray(questions) && questions.length > 0) {
    const first = questions[0];
    if (first && typeof first === "object") {
      const q = (first as Record<string, unknown>).question;
      if (typeof q === "string") return q;
    }
  }
  return undefined;
}

function inferKindFromName(name: string): DecisionKind | undefined {
  if (name.startsWith("dec.plan_review.")) return "plan_review";
  if (name.startsWith("dec.permission.")) return "permission";
  if (name.startsWith("dec.question.")) return "question";
  return undefined;
}
