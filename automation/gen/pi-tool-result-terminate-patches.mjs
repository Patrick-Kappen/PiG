// pig additive (D652): tool_result handlers override termination in PiG's private Node Session and runner.
// Run through vendor-pi-dist.sh before compiling bundles. Exact matches fail closed when the pinned Pi changes.
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

export const toolResultTerminatePatches = {
  "pi-coding-agent/core/agent-session.js": [
    ["                usage: result.usage,\n", "                usage: result.usage,\n                // pig additive (D652): expose the tool's flag to the handler chain.\n                terminate: result.terminate,\n"],
    ["            usage: hookResult?.usage,\n", "            usage: hookResult?.usage,\n            // pig additive (D652): omission keeps the tool's own flag, including during image normalization.\n            terminate: hookResult?.terminate ?? result.terminate,\n"],
  ],
  "pi-coding-agent/core/extensions/runner.js": [
    ["                    if (handlerResult.usage !== undefined) {", "                    // pig additive (D652): later handlers win; false overrides true.\n                    if (handlerResult.terminate !== undefined) {\n                        currentEvent.terminate = handlerResult.terminate;\n                        modified = true;\n                    }\n                    if (handlerResult.usage !== undefined) {"],
    ["            usage: currentEvent.usage,\n", "            usage: currentEvent.usage,\n            // pig additive (D652): return the chained termination flag.\n            terminate: currentEvent.terminate,\n"],
  ],
};

export function patchToolResultTerminate(file, source) {
  for (const [before, after] of toolResultTerminatePatches[file] ?? []) {
    if (source.split(before).length !== 2) throw new Error(`Pi tool_result termination seam changed in ${file}: ${before}`);
    source = source.replace(before, () => after);
  }
  return source;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  for (const file of Object.keys(toolResultTerminatePatches)) {
    const path = join(process.argv[2], file);
    writeFileSync(path, patchToolResultTerminate(file, readFileSync(path, "utf8")));
  }
}
