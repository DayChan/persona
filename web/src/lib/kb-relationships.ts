import type { KBDetail, KBEntry, KBUpdate } from "./kb";

type List = "people" | "chats" | "projects" | "items";
type End = { kind: string; id: string; canonicalId: string; name: string; field: List; member: string };
export type Removal = { ends: [End, End]; target: { kind: string; id: string }; update: KBUpdate };

const items = new Set(["docs", "meetings", "events", "recordings"]);
const strings = (value: unknown): value is string[] => Array.isArray(value) && value.every((v) => typeof v === "string");

/** The KB's SafeID storage identity; requests and metadata still carry the original source ID. */
export function relationshipCanonicalId(kind: string, id: string): string {
  const safe = (kind === "recordings" ? id.toLowerCase() : id).replace(/[^\p{L}\p{N}._@+-]+/gu, "_").replace(/^[._]+|[._]+$/g, "");
  let result = "", bytes = 0;
  const encoder = new TextEncoder();
  for (const char of safe) {
    bytes += encoder.encode(char).length;
    if (bytes > 120) break;
    result += char;
  }
  return result;
}

/** Detail.links also includes provenance. Only raw membership in a supported relationship list is editable. */
export function relationshipRemoval(detail: KBDetail, name: string, linked: KBEntry): Removal | null {
  const a = detail, b = linked;
  const aid = typeof a.meta?.id === "string" ? a.meta.id : a.id;
  let af: List, bf: List, reverse = false;
  if (a.kind === "projects" && (b.kind === "people" || b.kind === "chats")) {
    af = b.kind; bf = "projects";
  } else if (a.kind === "projects" && items.has(b.kind)) {
    af = "items"; bf = "projects"; reverse = true;
  } else if (b.kind === "projects" && (a.kind === "people" || a.kind === "chats" || items.has(a.kind))) {
    af = "projects"; bf = a.kind === "people" || a.kind === "chats" ? a.kind : "items";
  } else if (a.kind === "people" && b.kind === "chats") {
    af = "chats"; bf = "people";
  } else if (a.kind === "chats" && b.kind === "people") {
    af = "people"; bf = "chats";
  } else return null;
  const bid = relationshipCanonicalId(b.kind, b.id);
  const am = af === "items" ? `${b.kind}/${bid}` : b.id;
  const bm = bf === "items" ? `${a.kind}/${a.id}` : aid;
  const members = a.meta?.[af];
  if (!strings(members) || !members.includes(am)) return null;
  return {
    ends: [{ kind: a.kind, id: aid, canonicalId: a.id, name, field: af, member: am }, { kind: b.kind, id: b.id, canonicalId: bid, name: b.name, field: bf, member: bm }],
    target: reverse ? { kind: b.kind, id: b.id } : { kind: a.kind, id: aid },
    update: { [reverse ? "remove_projects" : `remove_${af}`]: [reverse ? aid : b.id] },
  };
}

export function matchesRemovalEnd(removal: Removal, detail: KBDetail, index: number): boolean {
  const end = removal.ends[index];
  return detail?.kind === end.kind && detail.id === end.canonicalId;
}

/** Both lists must be absent and user-held: a failed attribution cleanup can leave an extension in control. */
export function removalConfirmed(removal: Removal, details: KBDetail[]): boolean {
  return removal.ends.every((end, i) => {
    const d = details[i], members = d?.meta?.[end.field], locks = d?.meta?.locked_fields, provenance = d?.meta?.provenance;
    const userHeld = provenance == null || (typeof provenance === "object" && !Array.isArray(provenance) && !Object.hasOwn(provenance, end.field));
    return matchesRemovalEnd(removal, d, i) &&
      (members == null || (strings(members) && !members.includes(end.member))) &&
      strings(locks) && locks.includes(end.field) && userHeld;
  });
}
