import { z } from "zod";

// Mirrors the ACTIVITY_SNAPSHOT content shape emitted by pkg/bridges/agui
// for activityType="subagent". Fields are intentionally permissive (mostly
// optional) to tolerate partial snapshots, deltas, and future additions
// without breaking the render path.

export const SubagentToolCallSchema = z.object({
  id: z.string(),
  name: z.string(),
  status: z.string(),
  args: z.record(z.string(), z.unknown()).optional(),
  result: z.record(z.string(), z.unknown()).optional(),
});

export type SubagentToolCall = z.infer<typeof SubagentToolCallSchema>;

export const SubagentActivitySchema = z.object({
  subagentId: z.string(),
  runId: z.string().optional(),
  parentToolCallId: z.string().optional(),
  agentKey: z.string(),
  agentName: z.string().optional(),
  kind: z.enum(["native", "delegated"]).optional(),
  protocol: z.string().optional(),
  // Keep rendering if a future provider adds a status value the bridge does
  // not yet normalize; the card has a neutral fallback palette.
  status: z.string().optional().default("started"),
  description: z.string().optional(),
  text: z.string().optional(),
  reasoning: z.string().optional(),
  toolCalls: z.array(SubagentToolCallSchema).optional().default([]),
  usage: z.record(z.string(), z.unknown()).optional(),
  // result carries the terminal delegation payload. For delegated roles the
  // bridge sets { status, text } on completion, where text is the role's final
  // assistant output — for a structured-output role that text is the validated
  // JSON (e.g. the plan role's codingPlan).
  result: z.record(z.string(), z.unknown()).optional(),
  error: z.record(z.string(), z.unknown()).nullable().optional(),
  startedAt: z.string().optional(),
  updatedAt: z.string().optional(),
  durationMs: z.number().optional(),
});

export type SubagentActivityContent = z.infer<typeof SubagentActivitySchema>;

// -----------------------------------------------------------------------------
// CodingPlan mirrors the plan role's structured-output schema (main.go:
// codingPlan). The plan stage (Codex) is required to return this shape, and the
// frontend renders it as a downloadable attachment on the plan subagent card.
// -----------------------------------------------------------------------------
export const CodingPlanStepSchema = z.object({
  title: z.string(),
  detail: z.string().optional().default(""),
});

export const CodingPlanSchema = z.object({
  summary: z.string().optional().default(""),
  steps: z.array(CodingPlanStepSchema).min(1),
  acceptance_checks: z.array(z.string()).optional().default([]),
});

export type CodingPlan = z.infer<typeof CodingPlanSchema>;

// extractCodingPlan attempts to recover a validated CodingPlan from a subagent's
// content. Structured-output plan text arrives either as the accumulated
// assistant text or inside result.text (terminal delegation payload). It returns
// null for any non-plan card, so the caller renders the attachment only for the
// plan role.
export function extractCodingPlan(
  content: SubagentActivityContent,
): CodingPlan | null {
  const candidates: unknown[] = [];
  const resultText = content.result?.["text"];
  if (typeof resultText === "string") candidates.push(resultText);
  if (typeof content.text === "string") candidates.push(content.text);

  for (const candidate of candidates) {
    const value = coerceJSON(candidate);
    if (value == null) continue;
    const parsed = CodingPlanSchema.safeParse(value);
    if (parsed.success) return parsed.data;
  }
  return null;
}

// coerceJSON tolerates plain JSON as well as a single ```json fenced block,
// which some providers emit even when asked for raw JSON.
function coerceJSON(raw: unknown): unknown {
  if (typeof raw !== "string") return null;
  let text = raw.trim();
  if (text.startsWith("```")) {
    text = text.replace(/^```(?:json)?/i, "").replace(/```$/, "").trim();
  }
  if (!text.startsWith("{")) return null;
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}
