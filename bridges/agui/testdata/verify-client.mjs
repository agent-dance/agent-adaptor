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
