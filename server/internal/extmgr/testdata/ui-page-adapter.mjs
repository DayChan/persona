// Portable stdio facade for the actual installed trusted adapter bytes.
// This exercises transport behavior, not Deno permissions or resource enforcement.
import { Readable } from "node:stream";
import { pathToFileURL } from "node:url";
globalThis.Deno = {
  args: [pathToFileURL(process.argv[3]).href, "http://example.invalid"],
  exit: code => process.exit(code),
  stdin: { readable: Readable.toWeb(process.stdin) },
  stdout: { write: bytes => new Promise((resolve, reject) => process.stdout.write(bytes, error => error ? reject(error) : resolve(bytes.length))) },
};
await import(pathToFileURL(process.argv[2]).href);
