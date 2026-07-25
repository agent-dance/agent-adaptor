import type { ReactNode } from "react";
// NOTE: do NOT import "@copilotkit/react-ui/styles.css" (the legacy v1
// stylesheet). This app renders the v2 chat (<CopilotChat> from
// @copilotkit/react-core/v2). v1 and v2 share class names
// (.copilotKitMessage / .copilotKitUserMessage), but v1's rules are unlayered
// and therefore win the cascade over v2's @layer utilities — they force
// background: var(--copilot-kit-primary-color) (the dark box), max-width:80%
// and overflow-wrap:break-word onto the v2 wrapper, collapsing the user bubble
// so text wraps character-by-character ("he" / "llo"). v2 ships its own styles
// below, so the v1 sheet is both unused and harmful here.
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
