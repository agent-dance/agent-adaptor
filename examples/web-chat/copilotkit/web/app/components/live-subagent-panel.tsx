"use client";

import { useCopilotChatInternal } from "@copilotkit/react-core";
import { parseSubagentActivity } from "../lib/subagent";
import { SubagentCard } from "./subagent-card";

type ActivityMessage = {
  id: string;
  role: string;
  activityType?: string;
  content?: unknown;
};

// This panel reads the same CopilotKit AG-UI message store as CopilotChat. The
// public chat hook intentionally hides raw messages, so the example uses the
// package's exported internal hook to render protocol Activity messages.
export function LiveSubagentPanel() {
  const { messages: rawMessages } = useCopilotChatInternal();
  const messages = rawMessages as ActivityMessage[];
  const activities = messages.flatMap((message) => {
    if (message.role !== "activity" || message.activityType !== "subagent") {
      return [];
    }
    const content = parseSubagentActivity(message.content);
    return content ? [{ id: message.id, content }] : [];
  });

  return (
    <aside className="subagent-panel" aria-label="Subagent activity">
      <div className="subagent-panel-heading">
        <div>
          <h2>Subagents</h2>
          <p>Live delegated-role activity</p>
        </div>
        <span className="subagent-count">{activities.length}</span>
      </div>
      {activities.length === 0 ? (
        <div className="subagent-empty">
          <span aria-hidden="true">◇</span>
          Delegated roles will appear here when the leader starts the workflow.
        </div>
      ) : (
        <div className="subagent-list">
          {activities.map(({ id, content }) => (
            <SubagentCard key={id} content={content} />
          ))}
        </div>
      )}
    </aside>
  );
}
