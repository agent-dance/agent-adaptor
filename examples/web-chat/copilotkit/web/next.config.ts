import type { NextConfig } from "next";

const config: NextConfig = {
  reactStrictMode: true,
  // Next 16 otherwise writes AGENTS.md / CLAUDE.md into this example every
  // time the shared dev server starts, making a verification run dirty.
  agentRules: false,
  // The Go backend exposes a single AG-UI endpoint at :8080/agent. You can
  // override it via AGENT_BACKEND_URL when deploying somewhere else.
  env: {
    AGENT_BACKEND_URL: process.env.AGENT_BACKEND_URL ?? "http://localhost:8080/agent",
  },
  turbopack: {
    root: process.cwd(),
  },
};

export default config;
