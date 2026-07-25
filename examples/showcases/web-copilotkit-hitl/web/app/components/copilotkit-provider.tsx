"use client";

import { CopilotKit } from "@copilotkit/react-core";
import { HttpAgent } from "@ag-ui/client";
import type { ReactNode } from "react";

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
 * Subagent activity (ACTIVITY_SNAPSHOT / ACTIVITY_DELTA) is rendered LIVE by
 * <LiveSubagentPanel> via useAgent(), NOT through CopilotChat's
 * renderActivityMessages: CopilotChat's message memo does not fingerprint
 * activity delta patches, so in-chat activity cards freeze until an unrelated
 * message changes the memo key. The panel reads the same per-thread agent store
 * and re-renders on every delta.
 *
 * useCopilotAction({ name: "*" }) for HITL decision cards and generic
 * tool-call cards continues to work unchanged — it handles ToolCallMessage
 * frames, not ActivityMessage frames.
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
    >
      {children}
    </CopilotKit>
  );
}
