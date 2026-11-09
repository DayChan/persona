// Regenerate synthetic wire fixtures with the real KB engine and a failed filesystem attribution write.
import { constants } from "node:fs";
import { copyFile, unlink } from "node:fs/promises";
import { spawn } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const target = resolve(web, "../server/internal/kb/web_relationships_fixture_test.go");
const output = resolve(web, "src/views/knowledge/fixtures/relationships.json");
await copyFile(resolve(web, "e2e/kb_relationships_fixture_test.go"), target, constants.COPYFILE_EXCL);
try {
  const child = spawn("go", ["test", "-p", "1", "./internal/kb", "-run", "^TestWebRelationshipFixtures$", "-count=1"], {
    cwd: resolve(web, "../server"), stdio: "inherit", timeout: 300_000,
    env: { ...process.env, GOMAXPROCS: "2", WEB_KB_RELATIONSHIP_FIXTURE: output },
  });
  const code = await new Promise((done, reject) => { child.on("error", reject); child.on("exit", done); });
  if (code !== 0) throw new Error(`KB relationship fixture generation failed (${code})`);
} finally {
  await unlink(target);
}
