import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { join, resolve } from "node:path";
import ts from "typescript";
import Ajv from "ajv/dist/2020.js";

export const web = fileURLToPath(new URL("../", import.meta.url));
export const root = resolve(web, "../../../..");
const schema = JSON.parse(readFileSync(join(root, "bridges/agui/testdata/ag-ui-schema-1.0.json")));
// strictRequired only lints schema authoring across allOf; no instance constraint
// is relaxed. Register the standard annotation missing from this Ajv release.
const ajv = new Ajv({ strict: true, strictRequired: false });
ajv.addKeyword({ keyword: "$anchor", schemaType: "string" });
export const validate = ajv.compile(schema);
export const validateMessage = ajv.compile({ ...schema, $id: "example-message", $ref: "#/$defs/Message" });
export const wire = (events) => events.map((e) => `data: ${JSON.stringify(e)}\n\n`).join("");
export const withoutHistoryErrors = (messages) => messages.map((m) => { const c = structuredClone(m); if (c.role === "tool") delete c.error; return c; });
export function withoutSeq(value) {
  const copy = structuredClone(value);
  function visit(v) {
    if (!v || typeof v !== "object") return;
    if (v.messageMetadata) { const holder = { metadata: v.messageMetadata }; visit(holder); if (holder.metadata) v.messageMetadata = holder.metadata; else delete v.messageMetadata; }
    if (v.metadata?.agentAdaptor && Object.hasOwn(v.metadata.agentAdaptor, "seq")) {
      delete v.metadata.agentAdaptor.seq;
      if (!Object.keys(v.metadata.agentAdaptor).length) delete v.metadata.agentAdaptor;
      if (!Object.keys(v.metadata).length) delete v.metadata;
    }
    Object.values(v).forEach(visit);
  }
  visit(copy); return copy;
}
// Compile the actual browser glue/renderers with the already installed TS.
// Keep generated modules beside node_modules; remove them immediately on import.
export async function sourceModules() {
  const dir = mkdtempSync(join(web, ".test-"));
  try {
    for (const [name, source] of [["session", "session.ts"], ["App", "App.tsx"]]) {
      const result = ts.transpileModule(readFileSync(join(web, "src", source), "utf8"), {
        compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, jsx: ts.JsxEmit.ReactJSX },
      });
      writeFileSync(join(dir, `${name}.mjs`), result.outputText.replace('from "./session"', 'from "./session.mjs"'));
    }
    return { ...await import(pathToFileURL(join(dir, "session.mjs"))), ...await import(pathToFileURL(join(dir, "App.mjs"))) };
  } finally { rmSync(dir, { recursive: true, force: true }); }
}
// Validate the raw SSE frames before HttpAgent's compatibility layer can change
// them. The original byte stream continues to the official parser unchanged.
export function validatingFetch(events, fetchImpl = fetch) {
  return async (url, init) => {
    const response = await fetchImpl(url, init);
    if (!response.ok) return response;
    let buffer = "";
    const decoder = new TextDecoder();
    const consume = () => {
      let end;
      while ((end = buffer.indexOf("\n\n")) >= 0) {
        const frame = buffer.slice(0, end); buffer = buffer.slice(end + 2);
        const data = frame.split("\n").filter((s) => s.startsWith("data:")).map((s) => s.slice(5).trimStart()).join("\n");
        if (!data) continue;
        const event = JSON.parse(data);
        assert.ok(validate(event), JSON.stringify(validate.errors)); events.push(event);
      }
    };
    return new Response(response.body.pipeThrough(new TransformStream({
      transform(chunk, controller) { buffer += decoder.decode(chunk, { stream: true }); consume(); controller.enqueue(chunk); },
      flush() { buffer += decoder.decode(); consume(); assert.equal(buffer.trim(), "", "unterminated SSE frame"); },
    })), { status: response.status, headers: response.headers });
  };
}
