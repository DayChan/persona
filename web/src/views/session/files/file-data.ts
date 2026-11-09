// What the file viewer's views share: a binary file's bytes and the code coloring's classes. Kept out of FileView,
// which imports the notebook and Office views, so they never import it back.
import { fileDownloadURL } from "../../../lib/api";
import { base64Bytes, type FileContent, type TokenKind } from "../../../lib/files";
import { t } from "../../../i18n";

/** A binary file's whole bytes: from file/download when the hub streams it, else the base64 an older hub sent. */
export async function fileBytes(mid: string, f: FileContent, cwd?: string, signal?: AbortSignal): Promise<ArrayBuffer> {
  if (!f.stream && typeof f.data === "string") {
    if (f.truncated) throw new Error(t("session-files-fileview.hub_too_old_preview"));
    return base64Bytes(f.data).buffer;
  }
  const res = await fetch(fileDownloadURL(mid, f.path, cwd), { signal });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}) as Record<string, unknown>);
    throw new Error(typeof body.error === "string" ? body.error : `HTTP ${res.status}`);
  }
  return res.arrayBuffer();
}

export const TOKEN_CLASS: Record<TokenKind, string> = {
  comment: "text-ink-3",
  string: "text-green",
  number: "text-accent-ink",
  const: "text-accent-ink",
  key: "text-red",
  add: "text-green",
  del: "text-red",
  meta: "text-ink-3",
};
