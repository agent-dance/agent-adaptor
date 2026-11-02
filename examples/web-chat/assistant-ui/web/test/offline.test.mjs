import assert from "node:assert/strict";
import test, { before } from "node:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { fromAgUiMessages, toAgUiMessages } from "@assistant-ui/react-ag-ui";
import { root, sourceModules, wire, withoutSeq, assertActivityParity } from "./helpers.mjs";

let glue;
before(async () => { glue = await sourceModules(); });
test("frozen Go goldens pass the official client/schema/runtime checks", async () => {
  // Keep the Go-side golden fixture and its standalone offline entry point.
  await import(new URL("../../../../../bridges/agui/testdata/verify-client.mjs", import.meta.url));
});
test("actual history adapter seeds active user and yields converted run updates", async () => {
  const fixture = JSON.parse(readFileSync(join(root, "bridges/agui/aguisession/testdata/client-observe.json")));
  const events = fixture.records.flatMap((r) => r.events ?? []);
  const original = globalThis.fetch;
  globalThis.fetch = async (url, init) => {
    if (url.includes("/snapshot?")) return Response.json(fixture.snapshot);
    if (url.includes("/observe?")) { assert.equal(init.signal instanceof AbortSignal, true); assert.ok(url.endsWith(`after=${fixture.snapshot.cursor}`)); return new Response(wire(events), { headers: { "content-type": "text/event-stream" } }); }
    if (url.includes("/history?")) return Response.json(fixture.history);
    throw new Error(`unexpected URL ${url}`);
  };
  try {
    const seen = new Map(); let canonical;
    const history = glue.createHistory({ threadId: fixture.snapshot.activeRun.threadId }, (id, status) => seen.set(id, status), (m) => { canonical = m; }, assert.fail);
    const repo = await history.load();
    assert.equal(repo.unstable_resume, true);
    assert.equal(repo.messages.at(-1).message.role, "user");
    assert.equal(repo.messages.at(-1).message.id, "u1");
    const updates = [];
    for await (const update of history.resume({ abortSignal: new AbortController().signal })) updates.push(update);
    const content = updates.filter((u) => u.content).at(-1).content;
    assert.deepEqual(withoutSeq(content), fromAgUiMessages(fixture.history.slice(fixture.snapshot.messages.length).filter((m) => m.role !== "user"), { showThinking: true }).flatMap((m) => m.role === "assistant" ? m.content : []));
    assert.equal(updates.at(-1).status.type, "complete");
    assert.deepEqual(canonical, fixture.history);
    assert.ok(seen.size > 0);
    const ids = (ms) => ms.map((m) => [m.id, m.role]);
    assert.deepEqual(ids(toAgUiMessages(fromAgUiMessages(canonical))), ids(canonical));
    history.dispose();
  } finally { globalThis.fetch = original; }
});
test("thin renderer visibly fails tool 13 live and after history reload", () => {
  const fixtures = JSON.parse(readFileSync(join(root, "bridges/agui/aguisession/testdata/client-session.json")));
  const fixture = fixtures.find((f) => f.name === "tools-1");
  const event = fixture.events.filter((e) => e.type === "TOOL_CALL_RESULT").at(-1);
  const status = glue.toolOutcome(event); assert.deepEqual(status, { status: "failed", exitCode: 1 });
  const call = fromAgUiMessages(fixture.history).flatMap((m) => m.content).filter((p) => p.type === "tool-call").at(-1);
  assert.equal(call.isError, true);
  const render = (part, live) => renderToStaticMarkup(createElement(glue.Outcomes.Provider, { value: live }, createElement(glue.Shell, { ...part, status: { type: "complete" } })));
  assert.match(render({ ...call, isError: false }, new Map([[event.toolCallId, status]])), /Failed.*exit 1/);
  assert.match(render(call, new Map()), /data-status="failed"/);
});
test("structured HTTP errors keep status and stable code", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ error: { code: "agui.history_diverged", message: "edited history" } }, { status: 409 });
  try { await assert.rejects(glue.checkedFetch("/agent"), /HTTP 409.*agui.history_diverged/); }
  finally { globalThis.fetch = original; }
});

test("actual Go example trace: official client, strict schema, three checkpointed turns", async () => {
  const { spawnSync } = await import("node:child_process");
  const { mkdtempSync, rmSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const { HttpAgent } = await import("@ag-ui/client");
  const { validatingFetch, validateMessage, withoutHistoryErrors } = await import("./helpers.mjs");
  const dir = mkdtempSync(join(tmpdir(), "agui-example-trace-"));
  try {
    const file = join(dir, "trace.json");
    const go = spawnSync("go", ["test", "-count=1", "./examples/web-chat/assistant-ui", "-run", "^TestScriptedThreadSurvivesReconstruction$"], { cwd: root, env: { ...process.env, AGUI_EXAMPLE_TRACE: file }, encoding: "utf8" });
    assert.equal(go.status, 0, go.stdout + go.stderr);
    const traces = JSON.parse(readFileSync(file));
    const events = [];
    const client = new HttpAgent({ url: "http://unused.invalid", threadId: "scoped/thread" });
    for (const trace of traces) {
      events.length = 0;
      client.fetch = validatingFetch(events, async () => new Response(trace.wire, { headers: { "content-type": "text/event-stream" } }));
      client.addMessage({ id: `user-${trace.runId}`, role: "user", content: "continue" });
      await client.runAgent({ runId: trace.runId });
      assert.equal(events[0].threadId, "scoped/thread"); assert.equal(events[0].runId, trace.runId); assert.equal(events[0].protocolVersion, "1.0");
      const terminal = events.at(-1);
      assert.equal(terminal.type, "RUN_FINISHED"); assert.equal(terminal.threadId, "scoped/thread"); assert.equal(terminal.runId, trace.runId);
      assert.ok(terminal.metadata.agentAdaptor.sdkRunId); assert.ok(terminal.metadata.agentAdaptor.providerThreadId);
      assert.notEqual(terminal.metadata.agentAdaptor.sdkRunId, trace.runId); assert.notEqual(terminal.metadata.agentAdaptor.providerThreadId, "scoped/thread");
      assert.deepEqual(client.messages, withoutHistoryErrors(trace.history));
      assert.deepEqual(fromAgUiMessages(client.messages), fromAgUiMessages(withoutHistoryErrors(trace.history)));
      const part = assertActivityParity(client.messages, withoutHistoryErrors(trace.history), trace.runId === "1" ? events : undefined, fromAgUiMessages);
      const render = (p) => renderToStaticMarkup(createElement(glue.ProtocolData, p));
      const livePart = fromAgUiMessages(client.messages).flatMap((m) => m.content).find((p) => p.type === "data" && p.name === part.name);
      assert.equal(render(livePart), render(part));
      assert.match(render(part), /Command review.*completed/);
      assert.match(render(part), /Checked 13 commands; command 13 needs attention/);
      assert.ok(render(part).includes(`data-tool-call-id="${part.data.parentToolCallId}"`));
      for (const m of trace.history) assert.ok(validateMessage(m), JSON.stringify(validateMessage.errors));
      assert.deepEqual(toAgUiMessages(fromAgUiMessages(trace.history)).filter((m) => m.role !== "activity").map((m) => [m.id, m.role]), trace.history.filter((m) => m.role !== "activity").map((m) => [m.id, m.role]));
      for (const showThinking of [true, false]) {
        const required = (ms) => ms.filter((m) => m.role !== "reasoning" && m.role !== "activity").map((m) => [m.id, m.role]);
        assert.deepEqual(required(toAgUiMessages(fromAgUiMessages(trace.history, { showThinking }))), required(trace.history));
      }
    }
    assert.match(client.messages.filter((m) => m.role === "assistant").at(-1).content, /Turn 3.*checkpoint 2/);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test("idle history reload retains cancelled terminal banner", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async (url) => {
    if (url.includes("/snapshot?")) return Response.json({ messages: [], cursor: 8, activeRun: null });
    if (url.includes("/events?")) return Response.json([{ host_seq: 8, events: [{ type: "RUN_ERROR", code: "run.cancelled", message: "codex app-server exited with status -1" }] }]);
    throw new Error(`unexpected URL ${url}`);
  };
  try {
    let banner;
    const history = glue.createHistory({ threadId: "cancelled" }, assert.fail, assert.fail, (error) => { banner = error; });
    const repo = await history.load();
    assert.equal(repo.unstable_resume, undefined);
    assert.deepEqual(banner, { kind: "cancelled", message: "Run cancelled." });
    history.dispose();
  } finally { globalThis.fetch = original; }
});

test("resume turns recorded RUN_ERROR into an incomplete status", async () => {
  const fixture = JSON.parse(readFileSync(join(root, "bridges/agui/aguisession/testdata/client-observe.json")));
  const events = fixture.records.flatMap((r) => r.events ?? []);
  const previous = events.pop();
  events.push({ type: "RUN_ERROR", code: "run.cancelled", message: "codex app-server exited with status -1", metadata: { agentAdaptor: { ...previous.metadata.agentAdaptor, threadId: fixture.snapshot.activeRun.threadId, runId: fixture.snapshot.activeRun.runId } } });
  const original = globalThis.fetch;
  globalThis.fetch = async (url) => {
    if (url.includes("/snapshot?")) return Response.json(fixture.snapshot);
    if (url.includes("/observe?")) return new Response(wire(events), { headers: { "content-type": "text/event-stream" } });
    if (url.includes("/history?")) return Response.json(fixture.history);
    throw new Error(`unexpected URL ${url}`);
  };
  try {
    let banner;
    const history = glue.createHistory({ threadId: fixture.snapshot.activeRun.threadId }, () => {}, () => {}, (error) => { banner = error; });
    await history.load();
    let last;
    for await (const update of history.resume({ abortSignal: new AbortController().signal })) last = update;
    assert.equal(last.status.type, "incomplete"); assert.equal(last.status.reason, "cancelled");
    assert.deepEqual(banner, { kind: "cancelled", message: "Run cancelled." }); history.dispose();
  } finally { globalThis.fetch = original; }
});

test("observation EOF without a terminal never becomes a completed run", async () => {
  const fixture = JSON.parse(readFileSync(join(root, "bridges/agui/aguisession/testdata/client-observe.json")));
  const events = fixture.records.flatMap((r) => r.events ?? []).slice(0, 3);
  const original = globalThis.fetch;
  globalThis.fetch = async (url) => {
    if (url.includes("/snapshot?")) return Response.json(fixture.snapshot);
    if (url.includes("/observe?")) return new Response(wire(events), { headers: { "content-type": "text/event-stream" } });
    throw new Error(`unexpected URL ${url}`);
  };
  try {
    let banner;
    const history = glue.createHistory({ threadId: fixture.snapshot.activeRun.threadId }, () => {}, assert.fail, (error) => { banner = error; });
    await history.load();
    const updates = [];
    await assert.rejects(async () => {
      for await (const update of history.resume({ abortSignal: new AbortController().signal })) updates.push(update);
    });
    assert.ok(!updates.some((u) => u.status?.type === "complete"));
    assert.equal(updates.at(-1).status.type, "incomplete");
    assert.equal(banner.kind, "error"); assert.match(banner.message, /terminal/); history.dispose();
  } finally { globalThis.fetch = original; }
});

test("sanitized real Codex history round-trips required identities with thinking on or off", () => {
  const fixture = JSON.parse(readFileSync(new URL("../testdata/codex-history.json", import.meta.url)));
  assert.equal(fixture.length, 10);
  assert.ok(fixture.some((m) => m.role === "reasoning" && m.content === ""));
  assert.doesNotMatch(JSON.stringify(fixture), /\/mnt\/|\/home\/|\/tmp\/|[A-Za-z]:[\\/]/);
  const requiredIds = (messages) => messages.filter((m) => m.role !== "reasoning" && m.role !== "activity").map((m) => [m.id, m.role]);
  const withVisibleReasoning = [...fixture.slice(0, 1), { id: "visible-reasoning", role: "reasoning", content: "Optional visible reasoning." }, ...fixture.slice(1)];
  for (const history of [fixture, withVisibleReasoning]) {
    for (const showThinking of [true, false]) {
      const converted = fromAgUiMessages(history, { showThinking });
      const roundTrip = toAgUiMessages(converted);
      assert.deepEqual(requiredIds(roundTrip), requiredIds(history));
      assert.ok(!roundTrip.some((m) => m.role === "reasoning" && m.content === ""));
      assert.equal(roundTrip.some((m) => m.id === "visible-reasoning"), showThinking && history === withVisibleReasoning);
      assert.equal(converted.flatMap((m) => m.content).filter((p) => p.type === "tool-call" && p.isError).length, 1);
    }
  }
});

test("unknown CUSTOM data does not become a top-level UI block", () => {
  for (const name of ["invocation", "runtime", "transcript.item", "codex.event", "claude.event", "unknown.card"]) {
    const html = renderToStaticMarkup(createElement(glue.ProtocolData, { name, data: { text: "private-local-data" } }));
    assert.equal(html, "");
  }
  const warning = renderToStaticMarkup(createElement(glue.ProtocolData, { name: "agentAdaptor.input.warning", data: { message: "Some content was skipped" } }));
  assert.match(warning, /role="status"/); assert.match(warning, /Some content was skipped/);
});

test("health mode controls fake and real example copy", () => {
  const fake = glue.modeCopy({ mode: "fake", agent: "fake", unattended: false, observation: true });
  assert.match(fake.intro, /13 commands/); assert.match(fake.footer, /simulated/);
  const real = glue.modeCopy({ mode: "real", agent: "codex", unattended: true, observation: true });
  assert.match(real.intro, /codex CLI/); assert.match(real.footer, /operator profile/);
  assert.doesNotMatch(JSON.stringify(real), /fake|scripted|simulated|13 commands/);
  assert.doesNotMatch(JSON.stringify(glue.modeCopy(undefined)), /fake|scripted|simulated/);
});

test("tool-only assistants retain their declared parent without empty text events", async () => {
  const { HttpAgent } = await import("@ag-ui/client");
  const { validatingFetch } = await import("./helpers.mjs");
  const events = [
    { type: "RUN_STARTED", threadId: "t", runId: "r", protocolVersion: "1.0" },
    { type: "TOOL_CALL_START", toolCallId: "call", toolCallName: "shell", parentMessageId: "tool-only" },
    { type: "TOOL_CALL_ARGS", toolCallId: "call", delta: "{}" },
    { type: "TOOL_CALL_END", toolCallId: "call" },
    { type: "TOOL_CALL_RESULT", messageId: "result", toolCallId: "call", content: "ok", role: "tool" },
    { type: "RUN_FINISHED", threadId: "t", runId: "r" },
  ];
  const received = [];
  const client = new HttpAgent({ url: "http://unused.invalid", threadId: "t", fetch: validatingFetch(received, async () => new Response(wire(events), { headers: { "content-type": "text/event-stream" } })) });
  await client.runAgent({ runId: "r" });
  assert.equal(received.length, 6);
  assert.ok(!received.some((e) => e.type.startsWith("TEXT_MESSAGE_")));
  const identities = (messages) => messages.map((m) => [m.id, m.role]);
  assert.deepEqual(identities(client.messages), [["tool-only", "assistant"], ["result", "tool"]]);
  assert.deepEqual(identities(toAgUiMessages(fromAgUiMessages(client.messages))), identities(client.messages));
});

test("cancelled runs render a status while genuine driver failures remain errors", () => {
  const detail = "codex app-server exited with status -1";
  const cancelled = glue.runNotice({ code: "run.cancelled", message: detail });
  const html = renderToStaticMarkup(createElement(glue.RunNoticeView, { notice: cancelled }));
  assert.match(html, /role="status"/); assert.match(html, /Run cancelled/);
  assert.doesNotMatch(html, /role="alert"|class="error"|codex app-server|status -1/);
  const failed = glue.runNotice({ code: "agent_error", message: detail });
  const errorHTML = renderToStaticMarkup(createElement(glue.RunNoticeView, { notice: failed }));
  assert.match(errorHTML, /role="alert"/); assert.match(errorHTML, /agent_error/); assert.match(errorHTML, /status -1/);
});
test("live glue drops only user-role echoes and maps guidance receipts", async () => {
  const frames = [
    { type: "RUN_STARTED", threadId: "t", runId: "r" },
    { type: "TEXT_MESSAGE_START", messageId: "a", role: "assistant" },
    { type: "TEXT_MESSAGE_START", messageId: "guide", role: "user" },
    { type: "TEXT_MESSAGE_CONTENT", messageId: "guide", delta: "more" },
    { type: "TEXT_MESSAGE_CONTENT", messageId: "a", delta: "answer" },
    { type: "TEXT_MESSAGE_END", messageId: "guide" },
    { type: "TEXT_MESSAGE_END", messageId: "a" },
    { type: "RUN_FINISHED", threadId: "t", runId: "r" },
  ];
  const body = `: keep-alive\n\n${wire(frames)}`;
  // Split mid-frame to prove the filter buffers across chunks.
  const chunks = [body.slice(0, 37), body.slice(37, 140), body.slice(140)];
  const upstream = async () => new Response(new ReadableStream({ start(c) { for (const chunk of chunks) c.enqueue(new TextEncoder().encode(chunk)); c.close(); } }), { headers: { "content-type": "text/event-stream" } });
  const text = await (await glue.withoutUserEchoes(upstream)("/agent")).text();
  const kept = text.split("\n\n").filter((f) => f.startsWith("data:")).map((f) => JSON.parse(f.slice(5)));
  assert.deepEqual(kept.map((e) => `${e.type}:${e.messageId ?? ""}`), ["RUN_STARTED:", "TEXT_MESSAGE_START:a", "TEXT_MESSAGE_CONTENT:a", "TEXT_MESSAGE_END:a", "RUN_FINISHED:"]);
  assert.ok(text.startsWith(": keep-alive\n\n"));
  const json = new Response("{}", { headers: { "content-type": "application/json" } });
  assert.equal(await glue.withoutUserEchoes(async () => json)("/agent"), json);
  assert.deepEqual(glue.messageUpdate({ name: "message.update", value: { id: "g", state: 2 } }), { id: "g", state: "delivered" });
  assert.deepEqual(glue.messageUpdate({ name: "message.update", value: { id: "g", state: 3, reason: "limit_exceeded" } }), { id: "g", state: "failed", detail: "Not delivered: limit_exceeded" });
  assert.equal(glue.messageUpdate({ name: "turn.end", value: {} }), undefined);
});
test("guidance POST maps acceptance and inactive runs", async () => {
  const original = globalThis.fetch;
  try {
    globalThis.fetch = async (_url, init) => {
      const { messageId } = JSON.parse(init.body);
      return messageId === "ok"
        ? Response.json({ threadId: "t", runId: "r", messageId, state: "accepted" }, { status: 202 })
        : Response.json({ error: { code: "agui.run_inactive", message: "no run" } }, { status: 409 });
    };
    assert.deepEqual(await glue.sendGuidance("t", "ok", "more"), { id: "ok", text: "more", state: "accepted" });
    const inactive = await glue.sendGuidance("t", "late", "more");
    assert.equal(inactive.state, "failed"); assert.match(inactive.detail, /run has finished/);
  } finally { globalThis.fetch = original; }
});
