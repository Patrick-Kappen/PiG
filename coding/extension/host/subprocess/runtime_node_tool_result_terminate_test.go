package subprocess_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// PiG's additive override must also survive Pi's independent Node Session, not just the Go host's event bridge.
func TestNodeToolResultTerminateChainsAndReachesSession(t *testing.T) {
	root, err := filepath.Abs("runtime-node/shims/pi-dist/pi-coding-agent")
	if err != nil {
		t.Fatal(err)
	}
	script := `
import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";
const root = pathToFileURL(process.argv[1] + "/");
for (const entry of ["index.js", "sdk-bundle/index.js"]) {
 const { AgentSession, ExtensionRunner } = await import(new URL(entry, root));
 for (const initial of [false, true]) {
  for (const last of [false, true]) {
   const runner = Object.create(ExtensionRunner.prototype);
   runner.createContext = () => ({});
   runner.extensions = [{ path: "terminate", handlers: new Map([["tool_result", [
    async (event) => { assert.equal(event.terminate, initial); return { terminate: !initial }; },
    async (event) => { assert.equal(event.terminate, !initial); return { details: "keep flag" }; },
    async (event) => { assert.equal(event.terminate, !initial); return { terminate: last }; },
    async (event) => { assert.equal(event.terminate, last); },
   ]]]) }];
   runner.emitError = (error) => { throw new Error(JSON.stringify(error)); };
   const session = Object.create(AgentSession.prototype);
   session._extensionRunner = runner;
   session._limitsModel = () => undefined;
   session.settingsManager = { getImageAutoResize: () => false };
   const result = await session._afterToolCall({
    toolCall: { id: "call", name: "custom" }, args: {},
    result: { content: [{ type: "text", text: "retained" }], terminate: initial }, isError: false,
   });
   assert.equal(result.terminate, last, entry);
   assert.equal(result.content[0].text, "retained");
  }
 }
}
`
	command := exec.CommandContext(t.Context(), "node", "--input-type=module", "--eval", script, root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}
