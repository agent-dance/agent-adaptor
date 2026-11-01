import assert from "node:assert/strict";
import test from "node:test";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { HttpAgent } from "@ag-ui/client";
import { fromAgUiMessages, toAgUiMessages } from "@assistant-ui/react-ag-ui";
import { assertActivityParity, root, validatingFetch, validate, validateMessage, withoutHistoryErrors, withoutSeq } from "./helpers.mjs";

async function freePort() {
  const server = createServer(); server.listen(0, "127.0.0.1"); await once(server, "listening");
  const port = server.address().port; await new Promise((resolve, reject) => server.close((e) => e ? reject(e) : resolve())); return port;
}
async function eventually(read, predicate, message) {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) { const value = await read(); if (predicate(value)) return value; await delay(30); }
  assert.fail(message);
}
async function stop(child) {
  if (child.exitCode !== null) return;
  const exited = once(child, "exit"); child.kill("SIGTERM");
  let timer;
  try { await Promise.race([exited, new Promise((_, reject) => { timer = setTimeout(() => { child.kill("SIGKILL"); reject(new Error("server shutdown timed out")); }, 12000); })]); }
  finally { clearTimeout(timer); }
  assert.equal(child.exitCode, 0, "server shutdown failed");
}
function assertIdentity(events, threadId, runId, success = true) {
  const start = events[0]; const finish = events.at(-1);
  assert.equal(start.type, "RUN_STARTED"); assert.equal(start.protocolVersion, "1.0");
  assert.equal(start.threadId, threadId); assert.equal(start.runId, runId);
  assert.equal(finish.type, success ? "RUN_FINISHED" : "RUN_ERROR");
  assert.equal(finish.threadId ?? finish.metadata?.agentAdaptor?.threadId, threadId);
  assert.equal(finish.runId ?? finish.metadata?.agentAdaptor?.runId, runId);
  let sawSDK = false, sawProvider = false;
  for (const event of events) {
    assert.ok(validate(event));
    const meta = event.metadata?.agentAdaptor;
    if (meta?.sdkRunId) { sawSDK = true; assert.notEqual(meta.sdkRunId, runId); assert.notEqual(meta.sdkRunId, threadId); }
    if (meta?.providerThreadId) { sawProvider = true; assert.notEqual(meta.providerThreadId, threadId); assert.notEqual(meta.providerThreadId, runId); }
    if (meta?.threadKey) assert.notEqual(meta.threadKey, threadId);
  }
  assert.ok(sawSDK, "SDK identity must be in audit metadata");
  if (success) assert.ok(sawProvider, "provider identity must be in audit metadata");
}

test("HTTP official client: turns, durable restart, validation, observation and cancellation", { skip: process.env.AGUI_TEST_NO_SOCKETS === "1", timeout: 120000 }, async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "agui-assistant-ui-"));
  let child;
  t.after(async () => { try { if (child) await stop(child); } finally { await rm(dir, { recursive: true, force: true }); } });
  // Build once, then restart the same executable against the same data dir.
  const binary = join(dir, "server");
  const built = spawnSync("go", ["build", "-o", binary, "./examples/web-chat/assistant-ui"], { cwd: root, encoding: "utf8" });
  assert.equal(built.status, 0, built.stderr || built.error?.message);
  let base;
  async function start(pace = "0s") {
    const port = await freePort(); base = `http://127.0.0.1:${port}`;
    child = spawn(binary, ["-addr", `127.0.0.1:${port}`, "-data-dir", join(dir, "data"), "-agent", "fake", "-unattended", "-observation", "-pace", pace], { cwd: root, stdio: ["ignore", "ignore", "pipe"] });
    let logs = ""; child.stderr.on("data", (d) => { logs += d; });
    await eventually(async () => { if (child.exitCode !== null) assert.fail(logs); try { return await fetch(`${base}/health`); } catch { return null; } }, (r) => r?.ok, "server did not start");
  }
  const json = async (path) => { const r = await fetch(`${base}${path}`); assert.equal(r.status, 200, await r.clone().text()); return r.json(); };
  const history = (thread) => json(`/history?thread_id=${encodeURIComponent(thread)}`);
  const snapshot = (thread) => json(`/snapshot?thread_id=${encodeURIComponent(thread)}`);
  async function reject(body, status, code) {
    const response = await fetch(`${base}/agent`, { method: "POST", headers: { "content-type": "application/json" }, body: typeof body === "string" ? body : JSON.stringify(body) });
    assert.equal(response.status, status); assert.doesNotMatch(response.headers.get("content-type"), /event-stream/);
    const text = await response.text(); assert.doesNotMatch(text, /RUN_STARTED/); if (code) assert.match(text, new RegExp(code.replaceAll(".", "\\.")));
  }
  await start();
  const events = []; const client = new HttpAgent({ url: `${base}/agent`, threadId: "public-thread", fetch: validatingFetch(events) });
  for (let turn = 1; turn <= 2; turn++) {
    events.length = 0; client.addMessage({ id: `user-${turn}`, role: "user", content: turn === 1 ? "check commands" : "what failed?" });
    await client.runAgent({ runId: `public-run-${turn}` });
    assertIdentity(events, "public-thread", `public-run-${turn}`);
    const stored = await history("public-thread");
    for (const m of stored) assert.ok(validateMessage(m), JSON.stringify(validateMessage.errors));
    // Intentional live/history differences: the observer transport cursor
    // (metadata.agentAdaptor.seq) exists only live, ToolMessage.error only in history.
    const live = withoutSeq(client.messages);
    assert.deepEqual(live, withoutHistoryErrors(stored));
    assert.deepEqual(fromAgUiMessages(live), fromAgUiMessages(withoutHistoryErrors(stored)));
    assertActivityParity(live, withoutHistoryErrors(stored), turn === 1 ? events : undefined, fromAgUiMessages);
    const liveCalls = fromAgUiMessages(live).flatMap((m) => m.content).filter((p) => p.type === "tool-call");
    const storedCalls = fromAgUiMessages(stored).flatMap((m) => m.content).filter((p) => p.type === "tool-call");
    assert.deepEqual(liveCalls.map(({ isError, ...p }) => p), storedCalls.map(({ isError, ...p }) => p));
    assert.equal(storedCalls.length, 13); assert.equal(storedCalls.at(-1).isError, true);
    if (turn === 1) {
      const results = events.filter((e) => e.type === "TOOL_CALL_RESULT"); assert.equal(results.length, 13);
      assert.equal(results[12].metadata.agentAdaptor.status, "failed"); assert.equal(results[12].metadata.agentAdaptor.exitCode, 1);
    } else assert.match(stored.filter((m) => m.role === "assistant").at(-1).content, /same Thread using checkpoint 1/);
  }
  const before = await history("public-thread");
  await stop(child); child = undefined; await start();
  assert.deepEqual(await history("public-thread"), before);
  assertActivityParity(withoutSeq(client.messages), withoutHistoryErrors(await history("public-thread")), undefined, fromAgUiMessages);
  const thirdEvents = [];
  const fresh = new HttpAgent({ url: `${base}/agent`, threadId: "public-thread", initialMessages: before, fetch: validatingFetch(thirdEvents) });
  fresh.addMessage({ id: "user-3", role: "user", content: "continue after restart" });
  await fresh.runAgent({ runId: "public-run-3" }); assertIdentity(thirdEvents, "public-thread", "public-run-3");
  assert.match(fresh.messages.filter((m) => m.role === "assistant").at(-1).content, /Turn 3.*same Thread using checkpoint 2/);
  const input = { protocolVersion: "1.0", threadId: "public-thread", runId: "invalid", messages: [...fresh.messages, { id: "u4", role: "user", content: "next" }] };
  await reject("{", 400);
  await reject({ ...input, state: null }, 400, "agui.invalid_input");
  const collision = structuredClone(input); collision.messages.at(-1).id = collision.messages[1].id;
  await reject(collision, 409, "agui.duplicate_message");
  await reject({ ...input, messages: [{ id: "result", role: "tool", toolCallId: "tc", content: "done" }] }, 400);
  const edited = structuredClone(input); edited.messages[0].id = "edited-branch";
  await reject(edited, 409, "agui.history_diverged");
  await reject({ ...input, runId: "public-run-3" }, 409, "agui.duplicate_run");
  // A reload must use assistant-ui's actual export, not raw stored Message[].
  // showThinking=false deliberately omits reasoning; user/assistant/tool IDs
  // still match. Each case uses a fresh HttpAgent as a refreshed page would.
  for (const showThinking of [true, false]) {
    const thread = `reload-thinking-${showThinking}`;
    const firstEvents = [];
    const first = new HttpAgent({ url: `${base}/agent`, threadId: thread, fetch: validatingFetch(firstEvents) });
    first.addMessage({ id: `${thread}-u1`, role: "user", content: "check commands" });
    await first.runAgent({ runId: `${thread}-run1` });
    assertIdentity(firstEvents, thread, `${thread}-run1`);
    const loaded = await history(thread);
    const roundTrip = toAgUiMessages(fromAgUiMessages(loaded, { showThinking }));
    const requiredIds = (messages) => messages.filter((m) => m.role !== "reasoning" && m.role !== "activity").map((m) => [m.id, m.role]);
    assert.deepEqual(requiredIds(roundTrip), requiredIds(loaded));
    assert.equal(roundTrip.some((m) => m.role === "reasoning"), showThinking);
    const reloadedEvents = [];
    const reloaded = new HttpAgent({ url: `${base}/agent`, threadId: thread, initialMessages: roundTrip, fetch: validatingFetch(reloadedEvents) });
    reloaded.addMessage({ id: `${thread}-u2`, role: "user", content: "continue after page reload" });
    await reloaded.runAgent({ runId: `${thread}-run2` });
    assertIdentity(reloadedEvents, thread, `${thread}-run2`);
    assert.match(reloaded.messages.filter((m) => m.role === "assistant").at(-1).content, /Turn 2.*same Thread using checkpoint 1/);
  }
  // The bridge deliberately compares ID/role order, not content edits retaining
  // IDs. Products assign new IDs to edits or explicitly branch to a fresh key.
  await stop(child); child = undefined; await start("50ms");
  const firstEvents = [];
  const first = new HttpAgent({ url: `${base}/agent`, threadId: "paced", fetch: validatingFetch(firstEvents) });
  first.addMessage({ id: "paced-user", role: "user", content: "paced commands" });
  let disconnect;
  const delivered = new Promise((r) => { disconnect = r; });
  const running = first.runAgent({ runId: "paced-run" }, { onTextMessageContentEvent() { first.abortRun(); disconnect(); } }).catch(() => {});
  await delivered; await running;
  const cut = await snapshot("paced"); assert.ok(cut.activeRun, "disconnect cancelled unattended run");
  assert.equal(cut.cursor + 1, cut.activeRun.boundaryHostSeq);
  assert.ok(!cut.messages.some((m) => m.id === "paced-user"));
  await reject({ protocolVersion: "1.0", threadId: "paced", runId: "busy", messages: [{ id: "busy-user", role: "user", content: "busy" }] }, 409, "agui.run_active");
  const suffix = [];
  const joined = new HttpAgent({ url: `${base}/observe`, threadId: "paced", initialMessages: cut.messages,
    fetch: validatingFetch(suffix, () => fetch(`${base}/observe?thread_id=paced&after=${cut.cursor}`)) });
  await joined.runAgent({ runId: cut.activeRun.runId }); assertIdentity(suffix, "paced", "paced-run");
  const final = await history("paced");
  assert.deepEqual(withoutSeq(joined.messages), withoutHistoryErrors(final));
  assert.deepEqual(fromAgUiMessages(withoutSeq(joined.messages)), fromAgUiMessages(withoutHistoryErrors(final)));
  const records = await json(`/events?thread_id=paced&after=${cut.cursor}`);
  const projected = records.flatMap((r) => (r.events ?? []).map((event) => ({ ...event, metadata: { ...event.metadata, agentAdaptor: { ...event.metadata?.agentAdaptor, seq: r.host_seq } } })));
  // Compare complete records, not unique seq per event: one HostSeq may project
  // several frames. Exact equality proves no duplicates or gaps in the suffix.
  assert.deepEqual(suffix, projected);
  let last = cut.cursor; for (const e of suffix) { assert.ok(e.metadata.agentAdaptor.seq >= last); last = e.metadata.agentAdaptor.seq; }
  assert.deepEqual(toAgUiMessages(fromAgUiMessages(final)).filter((m) => m.role !== "activity").map((m) => [m.id, m.role]), final.filter((m) => m.role !== "activity").map((m) => [m.id, m.role]));
  const cancelEvents = [];
  const cancelled = new HttpAgent({ url: `${base}/agent`, threadId: "cancelled", fetch: validatingFetch(cancelEvents) });
  cancelled.addMessage({ id: "cancel-user", role: "user", content: "cancel me" });
  const cancelRun = cancelled.runAgent({ runId: "cancel-run" });
  await eventually(() => snapshot("cancelled"), (s) => s.activeRun, "no active cancellable run");
  const cancelResponse = await fetch(`${base}/cancel?thread_id=cancelled`, { method: "POST" }); assert.equal(cancelResponse.status, 204);
  await cancelRun;
  assertIdentity(cancelEvents, "cancelled", "cancel-run", false);
  assert.equal(cancelEvents.at(-1).code, "run.cancelled");
  assert.equal((await snapshot("cancelled")).activeRun, null);
  const cancelHistory = await json("/events?thread_id=cancelled");
  const terminal = cancelHistory.flatMap((r) => r.events ?? []).at(-1);
  assert.equal(terminal.type, "RUN_ERROR"); assert.equal(terminal.code, "run.cancelled");
});
