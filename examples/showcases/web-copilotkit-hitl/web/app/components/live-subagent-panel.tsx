"use client";

import type { ReactNode } from "react";
import { useAgent } from "@copilotkit/react-core/v2";
import { SubagentActivitySchema } from "../lib/subagent-schema";
import { SubagentCard } from "./subagent-card";

// -----------------------------------------------------------------------------
// LiveSubagentPanel renders the subagent cards LIVE, outside <CopilotChat>.
//
// Why not render them inside the chat via renderActivityMessages? CopilotChat
// memoizes its message list with a key that, for activity messages, only
// fingerprints object *presence* (contentKey = 0 for object content) — it does
// not fingerprint the streamed ACTIVITY_DELTA patches (text / toolCalls /
// status). So in-chat activity cards freeze at "Waiting for provider activity…"
// and only repaint when some *other* message changes the memo key (typically
// when the delegation tool-call finishes). See CopilotChat.tsx messagesMemoKey.
//
// useAgent() shares the same per-(agentId, threadId) message store as
// CopilotChat but force-updates on EVERY message change (throttleMs=0, no memo
// gate), so reading agent.messages here re-renders on each delta and the cards
// update in real time.
// -----------------------------------------------------------------------------

interface LiveSubagentPanelProps {
  threadId: string;
}

export function LiveSubagentPanel({ threadId }: LiveSubagentPanelProps) {
  const { agent } = useAgent({ agentId: "codex", threadId });

  const cards: ReactNode[] = [];
  for (const message of agent.messages) {
    if (message.role !== "activity" || message.activityType !== "subagent") {
      continue;
    }
    const parsed = SubagentActivitySchema.safeParse(message.content);
    if (!parsed.success) continue;
    cards.push(
      <SubagentCard
        key={message.id}
        activityType={message.activityType}
        content={parsed.data}
        message={message}
        agent={agent}
      />,
    );
  }

  return (
    <section
      className="agent-subagent-panel"
      style={{
        background: "#fff",
        border: "1px solid #e5e7eb",
        borderRadius: 8,
        padding: "0.75rem",
        minWidth: 0,
      }}
    >
      <h2 style={{ margin: "0 0 0.5rem", fontSize: "0.95rem" }}>Subagents</h2>
      {cards.length === 0 ? (
        <p style={{ margin: 0, color: "#6b7280", fontSize: "0.82rem" }}>
          Delegated roles will appear here as the agent runs.
        </p>
      ) : (
        cards
      )}
    </section>
  );
}
