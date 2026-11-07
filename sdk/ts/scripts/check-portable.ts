import { copyFileSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dir, "..");
const output = mkdtempSync(join(tmpdir(), "persona-sdk-portable-"));
function run(command: string[]) {
  const result = Bun.spawnSync(command, { cwd: root, timeout: 30000 });
  process.stdout.write(result.stdout); process.stderr.write(result.stderr);
  if (result.exitCode !== 0) throw new Error(`portable UI verification failed: exit=${result.exitCode}`);
}
try {
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
