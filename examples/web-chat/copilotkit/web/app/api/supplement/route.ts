// Supplements use a plain JSON endpoint: the main CopilotKit runtime expects
// an AG-UI event stream and cannot consume an admission-only response.
export async function POST(request: Request) {
  let input: unknown;
  try {
    input = await request.json();
  } catch {
    return Response.json({ error: "Enter a valid supplement." }, { status: 400 });
  }
  if (!input || typeof input !== "object") {
    return Response.json({ error: "Enter a valid supplement." }, { status: 400 });
  }
  const { threadId, messageId, text } = input as Record<string, unknown>;
  if (
    typeof threadId !== "string" || !threadId.trim() ||
    typeof messageId !== "string" || !messageId.trim() ||
    typeof text !== "string" || !text.trim()
  ) {
    return Response.json(
      { error: "A thread, message ID, and nonempty text are required." },
      { status: 400 },
    );
  }

  let endpoint: URL;
  try {
    endpoint = new URL(process.env.AGENT_BACKEND_URL ?? "http://localhost:8080/agent");
    if (endpoint.protocol !== "http:" && endpoint.protocol !== "https:") {
      throw new Error("Unsupported backend protocol");
    }
    // Preserve a deployment prefix, treating /prefix/agent and /prefix/agent/
    // as the same configured agent endpoint. Never derive the host from input.
    const agentPath = endpoint.pathname.replace(/\/+$/, "");
    endpoint.pathname = `${agentPath.slice(0, agentPath.lastIndexOf("/"))}/message`;
    endpoint.search = "";
    endpoint.hash = "";
  } catch {
    return Response.json({ error: "The agent backend URL is invalid." }, { status: 500 });
  }

  try {
    const response = await fetch(endpoint, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ threadId, messageId, text }),
      cache: "no-store",
      signal: request.signal,
    });
    return new Response(await response.text(), {
      status: response.status,
      headers: {
        "Content-Type": response.headers.get("Content-Type") ?? "application/json",
        "Cache-Control": "no-store",
      },
    });
  } catch {
    return Response.json(
      { error: "Could not confirm acceptance. Retrying will reuse the message ID." },
      { status: 502 },
    );
  }
}
