import { t } from "../i18n";
import { api, ApiError, enc, HubTimeoutError, pastHub } from "./api";
import { itemHeaders } from "./manager-choice";
import { isLegacySpacesRouteError, type TodoDelivery } from "./todos";

/** One diagnostic's display fields. Unrecognized fields and future codes never imply completion. */
export interface TaskDoneCheck {
  verdict: string;
  reason: string;
  would_close: boolean;
  skip: string;
  delivery?: TodoDelivery;
}

const record = (value: unknown): Record<string, unknown> =>
  value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
const text = (value: unknown): string => typeof value === "string" ? value.trim() : "";

export async function checkTaskCompletion(id: string, manager: string, signal: AbortSignal): Promise<TaskDoneCheck> {
  const raw = record(await api<unknown>(`/todos/${enc(id)}/done-check`, {
    method: "POST", headers: itemHeaders(manager), signal, timeout: pastHub(90_000),
  }));
  const delivery = record(raw.delivery);
  return {
    verdict: text(raw.verdict), reason: text(raw.reason), would_close: raw.would_close === true, skip: text(raw.skip),
    ...(raw.delivery && typeof raw.delivery === "object" && !Array.isArray(raw.delivery) ? {
      delivery: { state: text(delivery.state), gate: text(delivery.gate), unblock: text(delivery.unblock) },
    } : {}),
  };
}

export function completionCheckError(error: unknown): string {
  if (error instanceof HubTimeoutError) return t("todos.check_timeout");
  if (isLegacySpacesRouteError(error)) return t("todos.check_unsupported");
  if (error instanceof ApiError) {
    switch (error.status) {
      case 401: return t("todos.check_auth");
      case 403: return t("todos.check_forbidden");
      case 404: return t("todos.check_missing");
      case 502: return t("todos.check_model_error");
      case 503: return t("todos.check_unavailable");
    }
  }
  return t(error instanceof TypeError ? "todos.check_network" : "todos.check_failed");
}
