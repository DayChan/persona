import { afterEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import { api, ApiError, errorText } from "./api";
import { isOfflineError } from "./feedback";
import { sessionLoadError } from "./session-link";

afterEach(() => vi.unstubAllGlobals());

describe("readable hub errors", () => {
  it.each([
    new ApiError(404, "not found", {}),
    new ApiError(404, "entry missing", {}),
    new ApiError(400, "entry missing", { code: "not_found" }),
    new ApiError(400, "not_found", {}),
    new ApiError(400, "Not Found\n", {}),
  ])("explains missing content or an unsupported endpoint: %s", (error) => {
    expect(errorText(error)).toBe(t("lib-api.not_found", { detail: `HTTP ${error.status}: ${error.message}` }));
    expect(errorText(error)).toContain(error.message);
  });

  it.each([500, 501, 505, 599])("explains HTTP %s without discarding the server's reason", (status) => {
    expect(errorText(new ApiError(status, "storage failed", {}))).toBe(t("lib-api.server_error", { detail: `HTTP ${status}: storage failed` }));
  });

  it.each([502, 503, 504])("explains HTTP %s and still lets offline feedback fold it", (status) => {
    const text = errorText(new ApiError(status, "upstream unavailable", {}));
    expect(text).toBe(t("lib-api.hub_unavailable", { detail: `HTTP ${status}: upstream unavailable` }));
    expect(isOfflineError(text)).toBe(true);
  });

  it.each(["", "HTTP 500"])("keeps a bare status only once: %s", (message) => {
    expect(errorText(new ApiError(500, message, {}, false))).toBe(t("lib-api.server_error", { detail: "HTTP 500" }));
  });

  it("keeps authentication and other client errors intact", () => {
    for (const [status, message] of [[401, "invalid token"], [403, "forbidden"], [409, "already exists"], [429, "slow down"], [400, "file not found in upload"]] as const) {
      expect(errorText(new ApiError(status, message, {}))).toBe(message);
    }
  });

  it("preserves non-API errors and the existing network explanation", () => {
    expect(errorText(new TypeError("Failed to fetch"))).toBe(t("lib-api.cant_reach_hub"));
    expect(errorText(new Error("invalid token"))).toBe("invalid token");
    expect(errorText("not found")).toBe("not found");
    expect(errorText(null)).toBe("null");
  });

  it("leaves the original error available to callers and session-specific explanations", () => {
    const message = "transcript for session deadbeef not found";
    const error = new ApiError(404, message, { code: "not_found" }, true, "request-1");
    expect(sessionLoadError(errorText(error))).toBe("找不到这个会话：可能已经删除，或链接里的 id 不完整");
    expect(error.message).toBe(message);
    expect(error.code).toBe("not_found");
    expect(error.requestId).toBe("request-1");
  });

  it.each([
    [404, '{"error":"not found"}', "HTTP 404: not found"],
    [500, "", "HTTP 500"],
    [502, "Bad Gateway", "HTTP 502: Bad Gateway"],
  ] as const)("formats a failed HTTP %s response through the REST client", async (status, body, detail) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(body, { status })));
    const error = await api("/missing", { token: "test-token" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(errorText(error)).toContain(detail);
    expect(errorText(error)).not.toBe(detail);
  });
});

describe("an error page from a proxy in front of the hub", () => {
  it.each(["<!DOCTYPE html><html><title>502: Bad gateway</title></html>", "\n  <html><body>Bad Gateway</body></html>"])("shows the status, not the markup: %s", async (body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(body, { status: 502 })));
    const text = errorText(await api("/missing", { token: "test-token" }).catch((e: unknown) => e));
    expect(text).toBe(t("lib-api.hub_unavailable", { detail: "HTTP 502" }));
  });
});

// What the hub answers when the computer did not (server/internal/apierr Body and Call): the summary may already be in the
// page's language, the raw text in detail; the code is what the page goes by (#5031).
describe("a computer that did not answer", () => {
  const offline = new ApiError(503, "这台电脑未连接，请连接后重试。", { code: "node_unreachable", retryable: true, delivered: "none", detail: "MacBook: machine is offline" });
  const dropped = new ApiError(502, "rpc: connection closed", { code: "node_unreachable" });
  const timedOut = new ApiError(502, "send: context deadline exceeded", { code: "timeout" });

  it.each([
    ["offline", offline, "lib-api.computer_offline"],
    ["cut off mid-call", dropped, "lib-api.computer_dropped"],
    ["too slow", timedOut, "lib-api.computer_timeout"],
  ] as const)("says what became of a computer that is %s and what to do next", (_, error, key) => {
    const text = errorText(error);
    expect(text).toBe(t(key));
    expect(text).not.toContain(error.message);
    expect(text).not.toContain("HTTP 50");
    // the hub itself answered: not folded into "can't reach the hub" while the page's own connection is down
    expect(isOfflineError(text)).toBe(false);
  });

  it("calls it offline only when nothing went out", () => {
    for (const delivered of [undefined, "unknown", "yes"]) {
      expect(errorText(new ApiError(503, "machine is offline", { code: "node_unreachable", delivered }))).toBe(t("lib-api.computer_dropped"));
    }
  });

  it("leaves other gateway failures as they were", () => {
    for (const body of [{}, { code: "internal" }, { code: "assistant_offline" }]) {
      expect(errorText(new ApiError(503, "upstream unavailable", body))).toBe(t("lib-api.hub_unavailable", { detail: "HTTP 503: upstream unavailable" }));
    }
  });
});

describe("error language", { timeout: 30_000 }, () => {
  afterEach(() => initI18n("zh-Hans"), 30_000);

  it.each([
    ["zh-Hans", "找不到", "服务暂时出错", "hub 暂时无法响应"],
    ["en", "Content not found", "The service ran into a problem", "The hub can't respond right now"],
    ["ja", "内容が見つからない", "サービスで問題が発生しました", "ハブが応答できません"],
  ] as const)("uses the current %s catalog and retains diagnostics", async (lang, missing, server, gateway) => {
    await initI18n(lang);
    for (const [status, explanation] of [[404, missing], [500, server], [503, gateway]] as const) {
      const text = errorText(new ApiError(status, "original error", {}));
      expect(text).toContain(explanation);
      expect(text).toContain(`HTTP ${status}: original error`);
    }
  });

  it.each([
    ["zh-Hans", "不在线", "连接断了", "请求超时"],
    ["en", "is offline", "connection to this computer dropped", "timed out"],
    ["ja", "オフライン", "接続が切れました", "タイムアウト"],
  ] as const)("says what became of the computer in %s", async (lang, offline, dropped, timedOut) => {
    await initI18n(lang);
    expect(errorText(new ApiError(503, "MacBook: machine is offline", { code: "node_unreachable", delivered: "none" }))).toContain(offline);
    expect(errorText(new ApiError(502, "rpc: connection closed", { code: "node_unreachable" }))).toContain(dropped);
    expect(errorText(new ApiError(502, "send: context deadline exceeded", { code: "timeout" }))).toContain(timedOut);
  });
});
