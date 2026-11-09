// Extensions › Extension markets (docs/operations/admin.md "Extensions", docs/extensions/extensions.md "Extension markets"): the markets this hub reads (add with an optional
// write-only token, read again, change the token, remove) and their catalog (search, a market at a time). Installing
// or upgrading from it previews first (POST extensions/-/catalog/preview) and goes through the page's own install /
// upgrade with that preview's id and permissions digest: nothing installs before the user agreed to exactly that.
import { t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { useEffect, useRef, useState } from "react";
import { previewBlocked } from "../../lib/extension-model";
import { ago } from "../../lib/format";
import { agreeLabel, canUninstall, isGitMarket, itemState, MARKET_SPEC_HINT, MARKET_SPEC_PLACEHOLDER, marketKind, marketLine, specLabel, wantsGitHubToken, GITHUB_TOKEN_URL, isGitHubMarket, type CatalogItem, type Catalog, type Market } from "../../lib/extension-market";
import { Button, cx, Dialog, IconButton, toast, useNow } from "../../ui/atoms";
import { addMarket, AdminError, errorText, getCatalog, getMarkets, installExtension, previewFromMarket, refreshMarket, removeMarket, setMarketToken, upgradeExtension } from "../api";
import { AdminIcon } from "../icons";
import { devDialog } from "../router";
import type { ExtensionPreview } from "../types";
import { Card, ConfirmButton, EmptyState, ErrorBox, Field, Loader, Segmented, Tag } from "../parts";
import { PreviewBody } from "./ExtensionPreview";

/** Under a GitHub market's read error: why a token helps, and the way to add it (the markets' own dialog). */
function TokenHint({ onFix }: { onFix: () => void }) {
  return (
    <div className="mt-1 flex flex-wrap items-center gap-2">
      <p className="min-w-[200px] flex-1 text-[12px] leading-relaxed text-ink-3">{t("admin-pages-extensionmarket.token_rate_limit_hint")}</p>
      <Button tone="quiet" className="h-7 px-2" onClick={onFix}>
        <AdminIcon name="key" size={13} />
        {t("admin-pages-extensionmarket.set_token")}
      </Button>
    </div>
  );
}

/** The markets and their catalog; `onChanged` when an extension was installed or upgraded (the list above reloads). */
export function ExtensionMarkets({ onChanged }: { onChanged: () => void }) {
  const [markets, setMarkets] = useState<Market[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [gone, setGone] = useState(false);
  const [adding, setAdding] = useState(() => devDialog() === "market-add");
  const [tokenFor, setTokenFor] = useState<Market | null>(null);
  const [refreshing, setRefreshing] = useState("");
  const [catalogKey, setCatalogKey] = useState(0);

  const load = () =>
    getMarkets().then(
      (l) => (setMarkets(l), setError(null)),
      (e) => {
        // a hub without markets (older: 404; no extension base or database: 503): no section at all
        if (e instanceof AdminError && (e.status === 404 || e.status === 503)) setGone(true);
        else setError(e);
      },
    );
  useEffect(() => void load(), []);
  const put = (m: Market) => setMarkets((l) => (l ?? []).map((x) => (x.id === m.id ? m : x)));
  const changed = () => (void load(), setCatalogKey((k) => k + 1));

  const refresh = async (m: Market) => {
    setRefreshing(m.id);
    try {
      put(await refreshMarket(m.id));
      setCatalogKey((k) => k + 1);
      toast(t("admin-pages-extensionmarket.reread", { name: m.name }), "ok");
    } catch (e) {
      toast(t("admin-pages-extensionmarket.reread_failed", { name: m.name, error: errorText(e) }), "error");
      void load();
    } finally {
      setRefreshing("");
    }
  };
  const remove = async (m: Market) => {
    try {
      await removeMarket(m.id);
      toast(t("admin-pages-extensionmarket.removed", { name: m.name }), "ok");
      changed();
    } catch (e) {
      toast(errorText(e), "error");
    }
  };

  if (gone) return null;
  return (
    <>
      <Card
        title={t("admin-pages-extensionmarket.markets")}
        right={
          <Button tone="quiet" className="h-7 px-2" onClick={() => setAdding(true)}>
            <AdminIcon name="plus" size={13} />
            {t("admin-pages-extensionmarket.add_market")}
          </Button>
        }
        footer={t("admin-pages-extensionmarket.markets_footer")}
      >
        {!markets ? (
          error ? <ErrorBox error={error} onRetry={() => void load()} /> : <Loader />
        ) : markets.length === 0 ? (
          <EmptyState icon="puzzle" title={t("admin-pages-extensionmarket.no_markets")} action={<Button onClick={() => setAdding(true)}>{t("admin-pages-extensionmarket.add_market")}</Button>}>
            {tRich("admin-pages-extensionmarket.no_markets_detail", { github: <span className="font-mono">github:owner/repo</span>, git: <span className="font-mono">git:…</span> })}
          </EmptyState>
        ) : (
          markets.map((m) => (
            <div key={m.id} className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3">
              <span className={cx("flex size-9 shrink-0 items-center justify-center rounded-[10px]", m.error && !m.official ? "bg-red-tint text-red" : "bg-inset text-ink-2 shadow-hairline")}>
                <AdminIcon name={marketKind(m.spec) === "web" ? "web" : "git"} size={16} />
              </span>
              <div className="min-w-[200px] flex-1">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="text-[13.5px] font-medium text-ink">{m.name}</span>
                  {m.official && <Tag tone="accent">{t("admin-pages-extensionmarket.official")}</Tag>}
                  {m.has_token && (
                    <Tag>
                      <AdminIcon name="lock" size={10} />
                      token
                    </Tag>
                  )}
                </div>
                <div className="mt-0.5 font-mono text-[11.5px] break-all text-ink-3">{specLabel(m.spec)}</div>
                <div className="mt-0.5 text-[11.5px] text-ink-3">{marketLine(m, (iso) => ago(iso))}</div>
                {m.error &&
                  // the official market out of reach is the network's (rate limits, no route), not this hub's fault: grey,
                  // like Settings › Extensions (docs/assistant/onboarding.md 4.5); the reason stays on hover
                  (m.official ? (
                    <p title={m.error} className="mt-1 text-[12px] leading-relaxed text-ink-3">{t("admin-pages-extensionmarket.official_unreachable")}</p>
                  ) : (
                    <p className="mt-1 text-[12px] leading-relaxed break-words text-red">{t("admin-pages-extensionmarket.read_failed", { error: m.error })}</p>
                  ))}
                {m.error && wantsGitHubToken(m) && <TokenHint onFix={() => setTokenFor(m)} />}
                {(m.warnings ?? []).map((w) => (
                  <p key={w} className="mt-1 flex items-start gap-1.5 text-[11.5px] leading-relaxed break-words text-orange">
                    <AdminIcon name="alert" size={12} className="mt-0.5 shrink-0" />
                    {w}
                  </p>
                ))}
              </div>
              <div className="ml-auto flex shrink-0 items-center gap-1">
                <IconButton icon="retry" label={t("admin-pages-extensionmarket.reread_label", { name: m.name })} onClick={() => void refresh(m)} disabled={refreshing === m.id} size={28} iconSize={14} />
                {!isGitMarket(m.spec) && !(m.error && wantsGitHubToken(m)) && (
                  <Button tone="quiet" className="h-7 px-2" onClick={() => setTokenFor(m)}>
                    <AdminIcon name="key" size={13} />
                    {m.has_token ? t("admin-pages-extensionmarket.change_token") : t("admin-pages-extensionmarket.set_token")}
                  </Button>
                )}
                <ConfirmButton label={t("admin-pages-extensionmarket.remove_label", { name: m.name })} confirm={t("admin-pages-extensionmarket.remove")} onConfirm={() => remove(m)} />
              </div>
            </div>
          ))
        )}
      </Card>
      {markets && markets.length > 0 && <MarketCatalog key={catalogKey} markets={markets} onChanged={() => (onChanged(), setCatalogKey((k) => k + 1))} />}
      <AddMarketDialog open={adding} onClose={() => setAdding(false)} onAdded={(m) => (setAdding(false), toast(t("admin-pages-extensionmarket.added", { name: m.name }), "ok"), changed())} />
      <TokenDialog market={tokenFor} onClose={() => setTokenFor(null)} onDone={(m) => (put(m), setTokenFor(null), setCatalogKey((k) => k + 1), void load())} />
    </>
  );
}

function MarketCatalog({ markets, onChanged }: { markets: Market[]; onChanged: () => void }) {
  const [q, setQ] = useState("");
  const [market, setMarket] = useState("");
  const [data, setData] = useState<Catalog | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [picked, setPicked] = useState<CatalogItem | null>(null);
  const seq = useRef(0);
  const load = (refresh = false) => {
    const n = ++seq.current;
    setLoading(true);
    getCatalog(q, market, refresh).then(
      (c) => {
        if (n !== seq.current) return;
        setData(c);
        setError(null);
        setLoading(false);
        // Dev only (screenshots): ?dialog=market previews the first extension it can upgrade or install.
        if (devDialog() === "market" && !picked && !q && !market) setPicked(c.extensions.find((x) => itemState(x).kind === "upgrade") ?? c.extensions.find((x) => itemState(x).kind === "install") ?? null);
      },
      (e) => n === seq.current && (setError(e), setLoading(false)),
    );
  };
  useEffect(() => {
    const timer = setTimeout(() => load(), q ? 250 : 0);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, market]);

  return (
    <Card
      title={data ? t("admin-pages-extensionmarket.catalog_count", { count: data.extensions.length }) : t("admin-pages-extensionmarket.catalog")}
      right={<IconButton icon="retry" label={t("admin-pages-extensionmarket.reread_all")} onClick={() => load(true)} disabled={loading} size={24} iconSize={13} className="-mb-1" />}
      footer={t("admin-pages-extensionmarket.catalog_footer")}
    >
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        <label className="flex h-9 min-w-[220px] flex-1 items-center gap-2 rounded-control bg-inset px-3 shadow-hairline">
          <AdminIcon name="search" size={14} className="shrink-0 text-ink-3" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={t("admin-pages-extensionmarket.search_placeholder")}
            aria-label={t("admin-pages-extensionmarket.search_label")}
            spellCheck={false}
            className="h-full min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
          />
        </label>
        {markets.length > 1 && (
          <Segmented value={market || "-"} options={[{ value: "-", label: t("admin-pages-extensionmarket.all") }, ...markets.map((m) => ({ value: m.id, label: m.name }))]} onChange={(v) => setMarket(v === "-" ? "" : v)} label={t("admin-pages-extensionmarket.filter_by_market")} />
        )}
      </div>
      {!data ? (
        error ? <ErrorBox error={error} onRetry={() => load()} /> : <Loader />
      ) : data.extensions.length === 0 ? (
        <p className="px-4 py-6 text-center text-[12.5px] text-ink-3">{q.trim()
            ? t("admin-pages-extensionmarket.not_found", { query: q.trim() })
            : markets.some((m) => m.error && (!market || m.id === market))
              ? t("admin-pages-extensionmarket.catalog_unreadable")
              : t("admin-pages-extensionmarket.catalog_empty")}</p>
      ) : (
        data.extensions.map((it) => {
          const st = itemState(it);
          return (
            <div key={`${it.market}/${it.id}`} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
              <span className={cx("flex size-9 shrink-0 items-center justify-center rounded-[10px]", it.installed ? "bg-accent-tint text-accent-ink" : "bg-inset text-ink-2 shadow-hairline")}>
                <AdminIcon name="puzzle" size={16} />
              </span>
              <div className="min-w-[200px] flex-1">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="text-[13.5px] font-medium text-ink">{it.name}</span>
                  <span className="font-mono text-[11.5px] text-ink-3">{it.version}</span>
                  <Tag tone={st.tone}>{st.label}</Tag>
                </div>
                <div className="mt-0.5 text-[11.5px] text-ink-3">
                  {[it.publisher, it.market_name].filter(Boolean).join(" · ")}
                  {it.description && <span className="text-ink-2"> — {it.description}</span>}
                </div>
                {!!it.tags?.length && (
                  <div className="mt-1 flex flex-wrap gap-1">
                    {it.tags.map((tag) => (
                      <Tag key={tag}>{tag}</Tag>
                    ))}
                  </div>
                )}
                {it.problem && <p className="mt-1 text-[12px] leading-relaxed text-orange">{t("admin-pages-extensionmarket.cannot_install", { problem: it.problem })}</p>}
                {canUninstall(it) && st.kind !== "upgrade" && <p className="mt-1 text-[11.5px] text-ink-3">{t("admin-pages-extensionmarket.manage_above")}</p>}
              </div>
              {(st.kind === "install" || st.kind === "upgrade") && (
                <Button tone={st.kind === "upgrade" ? "primary" : "secondary"} className="ml-auto h-7 px-2.5" onClick={() => setPicked(it)}>
                  <AdminIcon name={st.kind === "upgrade" ? "up" : "plus"} size={13} />
                  {st.kind === "upgrade" ? t("admin-pages-extensionmarket.upgrade") : t("admin-pages-extensionmarket.install")}
                </Button>
              )}
            </div>
          );
        })
      )}
      <MarketInstallDialog item={picked} onClose={() => setPicked(null)} onDone={() => (setPicked(null), onChanged())} />
    </Card>
  );
}

/** Previews a catalog extension, then installs or upgrades exactly that preview. */
function MarketInstallDialog({ item, onClose, onDone }: { item: CatalogItem | null; onClose: () => void; onDone: () => void }) {
  const [pv, setPv] = useState<ExtensionPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const look = () => {
    if (!item) return;
    setPv(null);
    setError("");
    setBusy(true);
    previewFromMarket(item.market, item.id)
      .then(setPv, (e) => setError(errorText(e)))
      .finally(() => setBusy(false));
  };
  useEffect(look, [item]);
  const now = useNow(1000);
  const agree = async () => {
    if (!item || !pv) return;
    setBusy(true);
    setError("");
    try {
      const p = pv.upgrade ? await upgradeExtension(item.id, pv) : await installExtension(pv);
      toast(pv.upgrade ? t("admin-pages-extensionmarket.upgraded", { name: p.name, version: p.version ?? "" }) : t("admin-pages-extensionmarket.installed", { name: p.name }), "ok");
      onDone();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={Boolean(item)}
      onClose={onClose}
      width={540}
      title={item ? (itemState(item).kind === "upgrade" ? t("admin-pages-extensionmarket.upgrade_title", { name: item.name }) : t("admin-pages-extensionmarket.install_title", { name: item.name })) : ""}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("admin-parts.cancel")}
          </Button>
          {pv ? (
            <Button tone="primary" busy={busy} disabled={previewBlocked(pv, now)} onClick={() => void agree()}>
              {pv.rename_from ? agreeLabel(pv) : pv.upgrade ? (pv.more_permissions ? t("admin-pages-extensionmarket.agree_upgrade") : t("admin-pages-extensionmarket.upgrade")) : t("admin-pages-extensionmarket.agree_install")}
            </Button>
          ) : (
            <Button tone="primary" busy={busy} disabled={busy} onClick={look}>
              {t("admin-pages-extensionmarket.look_again")}
            </Button>
          )}
        </>
      }
    >
      {pv ? (
        <PreviewBody pv={pv} />
      ) : busy ? (
        <Loader label={t("admin-pages-extensionmarket.previewing")} />
      ) : null}
      {error && <p className={cx("text-[12.5px] leading-relaxed break-words text-red", pv && "mt-3")}>{error}</p>}
    </Dialog>
  );
}

function AddMarketDialog({ open, onClose, onAdded }: { open: boolean; onClose: () => void; onAdded: (m: Market) => void }) {
  const [spec, setSpec] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (open) (setSpec(""), setToken(""), setError(""));
  }, [open]);
  const git = isGitMarket(spec);
  const go = async () => {
    if (!spec.trim()) return;
    setBusy(true);
    setError("");
    try {
      onAdded(await addMarket(spec.trim(), git ? undefined : token.trim() || undefined));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("admin-pages-extensionmarket.add_market_title")}
      width={500}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("admin-parts.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!spec.trim()} onClick={() => void go()}>
            {t("admin-pages-extensionmarket.add")}
          </Button>
        </>
      }
    >
      <div className="grid gap-3">
        <Field
          label={t("admin-pages-extensionmarket.address")}
          value={spec}
          onChange={setSpec}
          placeholder={MARKET_SPEC_PLACEHOLDER}
          mono
          autoFocus
          autoCapitalize="off"
          hint={MARKET_SPEC_HINT}
        />
        <Field
          label={t("admin-pages-extensionmarket.token_optional")}
          type="password"
          value={token}
          onChange={setToken}
          placeholder={git ? t("admin-pages-extensionmarket.token_git_placeholder") : t("admin-pages-extensionmarket.token_placeholder")}
          mono
          autoComplete="new-password"
          disabled={git}
          hint={git ? t("admin-pages-extensionmarket.token_git_hint") : t("admin-pages-extensionmarket.token_hint")}
        />
        {busy && <p className="text-[12px] text-ink-3">{t("admin-pages-extensionmarket.adding")}</p>}
        {error && <p className="text-[12.5px] leading-relaxed break-words text-red">{error}</p>}
      </div>
    </Dialog>
  );
}

function TokenDialog({ market, onClose, onDone }: { market: Market | null; onClose: () => void; onDone: (m: Market) => void }) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState<"" | "save" | "clear">("");
  const [error, setError] = useState("");
  useEffect(() => (setToken(""), setError("")), [market]);
  const run = async (kind: "save" | "clear") => {
    if (!market || (kind === "save" && !token.trim())) return;
    setBusy(kind);
    setError("");
    try {
      const m = await setMarketToken(market.id, kind === "save" ? token.trim() : "");
      toast(kind === "save" ? t("admin-pages-extensionmarket.token_saved", { name: m.name }) : t("admin-pages-extensionmarket.token_cleared", { name: m.name }), "ok");
      onDone(m);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <Dialog
      open={Boolean(market)}
      onClose={onClose}
      title={market ? t("admin-pages-extensionmarket.token_title", { name: market.name }) : ""}
      width={460}
      footer={
        <>
          {market?.has_token && (
            <Button tone="quiet" className="mr-auto text-red enabled:hover:text-red" busy={busy === "clear"} disabled={!!busy} onClick={() => void run("clear")}>
              {t("admin-pages-extensionmarket.clear_token")}
            </Button>
          )}
          <Button tone="quiet" onClick={onClose}>
            {t("admin-parts.cancel")}
          </Button>
          <Button tone="primary" busy={busy === "save"} disabled={!token.trim() || !!busy} onClick={() => void run("save")}>
            {t("admin-pages-extensionmarket.save")}
          </Button>
        </>
      }
    >
      <div className="grid gap-3">
        <Field
          label={market?.has_token ? t("admin-pages-extensionmarket.new_token") : "token"}
          type="password"
          value={token}
          onChange={setToken}
          mono
          autoFocus
          autoComplete="new-password"
          hint={t("admin-pages-extensionmarket.token_save_hint")}
        />
        {market && isGitHubMarket(market) && (
          <div className="flex flex-wrap items-center gap-2 rounded-[8px] bg-hover px-3 py-2">
            <p className="min-w-[200px] flex-1 text-[12px] leading-relaxed text-ink-3">{t("admin-pages-extensionmarket.token_create_hint")}</p>
            <Button tone="secondary" icon="external" href={GITHUB_TOKEN_URL}>
              {t("admin-pages-extensionmarket.token_create")}
            </Button>
          </div>
        )}
        {error && <p className="text-[12.5px] leading-relaxed break-words text-red">{error}</p>}
      </div>
    </Dialog>
  );
}
