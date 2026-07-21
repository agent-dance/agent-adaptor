import type { ReactNode } from "react";
import { CopilotKit } from "@copilotkit/react-core";
import "@copilotkit/react-ui/styles.css";
import "./globals.css";

export const metadata = {
  title: "agent-adaptor × CopilotKit",
  description: "Streaming chat demo over AG-UI (local CLI backend).",
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
        <CopilotKit
          runtimeUrl="/api/copilotkit"
          agent="codex"
          enableInspector={false}
          showDevConsole={false}
        >
          {children}
        </CopilotKit>
      </body>
    </html>
  );
}
