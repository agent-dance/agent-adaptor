import { HttpAgent } from "@ag-ui/client";
import {
  CopilotRuntime,
  ExperimentalEmptyAdapter,
  copilotRuntimeNextJSAppRouterEndpoint,
} from "@copilotkit/runtime";
import type { NextRequest } from "next/server";

// The agent-adaptor Go backend exposes a single AG-UI endpoint at /agent. We
// Register the backend under the stable frontend name "codex". The Go
// backend still chooses the real local CLI via AGUI_AGENT.
const runtime = new CopilotRuntime({
  agents: {
    codex: new HttpAgent({
      url: process.env.AGENT_BACKEND_URL ?? "http://localhost:8080/agent",
    }),
  },
});

const serviceAdapter = new ExperimentalEmptyAdapter();

export async function POST(req: NextRequest) {
  const { handleRequest } = copilotRuntimeNextJSAppRouterEndpoint({
    runtime,
    serviceAdapter,
    endpoint: "/api/copilotkit",
  });
  return handleRequest(req);
}
