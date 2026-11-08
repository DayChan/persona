// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const catalog = (language: string, namespace: string): Record<string, string> =>
  JSON.parse(readFileSync(new URL(`./locales/${language}/${namespace}.json`, import.meta.url), "utf8"));

describe("download mirror copy and official pin status", () => {
  it.each([
    ["en", "Official", "Extension", "mirror", "signature", "original", "never"],
    ["zh-Hans", "官方", "扩展", "镜像", "签名", "原始", "不会"],
    ["ja", "公式", "拡張機能", "ミラー", "署名", "元の", "送信しません"],
  ])("ships scoped, security-preserving mirror copy in %s", (language, official, extension, mirror, signature, original, never) => {
    const hub = catalog(language, "settings-hubsettings");
    const nav = catalog(language, "settings-nav");
    const market = catalog(language, "lib-extension-market");
    expect(nav.hint_hub).toContain(mirror);
    expect(hub.mirror_manager).toContain(extension);
    expect(hub.catalog_mirror_hint).toContain("https://catalog.example.com/persona-extensions.json");
    expect(hub.registry_mirror_hint).toContain("https://registry.example.com/persona");
    for (const term of ["SHA256", "token", signature, original, never]) expect(hub.mirror_security).toContain(term);
    expect(hub.mirror_read_failed).toContain("{error}");
    expect(hub.mirror_write_failed).toContain("{error}");
    expect(market.sig_key_changed).toContain(official);
    expect(market.sig_key_changed).not.toMatch(/trust|CLI/i);
  });
});
