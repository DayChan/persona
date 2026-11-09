import { api, credentialOwner, getToken } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";

// Retained only while an editor or a write exists: navigation shares ordering, without a durable outbox.
const sessions = new Map<string, AboutMeSaves>();
let latestEditor = 0;
export function aboutMeSaves(manager: string, owner: string): AboutMeSaves {
  const key = JSON.stringify([manager, owner]);
  const session = sessions.get(key);
  return session ?? new AboutMeSaves(key, manager, owner, getToken());
}

class AboutMeSaves {
  saved?: string;
  pending?: { value: string; promise: Promise<boolean> };
  private tail = Promise.resolve();
  private count = 0;
  private editors = 0;
  private generation = 0;
  constructor(private key: string, private manager: string, private owner: string, private token: string) {}

  attach(): number {
    sessions.set(this.key, this);
    this.editors++;
    return this.generation = ++latestEditor;
  }
  detach() { this.editors--; this.release(); }
  current(generation: number): boolean { return latestEditor === generation && this.generation === generation && this.owner === credentialOwner(); }
  private release() {
    if (!this.editors && !this.count && sessions.get(this.key) === this) sessions.delete(this.key);
  }

  save(value: string): Promise<boolean> {
    if (this.owner !== credentialOwner()) return Promise.resolve(false);
    // A manual save followed by leaving with the same text is one admitted snapshot.
    if (this.pending?.value === value) return this.pending.promise;
    const promise = this.tail.then(async () => {
      if (this.owner !== credentialOwner()) return false;
      await api("/assistant", { method: "PUT", json: { notes: value }, token: this.token, tokenOwner: this.owner, headers: { [MANAGER_HEADER]: this.manager } });
      if (this.owner !== credentialOwner()) return false;
      this.saved = value;
      return true;
    });
    this.count++;
    this.pending = { value, promise };
    // Rejections release ordering too: a newer snapshot and an explicit retry can still proceed.
    this.tail = promise.then(() => {}, () => {});
    const settled = () => {
      this.count--;
      if (this.pending?.promise === promise) this.pending = undefined;
      this.release();
    };
    void promise.then(settled, settled);
    return promise;
  }
}
