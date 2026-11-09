// A model ID typed by hand (#1644, docs/assistant/model-config.md "Typing a model ID"): the hub's proto.ValidModelID, the
// rule the hub, the manager and the node hold every model to, so the page says why before the hub refuses. A letter or
// digit first (or `~` and then one: a gateway's "latest" alias), then only letters, digits and `. _ - / : @ [ ]`, at
// most 256 bytes. The apps' twin is apple/Shared/ModelID.swift; both are tested on the server's cases.
import { t } from "../i18n";

/** proto.MaxModelIDLen, in UTF-8 bytes (what the hub counts). */
export const MAX_MODEL_ID_LEN = 256;

/** The hub takes a typed ID where its lists lack it: an older one dropped an unlisted pick for the Persona chat's next
 *  message without a word, so that menu offers "Other model…" only with it. */
export const TYPED_MODELS_FEATURE = "typed_models";

/** Why an ID breaks the rule: the first thing wrong, read left to right. */
export type ModelIdProblem =
  | { kind: "empty" }
  | { kind: "start" }
  | { kind: "space" }
  | { kind: "control" }
  | { kind: "character"; char: string }
  | { kind: "too_long" };

const alnum = (c: number) => (c >= 0x61 && c <= 0x7a) || (c >= 0x41 && c <= 0x5a) || (c >= 0x30 && c <= 0x39);
const PUNCTUATION = new Set([..."._-/:@[]"].map((c) => c.charCodeAt(0)));

/** What is wrong with `id` as it is (nothing trimmed); null: the hub takes it. Bytes, as the hub counts them. */
export function modelIdProblem(id: string): ModelIdProblem | null {
  const bytes = new TextEncoder().encode(id);
  if (!bytes.length) return { kind: "empty" };
  for (let i = 0; i < bytes.length; i++) {
    const c = bytes[i];
    if (alnum(c)) continue;
    if (i === 0 && c === 0x7e && bytes.length > 1 && alnum(bytes[1])) continue; // ~ and then a letter or digit
    if (i > 0 && PUNCTUATION.has(c)) continue;
    if (c === 0x20 || c === 0x09) return { kind: "space" };
    if (c < 0x20 || c === 0x7f) return { kind: "control" };
    if (i === 0) return { kind: "start" };
    // every byte before it is ASCII, so it starts the i-th character
    return { kind: "character", char: [...id.slice(i)][0] ?? "" };
  }
  return bytes.length > MAX_MODEL_ID_LEN ? { kind: "too_long" } : null;
}

export const validModelId = (id: string) => modelIdProblem(id) === null;

/** What a field holds as an ID: the spaces and line breaks around it dropped (a pasted ID often ends in one). */
export const cleanModelId = (text: string) => text.trim();

/** The reason under the field. */
export function modelIdProblemText(p: ModelIdProblem): string {
  switch (p.kind) {
    case "empty":
      return t("lib-model-id.empty");
    case "start":
      return t("lib-model-id.start");
    case "space":
      return t("lib-model-id.space");
    case "control":
      return t("lib-model-id.control");
    case "character":
      return t("lib-model-id.character", { char: p.char });
    case "too_long":
      return t("lib-model-id.too_long", { max: MAX_MODEL_ID_LEN });
  }
}

/** Why what is typed can't be used, "" while it is empty or fine. */
export function typedModelProblem(text: string): string {
  const id = cleanModelId(text);
  if (!id) return "";
  const p = modelIdProblem(id);
  return p ? modelIdProblemText(p) : "";
}

/** The rule as one line, under a field that is still empty or fine. */
export const modelIdRule = () => t("lib-model-id.rule");
