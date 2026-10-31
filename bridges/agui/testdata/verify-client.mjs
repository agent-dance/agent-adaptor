// Run from any directory with: node bridges/agui/testdata/verify-client.mjs
// Uses existing, read-only node_modules; no server, network, or install required.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const modules = process.env.AGUI_NODE_MODULES
  ?? resolve(root, "examples/web-chat/assistant-ui/web/node_modules");
assert.equal(JSON.parse(readFileSync(resolve(modules, "@ag-ui/client/package.json"))).version, "1.0.3");
const load = (path) => import(pathToFileURL(resolve(modules, path)));
const { verifyEvents, HttpAgent } = await load("@ag-ui/client/dist/index.mjs");
const { from } = await load("rxjs/dist/cjs/index.js");
const { toArray } = await load("rxjs/dist/cjs/operators/index.js");
const { default: Ajv } = await load("ajv/dist/2020.js");
const schema = JSON.parse(readFileSync(new URL("./ag-ui-schema-1.0.json", import.meta.url)));
const wire = readFileSync(new URL("./handler-agui-1.0.sse", import.meta.url), "utf8");

// Ajv needs the standard $anchor annotation registered for strict compilation.
// strictRequired is a schema lint: inherited properties in allOf are valid.
// All actual schema constraints, including unevaluatedProperties, remain active.
const ajv = new Ajv({ strict: true, strictRequired: false });
ajv.addKeyword({ keyword: "$anchor", schemaType: "string" });
const validate = ajv.compile(schema);
const events = wire.split("\n")
  .filter((line) => line.startsWith("data: "))
  .map((line) => JSON.parse(line.slice(6)));
assert.equal(events.length, 62);
for (const event of events) {
  assert.ok(validate(event), JSON.stringify(validate.errors));
}
await new Promise((complete, error) => {
  from(events).pipe(verifyEvents(), toArray()).subscribe({ error, complete });
});
const start = events[0];
const finish = events.at(-1);
assert.equal(start.type, "RUN_STARTED");
assert.equal(start.protocolVersion, "1.0");
assert.equal(start.parentRunId, "public-parent");
assert.equal(start.runId, "public-run");
assert.equal(start.threadId, "public-thread");
assert.equal(finish.type, "RUN_FINISHED");
assert.equal(finish.runId, start.runId);
assert.equal(finish.threadId, start.threadId);

const agent = new HttpAgent({
  url: "http://unused.invalid",
  threadId: start.threadId,
  fetch: async () => new Response(wire, {
    headers: { "content-type": "text/event-stream" },
  }),
});
agent.addMessage({ id: "user-1", role: "user", content: "run commands" });
let failed;
await agent.runAgent({ runId: start.runId }, {
  onToolCallResultEvent: ({ event }) => {
    if (event.toolCallId === "tool-13") failed = event.metadata?.agentAdaptor;
  },
});
assert.equal(failed?.status, "failed");
assert.equal(failed.exitCode, 1);
const tool = agent.messages.find((m) => m.role === "tool" && m.toolCallId === "tool-13");
assert.ok(tool?.content.includes("[exit=1]"));
assert.equal(agent.messages.filter((m) => m.role === "tool").length, 13);
assert.equal(agent.messages.length, 15);
console.log("PASS: 62 schema/client-verified frames; public identity/version; 13 tools; failed exit=1");

// The literal table is hand-derived from defaultApplyEvents and core
// mergeMetadata. Exercise that reducer directly: defensive collision/invalid
// patch cases intentionally bypass the separate schema/sequence verifier.
const { defaultApplyEvents } = await load("@ag-ui/client/dist/index.mjs");
const foldCases = JSON.parse(readFileSync(new URL("../aguisession/testdata/client-fold.json", import.meta.url)));
const validateMessage = ajv.compile({ ...schema, $ref: "#/$defs/Message", $id: "fold-message" });
for (const fixture of foldCases) {
  const updates = await new Promise((resolve, reject) => {
    defaultApplyEvents({ state: {} }, from(fixture.events), { messages: fixture.initial }, [])
      .pipe(toArray()).subscribe({ next: resolve, error: reject });
  });
  const actual = updates.filter((u) => u.messages !== undefined).at(-1)?.messages ?? fixture.initial;
  assert.deepEqual(actual, fixture.expected, fixture.name);
  for (const message of actual) assert.ok(validateMessage(message), `${fixture.name}: ${JSON.stringify(validateMessage.errors)}`);
}
console.log(`PASS: ${foldCases.length} independent Go/JS reducer expectation cases`);

const { fromAgUiMessages } = await load("@assistant-ui/react-ag-ui/dist/index.js");
assert.equal(JSON.parse(readFileSync(resolve(modules, "@assistant-ui/react-ag-ui/package.json"))).version, "0.1.1");
const sessions = JSON.parse(readFileSync(new URL("../aguisession/testdata/client-session.json", import.meta.url)));
const clients = new Map();
const idsRoles = (messages) => messages.filter((m) => m.role !== "activity").map((m) => [m.id, m.role]);
const withoutHistoryErrors = (messages) => messages.map((message) => {
  const copy = structuredClone(message);
  if (copy.role === "tool") delete copy.error;
  return copy;
});
for (const fixture of sessions) {
  const mode = fixture.name.replace(/-\d+$/, "");
  let client = clients.get(mode);
  const wire = fixture.events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join("");
  for (const event of fixture.events) assert.ok(validate(event), `${fixture.name}: ${JSON.stringify(validate.errors)}`);
  await new Promise((complete, error) => {
    from(fixture.events).pipe(verifyEvents(), toArray()).subscribe({ error, complete });
  });
  const fetch = async (_url, request) => {
    const sent = JSON.parse(request.body);
    // This is HttpAgent.prepareRunAgentInput's real egress, including reasoning,
    // tool-only assistants and results; it strips only activities.
    assert.deepEqual(idsRoles(sent.messages), idsRoles(fixture.input), `${fixture.name}: next-turn verifier input`);
    assert.ok(!sent.messages.some((m) => m.role === "activity"));
    return new Response(wire, { headers: { "content-type": "text/event-stream" } });
  };
  if (!client) {
    client = new HttpAgent({ url: "http://unused.invalid", threadId: "t", initialMessages: fixture.input, fetch });
    clients.set(mode, client);
  } else {
    client.fetch = fetch;
    client.addMessage(fixture.input.at(-1));
  }
  await client.runAgent({ runId: fixture.events[0].runId });
  assert.deepEqual(client.messages, withoutHistoryErrors(fixture.history), `${fixture.name}: live/history Message[]`);
  for (const message of fixture.history) assert.ok(validateMessage(message), JSON.stringify(validateMessage.errors));
  if (mode === "tools") {
    const converted = fromAgUiMessages(fixture.history, { showThinking: true });
    const calls = converted.flatMap((m) => m.content).filter((p) => p.type === "tool-call");
    assert.equal(calls.length, fixture.name.endsWith("-1") ? 13 : 26);
    assert.equal(calls.filter((p) => p.isError).length, fixture.name.endsWith("-1") ? 1 : 2);
    assert.ok(converted.some((m) => Array.isArray(m.content) && m.content.some((p) => p.type === "reasoning")));
  }
}
console.log(`PASS: ${sessions.length} recorded-session turns: real HttpAgent live/history, next-turn input and assistant-ui failed tools`);
