import { MANAGER_HEADER } from "./manager-choice";
import { api, enc } from "./api";
import type { ModelBinding } from "./model-config";

export interface ModelProfile {
  id: string;
  name: string;
  description?: string;
  purposes?: Record<string, ModelBinding>;
  launchers?: Record<string, Record<string, string>>;
  /** The Agent presets (#2225): what agent_start / task_plan's tier and "New agent"'s presets run on, and when to use each. */
  tiers?: Record<string, { launcher: string; model?: string; effort?: string; description?: string }>;
  active?: boolean;
  changes: string[];
}
export interface ModelProfiles {
  revision: number;
  active?: string;
  profiles: ModelProfile[];
  rollback?: { to?: string; changes: string[] };
}
export const modelProfiles = (manager?: string) => api<ModelProfiles>("/model-profiles", { headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
export const switchModelProfile = (id: string, manager?: string) => api<ModelProfiles>(`/model-profiles/${enc(id)}/apply`, { method: "POST", headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
export const rollbackModelProfile = (manager?: string) => api<ModelProfiles>("/model-profiles/rollback", { method: "POST", headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
