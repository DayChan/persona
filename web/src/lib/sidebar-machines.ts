import type { Machine } from "./types";

// The fields read by the sidebar's groups, labels, terminal tree and machine menus. In particular, error_code
// distinguishes a computer that has never connected from one that is temporarily offline. Pending computers also
// use last_seen's presence for that distinction; the sidebar never displays its timestamp.
const fields = ["id", "name", "online", "hub_id", "hub_name", "error_code", "trust"] as const;
type SidebarMachine = Pick<Machine, (typeof fields)[number] | "last_seen">;

let source: Machine[] | undefined;
let projection: SidebarMachine[] = [];

/** A machines push that only changes telemetry keeps the sidebar's tree inputs and rows unchanged. The store
 *  replaces machine arrays on updates; keep only the last input and its projection, not a history of pushes. */
export function selectSidebarMachines({ machines }: { machines: Machine[] }): SidebarMachine[] {
  if (machines === source) return projection;
  const previous = new Map(projection.map((m) => [m.id, m]));
  let same = machines.length === projection.length;
  const next = machines.map((m, i) => {
    const old = previous.get(m.id);
    const kept = old && fields.every((key) => old[key] === m[key]) && Boolean(old.last_seen) === Boolean(m.last_seen)
      ? old
      : { id: m.id, name: m.name, online: m.online, hub_id: m.hub_id, hub_name: m.hub_name, error_code: m.error_code, trust: m.trust, last_seen: m.last_seen };
    same &&= kept === projection[i];
    return kept;
  });
  source = machines;
  if (!same) projection = next;
  return projection;
}
