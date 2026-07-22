"use client";

import { CopilotKit } from "@copilotkit/react-core";
import { HttpAgent } from "@ag-ui/client";
import type { ReactNode } from "react";
import { SubagentActivitySchema } from "../lib/subagent-schema";
import { SubagentCard } from "./subagent-card";

// Stable module-level definition so the array identity never changes
// across renders — satisfies CopilotKit's "must be a stable array" invariant.
const SUBAGENT_RENDERER = {
  activityType: "subagent",
  content: SubagentActivitySchema,
  render: SubagentCard,
} as const;

const ACTIVITY_RENDERERS = [SUBAGENT_RENDERER];
const DIRECT_AGENTS = {
  codex: new HttpAgent({
    url:
      process.env.NEXT_PUBLIC_AGENT_BACKEND_URL ??
      "http://localhost:8080/agent",
  }),
};

interface AppCopilotKitProviderProps {
  children: ReactNode;
  runtimeUrl: string;
  agent: string;
}

/**
 * Client-boundary wrapper around <CopilotKit>.
 *
 * Registers the `activityType="subagent"` renderer so that
 * ACTIVITY_SNAPSHOT / ACTIVITY_DELTA events from the AG-UI backend
 * render as SubagentCard components inside CopilotChat.
 *
 * Existing useCopilotAction({ name: "*" }) for HITL decision cards and
 * generic tool-call cards continues to work unchanged — the activity
 * renderer only intercepts ActivityMessage frames, not ToolCallMessage frames.
 */
export function AppCopilotKitProvider({
  children,
  runtimeUrl,
  agent,
}: AppCopilotKitProviderProps) {
  return (
    <CopilotKit
      runtimeUrl={runtimeUrl}
      agent={agent}
      selfManagedAgents={DIRECT_AGENTS}
      enableInspector={false}
      showDevConsole={false}
      renderActivityMessages={ACTIVITY_RENDERERS}
    >
      {children}
    </CopilotKit>
  );
}
