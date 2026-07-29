"use client";

import { useState } from "react";
import type { KeyboardEvent } from "react";
import type { InputProps } from "@copilotkit/react-ui";

export const TEAM_WORKFLOW_START_PROMPT =
  "Start the team workflow and watch the leader delegate plan, implementation, and review.";

export function TeamWorkflowInput({
  inProgress,
  onSend,
  onStop,
  isVisible = true,
  chatReady = false,
  hideStopButton = false,
}: InputProps) {
  const [text, setText] = useState(TEAM_WORKFLOW_START_PROMPT);

  if (!isVisible) return null;

  const send = async () => {
    const prompt = text.trim();
    if (!prompt || inProgress || !chatReady) return;
    await onSend(prompt);
    setText("");
  };

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    void send();
  };

  const canStop = inProgress && !hideStopButton && onStop !== undefined;
  return (
    <div className="team-workflow-input">
      <textarea
        aria-label="Team workflow prompt"
        value={text}
        onChange={(event) => setText(event.target.value)}
        onKeyDown={onKeyDown}
        rows={3}
      />
      <button
        type="button"
        onClick={canStop ? onStop : () => void send()}
        disabled={canStop ? false : !chatReady || inProgress || text.trim().length === 0}
        aria-label={canStop ? "Stop generating" : "Send"}
      >
        {canStop ? "Stop" : "Send"}
      </button>
    </div>
  );
}
