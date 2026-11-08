import { copyFileSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dir, "..");
const output = mkdtempSync(join(tmpdir(), "persona-sdk-portable-"));
function run(command: string[], timeout = 30000) {
  const result = Bun.spawnSync(command, { cwd: root, timeout });
  process.stdout.write(result.stdout); process.stderr.write(result.stderr);
  console.log(`portable verification: ${result.exitCode === 0 ? "PASS" : "FAIL"} exit=${result.exitCode}`);
  if (result.exitCode !== 0) throw new Error(`portable UI verification failed: exit=${result.exitCode}`);
}
try {
  // Run authoring contracts before the other portable probes, including on hosted CI.
  run(["node", "--test", resolve(root, "scripts/source-authoring-tests.mjs")], 120000);
  run([process.execPath, resolve(root, "node_modules/typescript/bin/tsc"), "-p", "tsconfig.ui.json", "--noEmit", "false",
    "--rewriteRelativeImportExtensions", "--rootDir", root, "--outDir", output]);
  run([process.execPath, resolve(root, "node_modules/typescript/bin/tsc"), "src/index.ts", "--target", "ES2022", "--module", "ESNext",
    "--moduleResolution", "bundler", "--strict", "--skipLibCheck", "--types", "bun", "--rewriteRelativeImportExtensions",
    "--rootDir", root, "--outDir", output]);
  copyFileSync(join(output, "src/index.js"), join(output, "src/index-copy.js"));
  // Declare the temporary output as ESM without Node's removed experimental flag.
  writeFileSync(join(output, "package.json"), '{"type":"module"}\n');
  run(["node", resolve(root, "scripts/portable-ui.mjs"), join(output, "src/ui.js")]);
  run(["node", resolve(root, "scripts/portable-transport.mjs"), join(output, "src/index.js")]);
} finally { rmSync(output, { recursive: true, force: true }); }
