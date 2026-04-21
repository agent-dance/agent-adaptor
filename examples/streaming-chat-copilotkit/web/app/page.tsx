"use client";

import { CopilotChat } from "@copilotkit/react-ui";

export default function HomePage() {
  return (
    <main
      style={{
        display: "grid",
        gridTemplateRows: "auto 1fr",
        maxWidth: 960,
        margin: "0 auto",
        padding: "2rem",
        minHeight: "100vh",
        gap: "1rem",
        boxSizing: "border-box",
      }}
    >
      <header>
        <h1 style={{ margin: 0, fontSize: "1.4rem" }}>
          agent-adaptor · CopilotKit · codex
        </h1>
        <p style={{ margin: "0.25rem 0 0", color: "#555", fontSize: "0.9rem" }}>
          AG-UI over SSE. Backend: <code>localhost:8080/agent</code>. Every
          message, tool-call, and token streams live from{" "}
          <code>codex app-server</code>.
        </p>
      </header>

      <section
        style={{
          background: "#fff",
          border: "1px solid #e5e7eb",
          borderRadius: "12px",
          padding: "0.5rem",
          minHeight: "60vh",
          display: "flex",
          flexDirection: "column",
        }}
      >
        <CopilotChat
          labels={{
            title: "codex",
            initial: "Ask anything. Text, tool calls, and reasoning all stream.",
            placeholder: "Type a message...",
          }}
        />
      </section>
    </main>
  );
}
