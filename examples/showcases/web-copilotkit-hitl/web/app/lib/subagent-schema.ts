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
  error: z.record(z.string(), z.unknown()).nullable().optional(),
  startedAt: z.string().optional(),
  updatedAt: z.string().optional(),
  durationMs: z.number().optional(),
});

export type SubagentActivityContent = z.infer<typeof SubagentActivitySchema>;
