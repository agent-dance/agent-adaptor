import type { ReactNode } from "react";
import { CopilotKit } from "@copilotkit/react-core";
import "@copilotkit/react-ui/styles.css";
import "./globals.css";

const teamWorkflowMode =
  process.env.NEXT_PUBLIC_COPILOTKIT_MODE === "team-agent-workflow";

export const metadata = {
  title: teamWorkflowMode
    ? "agent-adaptor × CopilotKit × Team workflow"
    : "agent-adaptor × CopilotKit",
  description: teamWorkflowMode
    ? "Leader, plan, implementation, and review over AG-UI."
    : "Streaming chat demo over AG-UI (local CLI backend).",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body
        style={{
          margin: 0,
          fontFamily:
            "system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif",
          background: "#f4f5f7",
          minHeight: "100vh",
        }}
      >
        <CopilotKit runtimeUrl="/api/copilotkit" agent="codex">
          {children}
        </CopilotKit>
      </body>
    </html>
  );
}
