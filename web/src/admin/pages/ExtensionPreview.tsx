// An extension's preview, what installing or upgrading it would do (docs/extensions/extensions.md): the Extensions page's
// install dialog and the market's both show it. Kept out of Extensions.tsx, which loads the market, so the market never
// imports the page back.
import { t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { dependencyText } from "../../lib/extension-model";
import { renameConsent } from "../../lib/extension-market";
import { cx, useNow } from "../../ui/atoms";
import { AdminIcon, type AdminIconName } from "../icons";
import { countdown, pointLabel } from "../logic";
import type { ExtensionPreview, PermissionLine } from "../types";
import { Notice } from "../parts";

const PERM_ICON: Record<string, AdminIconName> = {
  runs_code: "terminal", network: "web", hub_api: "key", launcher_keys: "gauge", secrets: "lock",
  // its program on computers (docs/extensions/extensions.md "Programs that run on computers")
  node_runs_code: "machine", node_network: "web", node_files: "machine", node_macos_privacy: "shield", node_secrets: "lock",
  // command-line programs it installs in its own directory (#4507)
  tools: "terminal",
};

export function Permissions({ lines, title, tone }: { lines: PermissionLine[]; title: string; tone?: "orange" }) {
  return (
    <div>
      <div className={cx("mb-1.5 text-[11.5px] font-medium", tone === "orange" ? "text-orange" : "text-ink-3")}>{title}</div>
      <ul className={cx("divide-y divide-line overflow-hidden rounded-control shadow-hairline", tone === "orange" ? "bg-orange-tint" : "bg-inset")}>
        {lines.map((l) => (
          <li key={l.key} className="flex items-start gap-2.5 px-3 py-2">
            <AdminIcon name={PERM_ICON[l.key] ?? "info"} size={14} className={cx("mt-0.5 shrink-0", tone === "orange" ? "text-orange" : "text-ink-2")} />
            <span className="min-w-0">
              <span className="block text-[12.5px] font-medium text-ink">{l.title}</span>
              {l.detail && <span className="mt-0.5 block text-[11.5px] leading-relaxed break-words text-ink-2">{l.detail}</span>}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function PreviewBody({ pv }: { pv: ExtensionPreview }) {
  const now = useNow(1000);
  const left = countdown(pv.expires_at, now);
  const m = pv.manifest;
  const contributes = Object.entries(m.contributes ?? {});
  return (
    <div className="grid gap-3">
      <div className="flex items-start gap-3">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-[11px] bg-accent-tint text-accent-ink">
          <AdminIcon name="puzzle" size={18} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span className="text-[14px] font-semibold text-ink">{m.name}</span>
            {pv.upgrade ? (
              <span className="font-mono text-[12px] text-ink-2">
                {pv.from} → <span className="text-ink">{m.version}</span>
              </span>
            ) : (
              <span className="font-mono text-[12px] text-ink-2">{m.version}</span>
            )}
          </div>
          <div className="mt-0.5 text-[11.5px] text-ink-3">
            <span className="font-mono">{m.id}</span>
            {m.publisher && <> · {m.publisher}</>}
          </div>
          {m.description && <p className="mt-1.5 text-[12.5px] leading-relaxed text-ink-2">{m.description}</p>}
        </div>
      </div>
      {pv.rename_from && <Notice tone="orange" icon="alert" title={t("lib-extension-market.rename_title")}>
        <p className="break-words" data-rename-consent>{renameConsent(pv)}</p>
      </Notice>}
      {pv.upgrade && pv.more_permissions && (
        <Notice tone="orange" icon="alert" title={t("admin-pages-extensions.more_permissions_title")}>
          {t("admin-pages-extensions.more_permissions_detail")}
        </Notice>
      )}
      {pv.upgrade && (pv.added_permissions?.length ?? 0) > 0 && <Permissions lines={pv.added_permissions!} title={t("admin-pages-extensions.added")} tone="orange" />}
      {pv.permissions.length > 0 ? (
        <Permissions lines={pv.permissions} title={pv.upgrade ? t("admin-pages-extensions.new_can") : t("admin-pages-extensions.can")} />
      ) : (
        <p className="text-[12.5px] text-ink-2">{t("admin-pages-extensions.manifest_only")}</p>
      )}
      {contributes.length > 0 && (
        <p className="text-[12px] leading-relaxed text-ink-2">
          <span className="text-ink-3">{t("admin-pages-extensions.provides_prefix")}</span>
          {contributes.map(([point, items]) => t("admin-pages-extensions.provides_count", { point: pointLabel(point), count: items.length })).join(t("admin-pages-extensions.list_sep"))}
        </p>
      )}
      {!!pv.depends?.length && <div className="grid gap-1 text-ink-2">{pv.depends.map((d) => <p key={d.id}>{t("admin-pages-extensions.dependency", { name: d.name || d.id, text: dependencyText(d) })}</p>)}</div>}
      {!!pv.bundle?.length && <div className="grid gap-3"><p className="font-medium text-ink">{t("admin-pages-extensions.bundle")}</p>{pv.bundle.map((b) => <div key={b.preview_id} className="rounded-control bg-inset p-3"><PreviewBody pv={b} /></div>)}</div>}
      {!!pv.unresolved?.length && <p role="alert" className="text-red">{t("admin-pages-extensions.unresolved", { items: pv.unresolved.join(t("admin-pages-extensions.list_sep")) })}</p>}
      {pv.warnings?.map((w) => (
        <p key={w} className="flex items-start gap-1.5 text-[12px] leading-relaxed text-orange">
          <AdminIcon name="alert" size={13} className="mt-0.5 shrink-0" />
          {w}
        </p>
      ))}
      <p className="text-[11.5px] leading-relaxed text-ink-3">
        {left ? tRich("admin-pages-extensions.preview_left", { left: <span className="font-mono text-ink-2">{left}</span> }) : t("admin-pages-extensions.preview_expired")}
      </p>
    </div>
  );
}
