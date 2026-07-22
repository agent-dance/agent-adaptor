import type { ReactNode } from "react";
import "@copilotkit/react-ui/styles.css";
import "@copilotkit/react-core/v2/styles.css";
import "./globals.css";
import { AppCopilotKitProvider } from "./components/copilotkit-provider";

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
        <AppCopilotKitProvider runtimeUrl="/api/copilotkit" agent="codex">
          {children}
        </AppCopilotKitProvider>
      </body>
    </html>
  );
}
