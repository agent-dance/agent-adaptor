export type SubagentToolCall = {
  id: string;
  name: string;
  status: string;
  args?: Record<string, unknown>;
  result?: unknown;
};

export type SubagentActivity = {
  subagentId: string;
  runId?: string;
  parentToolCallId?: string;
  agentKey: string;
  agentName?: string;
  kind?: string;
  protocol?: string;
  status: string;
  description?: string;
  text?: string;
  reasoning?: string;
  toolCalls: SubagentToolCall[];
  result?: unknown;
  error?: unknown;
  startedAt?: string;
  updatedAt?: string;
  durationMs?: number;
};

export type PlanFileArtifact = {
  filename: string;
  mediaType: string;
  summary: string;
  content: string;
};

export function extractPlanFileArtifact(activity: SubagentActivity): PlanFileArtifact | null {
  const candidates: unknown[] = [activity.text];
  if (isRecord(activity.result)) {
    candidates.unshift(activity.result.text, activity.result.output);
  }
  for (const candidate of candidates) {
    const parsed = parseJSONRecord(candidate);
    if (!parsed) continue;
    if (typeof parsed.content !== "string" || parsed.content.trim().length === 0) continue;
    return {
      filename: optionalString(parsed.filename) ?? "PLAN.md",
      mediaType: optionalString(parsed.media_type) ?? "text/markdown",
      summary: optionalString(parsed.summary) ?? "Implementation plan",
      content: parsed.content,
    };
  }
  return null;
}

export function parseSubagentActivity(value: unknown): SubagentActivity | null {
  if (!isRecord(value)) return null;
  if (typeof value.subagentId !== "string" || typeof value.agentKey !== "string") {
    return null;
  }
  const toolCalls = Array.isArray(value.toolCalls)
    ? value.toolCalls.map(parseToolCall).filter((call): call is SubagentToolCall => call !== null)
    : [];
  return {
    subagentId: value.subagentId,
    runId: optionalString(value.runId),
    parentToolCallId: optionalString(value.parentToolCallId),
    agentKey: value.agentKey,
    agentName: optionalString(value.agentName),
    kind: optionalString(value.kind),
    protocol: optionalString(value.protocol),
    status: optionalString(value.status) ?? "started",
    description: optionalString(value.description),
    text: optionalString(value.text),
    reasoning: optionalString(value.reasoning),
    toolCalls,
    result: value.result,
    error: value.error,
    startedAt: optionalString(value.startedAt),
    updatedAt: optionalString(value.updatedAt),
    durationMs: typeof value.durationMs === "number" ? value.durationMs : undefined,
  };
}

function parseToolCall(value: unknown): SubagentToolCall | null {
  if (!isRecord(value) || typeof value.id !== "string") return null;
  return {
    id: value.id,
    name: optionalString(value.name) ?? "tool",
    status: optionalString(value.status) ?? "running",
    args: isRecord(value.args) ? value.args : undefined,
    result: value.result,
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function parseJSONRecord(value: unknown): Record<string, unknown> | null {
  if (isRecord(value)) return value;
  if (typeof value !== "string") return null;
  try {
    const parsed: unknown = JSON.parse(value.trim());
    return isRecord(parsed) ? parsed : null;
  } catch {
    return null;
  }
}
