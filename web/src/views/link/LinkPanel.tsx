// A link opened in the side panel (CLAUDE.md "Links open inside the app", docs/web/web.md "Links"): Feishu documents in a reader
// (the phone's FeishuDocReader), Moments posts as the post, while the hub reads Feishu (feature `lark`); any other page belongs in a browser tab (lib/links.ts says
// why), so one that lands here says so and opens the tab. Text selected in the reader can go to the agent:
// "Add as context" rides along with the next message, "AI edit" asks for a change at once (<persona-context>). A document's
// comments open over it (DocComments.tsx, #1290); with the plugin's `lark_doc_comments` the selection bar's "Comment"
// comments on the passage. Like the phone's reader, a document shows Feishu's cached AI summary (folded to a few lines)
// and an outline of its headings ("Contents" in the header) that scrolls to the one picked.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, enc, getToken } from "../../lib/api";
import { ago } from "../../lib/format";
import { DOC_LABEL, docKind, linkRoute, newContext, type ContextRef } from "../../lib/links";
import { useDocCommentsOn, useLarkOn } from "../../lib/store";
import { Button, copyText, cx, Dialog, EASE, IconButton, Menu, useMenu } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { PanelHeader } from "../../ui/PanelHeader";
import { DocCommentsView, useDocComments } from "./DocComments";
import { DocMarkdown, DocTree } from "./DocContent";
import { docOutline, loadDoc, mediaURL, prepareMarkdown, readInfo, readSummary, type DocContent, type DocInfo, type OutlineItem } from "./feishu-doc";
import { giveContext, giveEdit, handoffLabel } from "./handoff";
import { QuoteIcon } from "./QuoteIcon";
import { t } from "../../i18n";
import { tRich } from "../../i18n/rich";

export function LinkPanel({ url, title, onClose }: { url: string; title?: string; onClose: () => void }) {
  // a panel opened before the hub said what it has turns into the ordinary page once it says it has no Feishu
  const r = linkRoute(url, useLarkOn());
  if (r.kind === "doc") return <DocPanel url={url} title={title} onClose={onClose} />;
  if (r.kind === "moments") return <MomentsPanel id={r.id} url={url} onClose={onClose} />;
  return <WebPanel url={url} title={title} onClose={onClose} />;
}

function host(url: string) {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

function openTab(url: string) {
  window.open(url, "_blank", "noopener,noreferrer");
}

/** ⋯ in the header: copy the link, read again. */
function MoreMenu({ url, onReload }: { url: string; onReload?: () => void }) {
  const menu = useMenu();
  return (
    <>
      <IconButton icon="more" label={t("link-linkpanel.more")} onClick={menu.open} active={Boolean(menu.anchor)} />
      {menu.anchor && (
        <Menu
          anchor={menu.anchor}
          onClose={menu.close}
          align="right"
          width={180}
          items={[
            { key: "copy", label: t("link-linkpanel.copy_link"), icon: "link", onSelect: () => void copyText(url) },
            ...(onReload ? [{ key: "reload", label: t("link-linkpanel.reload"), icon: "retry" as const, onSelect: onReload }] : []),
          ]}
        />
      )}
    </>
  );
}

function ContextButton({ onClick, label = t("link-linkpanel.context_whole") }: { onClick: () => void; label?: string }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="flex size-7 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-[background-color,color,transform] duration-150 hover:bg-hover hover:text-ink active:scale-[0.94]"
    >
      <QuoteIcon />
    </button>
  );
}

/** "Comment" in the header, with how many threads are open. */
function CommentsButton({ count, active, onClick }: { count: number; active: boolean; onClick: () => void }) {
  const label = count ? t("link-linkpanel.comments_open", { count }) : t("link-linkpanel.comments");
  return (
    <button
      type="button"
      aria-label={label}
      aria-pressed={active}
      title={label}
      onClick={onClick}
      className={cx(
        "flex h-7 shrink-0 items-center gap-1 rounded-[8px] px-1.5 text-[12px] font-medium tabular-nums transition-[background-color,color,transform] duration-150 active:scale-[0.94]",
        active ? "bg-hover text-ink" : "text-ink-3 hover:bg-hover hover:text-ink",
      )}
    >
      <Icon name="chat" size={15} />
      {count > 0 && <span>{count}</span>}
    </button>
  );
}

// ── Feishu documents ──

function DocPanel({ url, title, onClose }: { url: string; title?: string; onClose: () => void }) {
  const kind = docKind(url);
  const [content, setContent] = useState<DocContent | null>(null);
  const [error, setError] = useState("");
  const [info, setInfo] = useState<DocInfo | null>(null);
  const [summary, setSummary] = useState<string | null>(null);
  const [outline, setOutline] = useState<OutlineItem[]>([]);
  const [showOutline, setShowOutline] = useState(false);
  const bodyRef = useRef<HTMLDivElement>(null);
  const [round, setRound] = useState(0);
  const [editing, setEditing] = useState<ContextRef | null>(null);
  const writes = useDocCommentsOn();
  const comments = useDocComments(url, content !== null && content.kind !== "unsupported");
  const [showComments, setShowComments] = useState(false);
  const [resolvedTab, setResolvedTab] = useState(false);
  // bumped to put the caret in the comment box ("Comment" on the selection bar, "Write a comment")
  const [focus, setFocus] = useState(0);
  const [of, setOf] = useState(url);
  if (of !== url) {
    setOf(url);
    setShowComments(false);
    setResolvedTab(false);
    setSummary(null);
    setOutline([]);
    setShowOutline(false);
  }

  useEffect(() => {
    let live = true;
    setError("");
    loadDoc(url, title, round > 0)
      .then((c) => live && setContent(c))
      .catch((e: unknown) => {
        if (!live) return;
        setContent(null);
        setError(e instanceof Error ? e.message : String(e));
      });
    readInfo(url)
      .then((i) => live && setInfo(i))
      .catch(() => {});
    readSummary(url)
      .then((s) => live && setSummary(s))
      .catch(() => {});
    return () => {
      live = false;
    };
    // the title only names it while it loads
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url, round]);

  const docTitle = content?.title || title || DOC_LABEL[kind];
  const pick = useCallback(
    (text: string, k: ContextRef["kind"]) => {
      const ref = newContext(k, docTitle, url, text);
      if (k === "edit") setEditing(ref);
      else giveContext(ref);
    },
    [docTitle, url],
  );
  const { setTarget } = comments;
  const openComments = useCallback(
    (passage?: { quote: string; blockId?: string }) => {
      if (passage) {
        setTarget({ kind: "passage", quote: passage.quote, blockId: passage.blockId });
        setResolvedTab(false);
      }
      setShowComments(true);
      if (passage || writes) setFocus((n) => n + 1);
    },
    [setTarget, writes],
  );
  const openCount = comments.threads?.filter((t) => !t.resolved).length ?? 0;

  // The outline is read off the drawn document (both the Markdown and the tree draw <h1>–<h6>).
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (!el || !content || content.kind === "unsupported") {
      setOutline([]);
      return;
    }
    setOutline(docOutline(headingsIn(el).map((h) => ({ level: Number(h.tagName.slice(1)), text: h.textContent ?? "" }))));
  }, [content]);
  const jump = useCallback((item: OutlineItem) => {
    setShowOutline(false);
    const el = bodyRef.current && headingsIn(bodyRef.current)[item.index];
    el?.scrollIntoView({ block: "start", behavior: "smooth" });
  }, []);
  const hasComments = comments.threads !== null || writes;

  return (
    <>
      <PanelHeader
        icon="read"
        title={docTitle}
        subtitle={`${DOC_LABEL[content?.kind === "unsupported" ? content.doc : kind]} · ${host(url)}`}
        onClose={onClose}
        actions={
          <>
            {/* the agent reads the document itself: the whole of it can go along even before (or without) it reading here */}
            {outline.length > 0 && <IconButton icon="list" label={t("link-linkpanel.outline")} active={showOutline} onClick={() => setShowOutline(true)} />}
            {content && content.kind !== "unsupported" && hasComments && <CommentsButton count={openCount} active={showComments} onClick={() => (showComments ? setShowComments(false) : openComments())} />}
            {content?.kind !== "unsupported" && <ContextButton onClick={() => pick("", "context")} />}
            <IconButton icon="external" label={t("link-linkpanel.open_tab")} onClick={() => openTab(url)} />
            <MoreMenu
              url={url}
              onReload={() => {
                setRound((n) => n + 1);
                comments.reload();
              }}
            />
          </>
        }
      />
      {/* the comments lie over the document, which keeps its place underneath */}
      <div className="relative flex min-h-0 flex-1 flex-col">
        <div className="min-h-0 flex-1 overflow-y-auto" inert={showComments}>
          <div className="mx-auto w-full max-w-[760px] px-5 pt-5 pb-10 sm:px-6">
            {content?.kind === "unsupported" ? (
              <Unsupported content={content} url={url} />
            ) : content ? (
              <DocBody bodyRef={bodyRef} summary={summary} content={content} info={info} onPick={pick} onComment={writes ? (quote, blockId) => openComments({ quote, blockId }) : undefined} comments={hasComments ? { open: openCount, onOpen: () => openComments(), writes } : undefined} url={url} />
            ) : error ? (
              <Failed message={error} onRetry={() => setRound((n) => n + 1)} url={url} />
            ) : (
              <Skeleton title={title} />
            )}
          </div>
        </div>
        {showComments && (
          <div className="absolute inset-0 z-10">
            <DocCommentsView url={url} model={comments} writes={writes} resolved={resolvedTab} onResolved={setResolvedTab} focus={focus} onBack={() => setShowComments(false)} />
          </div>
        )}
      </div>
      <EditDialog editing={editing} onClose={() => setEditing(null)} />
      <Dialog open={showOutline} onClose={() => setShowOutline(false)} title={t("link-linkpanel.outline")} width={380}>
        <nav aria-label={t("link-linkpanel.outline")} className="-mx-2 max-h-[min(60vh,480px)] overflow-y-auto">
          {outline.map((o) => (
            <button
              key={o.index}
              type="button"
              onClick={() => jump(o)}
              className={cx("block w-full truncate rounded-control py-1.5 pr-2 text-left text-[13px] transition-colors hover:bg-hover", o.depth === 0 ? "font-medium text-ink" : "text-ink-2")}
              style={{ paddingLeft: 8 + o.depth * 16 }}
            >
              {o.text}
            </button>
          ))}
        </nav>
      </Dialog>
    </>
  );
}

/** The document's headings, the panel's own title left out. */
function headingsIn(el: HTMLElement): HTMLElement[] {
  return [...el.querySelectorAll<HTMLElement>("[data-doc-body] :is(h1, h2, h3, h4, h5, h6)")];
}

/** Feishu's AI summary over the document: a few lines, "Expand" for the rest (FeishuDocReader.summaryCard). */
function SummaryCard({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const [long, setLong] = useState(true);
  const box = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (el) setLong(el.scrollHeight > 104);
  }, [text]);
  return (
    <section aria-label={t("link-linkpanel.ai_summary")} className="mt-4 rounded-card bg-accent-tint/50 px-3.5 py-3 shadow-hairline">
      <div className="flex items-center gap-1.5">
        <Icon name="sparkles" size={13} className="shrink-0 text-accent-ink" />
        <span className="flex-1 text-[12.5px] font-semibold text-ink">{t("link-linkpanel.ai_summary")}</span>
        {long && (
          <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="rounded px-1.5 py-0.5 text-[12px] whitespace-nowrap text-accent-ink hover:bg-accent-tint">
            {open ? t("link-linkpanel.collapse") : t("link-linkpanel.expand")}
          </button>
        )}
      </div>
      <div
        ref={box}
        onClick={() => long && !open && setOpen(true)}
        className={cx("mt-1.5 overflow-hidden text-[12.5px] text-ink-2 [&_p]:my-1", !open && long && "max-h-24 cursor-pointer [mask-image:linear-gradient(to_bottom,black_70%,transparent)]")}
      >
        <DocMarkdown markdown={text} />
      </div>
    </section>
  );
}

function metaLine(info: DocInfo | null) {
  if (!info) return "";
  const parts: string[] = [];
  if (info.owner) parts.push(info.owner);
  const updated = ago(info.updatedAt);
  if (updated) parts.push(t("link-linkpanel.updated", { when: updated }));
  if (info.views) parts.push(t("link-linkpanel.views", { count: info.views }));
  if (info.comments) parts.push(t("link-linkpanel.comment_count", { count: info.comments }));
  return parts.join(" · ");
}

function DocBody({
  bodyRef,
  summary,
  content,
  info,
  onPick,
  onComment,
  comments,
  url,
}: {
  bodyRef: React.RefObject<HTMLDivElement | null>;
  summary: string | null;
  content: Extract<DocContent, { kind: "markdown" | "tree" }>;
  info: DocInfo | null;
  onPick: (text: string, kind: ContextRef["kind"]) => void;
  onComment?: (quote: string, blockId?: string) => void;
  comments?: { open: number; onOpen: () => void; writes: boolean };
  url: string;
}) {
  const markdown = useMemo(() => (content.kind === "markdown" ? prepareMarkdown(content.markdown, content.title, getToken()) : ""), [content]);
  const meta = metaLine(info);
  return (
    <div ref={bodyRef} style={{ animation: `fade-in 200ms ease-out both` }}>
      <Pickable enabled={content.copy} onPick={onPick} onComment={onComment}>
        <h1 className="text-[22px] leading-[1.3] font-semibold tracking-tight text-ink">{content.title}</h1>
        {meta && <div className="mt-2 text-[12px] text-ink-3">{meta}</div>}
        {summary && <SummaryCard text={summary} />}
        <div className="mt-5" data-doc-body>{content.kind === "markdown" ? <DocMarkdown markdown={markdown} /> : <DocTree doc={content.doc} />}</div>
      </Pickable>
      {content.partial && <p className="mt-4 text-[12px] text-ink-3">{t("link-linkpanel.partial")}</p>}
      {!content.copy && <p className="mt-4 text-[12px] text-ink-3">{t("link-linkpanel.copy_forbidden")}</p>}
      <div className="mt-10 flex flex-wrap justify-center gap-2 border-t border-line pt-5">
        {comments && (comments.open > 0 || comments.writes) && (
          <Button icon="chat" onClick={comments.onOpen}>
            {comments.open > 0 ? t("link-linkpanel.comment_count", { count: comments.open }) : t("link-linkpanel.write_comment")}
          </Button>
        )}
        <Button icon="external" onClick={() => openTab(url)}>
          {t("link-linkpanel.open_tab")}
        </Button>
      </div>
    </div>
  );
}

function Unsupported({ content, url }: { content: Extract<DocContent, { kind: "unsupported" }>; url: string }) {
  return (
    <div className="flex flex-col items-center gap-3 pt-12 text-center" style={{ animation: `fade-up 300ms ${EASE} both` }}>
      {content.image ? (
        <a href={mediaURL(content.image)} target="_blank" rel="noreferrer noopener" className="block w-full">
          <img src={mediaURL(content.image)} alt={content.title} className="w-full rounded-card bg-inset shadow-hairline" />
        </a>
      ) : (
        <span className="flex size-14 items-center justify-center rounded-[14px] bg-field text-ink-2">
          <Icon name={content.doc === "file" ? "clip" : "read"} size={24} />
        </span>
      )}
      <div>
        <div className="text-[15px] font-semibold text-ink">{content.title}</div>
        <div className="mt-1 text-[12.5px] text-ink-2">{content.message}</div>
      </div>
      <div className="mt-2 flex flex-wrap justify-center gap-2">
        {content.file && (
          <a
            href={mediaURL(`/api/v1/lark/raw/documents/drive/files/${enc(content.file)}/download`)}
            download
            className="inline-flex h-8 items-center gap-1.5 rounded-control bg-ink px-3 text-[12.5px] font-medium whitespace-nowrap text-surface"
          >
            <Icon name="arrowDown" size={14} stroke={2.1} /> {t("link-linkpanel.download")}
          </a>
        )}
        <Button tone={content.file ? "secondary" : "primary"} icon="external" onClick={() => openTab(url)}>
          {t("link-linkpanel.open_tab")}
        </Button>
      </div>
    </div>
  );
}

function Failed({ message, onRetry, url }: { message: string; onRetry: () => void; url: string }) {
  return (
    <div role="alert" className="flex flex-col items-center gap-3 pt-14 text-center" style={{ animation: `fade-up 300ms ${EASE} both` }}>
      <span className="flex size-12 items-center justify-center rounded-[12px] bg-red-tint text-red">
        <Icon name="info" size={20} />
      </span>
      <div>
        <div className="text-[14.5px] font-semibold text-ink">{t("link-linkpanel.open_failed")}</div>
        <div className="mt-1 max-w-[340px] text-[12.5px] text-ink-2">{message}</div>
      </div>
      <div className="mt-1 flex flex-wrap justify-center gap-2">
        <Button icon="retry" onClick={onRetry}>
          {t("link-linkpanel.retry")}
        </Button>
        <Button tone="primary" icon="external" onClick={() => openTab(url)}>
          {t("link-linkpanel.open_tab")}
        </Button>
      </div>
    </div>
  );
}

const BARS = [92, 86, 58, 95, 80, 44, 90, 84, 52];

function Skeleton({ title }: { title?: string }) {
  return (
    <div aria-busy="true" aria-label={t("link-linkpanel.loading")} className="flex flex-col gap-3">
      {title ? <h1 className="text-[22px] leading-[1.3] font-semibold tracking-tight text-ink">{title}</h1> : <div className="h-6 w-2/3 animate-pulse busy-motion rounded-[6px] bg-field" />}
      <div className="h-3 w-1/3 animate-pulse busy-motion rounded-[4px] bg-field" />
      <div className="mt-3 flex flex-col gap-2.5">
        {BARS.map((w, i) => (
          <div key={i} className="h-3.5 animate-pulse busy-motion rounded-[4px] bg-field" style={{ width: `${w}%`, animationDelay: `${i * 60}ms` }} />
        ))}
      </div>
    </div>
  );
}

// ── Moments posts ──

interface MomentsPost {
  userName?: string;
  createTime?: number | string;
  commentCount?: number;
  reactionCount?: number;
  categoryNames?: string[];
  content?: { text?: string };
}

interface MomentsComment {
  id: string;
  userName?: string;
  createTime?: number | string;
  replyUserName?: string;
  content?: { text?: string };
}

function when(t?: number | string) {
  const n = typeof t === "string" ? Number(t) : t;
  if (!n || !Number.isFinite(n)) return "";
  return ago(new Date(n > 1e11 ? n : n * 1000).toISOString());
}

function MomentsPanel({ id, url, onClose }: { id: string; url: string; onClose: () => void }) {
  const [post, setPost] = useState<MomentsPost | null>(null);
  const [comments, setComments] = useState<MomentsComment[]>([]);
  const [error, setError] = useState("");
  const [round, setRound] = useState(0);
  const [editing, setEditing] = useState<ContextRef | null>(null);

  useEffect(() => {
    let live = true;
    setError("");
    // the explicit token: a Feishu-side 401 must not sign this browser out of the hub
    api<MomentsPost>(`/lark/moments/posts/${enc(id)}`, { token: getToken() })
      .then((p) => live && setPost(p))
      .catch((e: unknown) => live && setError(e instanceof Error ? e.message : String(e)));
    api<{ comments?: MomentsComment[] }>(`/lark/moments/posts/${enc(id)}/comments`, { token: getToken() })
      .then((c) => live && setComments(c.comments ?? []))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [id, round]);

  const name = post?.userName ? t("link-linkpanel.moments_by", { name: post.userName }) : t("link-linkpanel.moments");
  const pick = (text: string, k: ContextRef["kind"]) => {
    const ref = newContext(k, name, url, text);
    if (k === "edit") setEditing(ref);
    else giveContext(ref);
  };

  return (
    <>
      <PanelHeader
        icon="chat"
        title={name}
        subtitle={host(url)}
        onClose={onClose}
        actions={
          <>
            {post && <ContextButton label={t("link-linkpanel.post_as_context")} onClick={() => pick(post.content?.text ?? "", "context")} />}
            <IconButton icon="external" label={t("link-linkpanel.open_tab")} onClick={() => openTab(url)} />
            <MoreMenu url={url} onReload={() => setRound((n) => n + 1)} />
          </>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[680px] px-5 pt-5 pb-10 sm:px-6">
          {post ? (
            <Pickable enabled onPick={pick}>
              <div className="flex items-center gap-2.5">
                <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-field text-[13px] font-semibold text-ink-2">{(post.userName ?? "?").slice(0, 1)}</span>
                <div className="min-w-0">
                  <div className="truncate text-[13.5px] font-semibold text-ink">{post.userName}</div>
                  <div className="text-[11.5px] text-ink-3">{when(post.createTime)}</div>
                </div>
              </div>
              <p className="mt-3 text-[14.5px] leading-[1.7] whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{post.content?.text}</p>
              <div className="mt-3 flex flex-wrap items-center gap-1.5 text-[12px] text-ink-3">
                {(post.categoryNames ?? []).map((c) => (
                  <span key={c} className="rounded-chip bg-field px-1.5 py-0.5 text-ink-2">
                    {c}
                  </span>
                ))}
                {Boolean(post.reactionCount) && <span>{t("link-linkpanel.likes", { count: post.reactionCount ?? 0 })}</span>}
                {Boolean(post.commentCount) && <span>· {t("link-linkpanel.comment_count", { count: post.commentCount ?? 0 })}</span>}
              </div>
              {comments.length > 0 && (
                <div className="mt-6 border-t border-line pt-4">
                  <div className="mb-2 text-[12px] font-medium text-ink-3">{t("link-linkpanel.comments_header", { count: comments.length })}</div>
                  <div className="flex flex-col gap-3">
                    {comments.map((c) => (
                      <div key={c.id} className="text-[13px] leading-[1.6]">
                        <div className="text-[12px] text-ink-3">
                          <span className="font-medium text-ink-2">{c.userName}</span>
                          {c.replyUserName && <> {tRich("link-linkpanel.replied_to", { name: <span className="font-medium text-ink-2">{c.replyUserName}</span> })}</>}
                          {when(c.createTime) && <> · {when(c.createTime)}</>}
                        </div>
                        <div className="whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{c.content?.text}</div>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </Pickable>
          ) : error ? (
            <Failed message={error} onRetry={() => setRound((n) => n + 1)} url={url} />
          ) : (
            <Skeleton />
          )}
        </div>
      </div>
      <EditDialog editing={editing} onClose={() => setEditing(null)} />
    </>
  );
}

// ── any other page ──

function WebPanel({ url, title, onClose }: { url: string; title?: string; onClose: () => void }) {
  const web = linkRoute(url, false, "").kind === "web";
  return (
    <>
      <PanelHeader icon="web" title={title || host(url)} subtitle={host(url)} onClose={onClose} actions={<MoreMenu url={url} />} />
      <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 pb-[10vh] text-center">
        <span className="flex size-12 items-center justify-center rounded-[12px] bg-field text-ink-2">
          <Icon name="web" size={22} />
        </span>
        <div className="max-w-[360px]">
          <div className="text-[14.5px] font-semibold text-ink">{web ? t("link-linkpanel.web_title") : t("link-linkpanel.other_title")}</div>
          <div className="mt-1 text-[12.5px] text-ink-2">{web ? t("link-linkpanel.web_detail") : t("link-linkpanel.other_detail")}</div>
          <div className="mt-2 text-[12px] break-all text-ink-3">{url}</div>
        </div>
        <div className="mt-1 flex flex-wrap justify-center gap-2">
          <Button icon="link" onClick={() => void copyText(url)}>
            {t("link-linkpanel.copy_link")}
          </Button>
          {web && (
            <Button tone="primary" icon="external" onClick={() => openTab(url)}>
              {t("link-linkpanel.open_tab")}
            </Button>
          )}
        </div>
      </div>
    </>
  );
}

// ── picking text ──

interface Tip {
  x: number;
  y: number;
  below: boolean;
  text: string;
  /** The document block the selection lies in, when it is one (a JSON-tree paragraph or heading). */
  block?: string;
}

/** The `data-block` both ends of the range are in, when they are in the same one. */
function blockOf(range: Range): string | undefined {
  const at = (n: Node) => (n instanceof Element ? n : n.parentElement)?.closest<HTMLElement>("[data-block]");
  const a = at(range.startContainer);
  return a && a === at(range.endContainer) ? a.dataset.block || undefined : undefined;
}

/**
 * Content whose selected text can be handed on: a small bar over the selection with "Add as context" and "AI edit", and
 * "Comment" where the document takes comments (mouse, keyboard and touch selections alike; it shows once the pointer is
 * up, not while dragging).
 */
function Pickable({ enabled, onPick, onComment, children }: { enabled: boolean; onPick: (text: string, kind: ContextRef["kind"]) => void; onComment?: (quote: string, blockId?: string) => void; children: React.ReactNode }) {
  const box = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<Tip | null>(null);
  const wide = Boolean(onComment);

  useEffect(() => {
    if (!enabled) return;
    let down = false;
    let timer = 0;
    const read = () => {
      const el = box.current;
      const sel = getSelection();
      if (!el || !sel || sel.isCollapsed || !sel.rangeCount) return setTip(null);
      const range = sel.getRangeAt(0);
      if (!el.contains(range.commonAncestorContainer)) return setTip(null);
      const text = sel.toString().trim();
      if (!text) return setTip(null);
      const r = range.getBoundingClientRect();
      const b = el.getBoundingClientRect();
      const scroller = el.closest(".overflow-y-auto");
      const top = scroller ? scroller.getBoundingClientRect().top : 0;
      const below = r.top - Math.max(top, 0) < 52;
      const half = wide ? 124 : 92; // half the bar: it stays inside the content
      const x = Math.min(Math.max(r.left + r.width / 2 - b.left, half), Math.max(half, b.width - half));
      setTip({ x, y: below ? r.bottom - b.top + 8 : r.top - b.top - 8, below, text, block: blockOf(range) });
    };
    const onDown = (e: PointerEvent) => {
      if ((e.target as HTMLElement).closest?.("[data-pickbar]")) return;
      down = true;
    };
    const onUp = () => {
      down = false;
      clearTimeout(timer);
      timer = window.setTimeout(read, 10);
    };
    const onChange = () => {
      clearTimeout(timer);
      const sel = getSelection();
      if (!sel || sel.isCollapsed) return setTip(null);
      if (!down) timer = window.setTimeout(read, 250); // keyboard / touch selections settle on their own
    };
    document.addEventListener("pointerdown", onDown, true);
    document.addEventListener("pointerup", onUp, true);
    document.addEventListener("selectionchange", onChange);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("pointerdown", onDown, true);
      document.removeEventListener("pointerup", onUp, true);
      document.removeEventListener("selectionchange", onChange);
    };
  }, [enabled, wide]);

  const choose = (kind: ContextRef["kind"] | "comment") => {
    if (!tip) return;
    if (kind === "comment") onComment?.(tip.text, tip.block);
    else onPick(tip.text, kind);
    getSelection()?.removeAllRanges();
    setTip(null);
  };

  return (
    <div ref={box} className={cx("relative", !enabled && "select-none")}>
      {children}
      {tip && (
        <div
          data-pickbar
          role="toolbar"
          aria-label={t("link-linkpanel.selection")}
          onMouseDown={(e) => e.preventDefault()}
          className="absolute z-20 flex items-center gap-0.5 rounded-[10px] border border-tooltip-border bg-tooltip-bg p-1 text-tooltip-fg shadow-overlay"
          style={{ left: tip.x, top: tip.y, translate: `-50% ${tip.below ? "0" : "-100%"}`, animation: `pop-in 140ms ${EASE} both`, transformOrigin: tip.below ? "top center" : "bottom center" }}
        >
          <PickButton onClick={() => choose("context")} label={t("link-linkpanel.as_context")} title={t("link-linkpanel.as_context_hint", { target: handoffLabel() })}>
            <QuoteIcon size={13} />
          </PickButton>
          <PickButton onClick={() => choose("edit")} label={t("link-linkpanel.ai_edit")} title={t("link-linkpanel.ai_edit_hint", { target: handoffLabel() })}>
            <Icon name="spark" size={12} />
          </PickButton>
          {onComment && (
            <PickButton onClick={() => choose("comment")} label={t("link-linkpanel.comment")} title={t("link-linkpanel.comment_hint")}>
              <Icon name="chat" size={12} />
            </PickButton>
          )}
        </div>
      )}
    </div>
  );
}

function PickButton({ onClick, label, title, children }: { onClick: () => void; label: string; title: string; children: React.ReactNode }) {
  return (
    <button
      type="button"
      title={title}
      onClick={onClick}
      className="flex h-7 items-center gap-1.5 rounded-[7px] px-2 text-[12px] font-medium whitespace-nowrap transition-colors hover:bg-tooltip-border"
    >
      {children}
      {label}
    </button>
  );
}

// "AI edit": what to change, then it goes at once.
function EditDialog({ editing, onClose }: { editing: ContextRef | null; onClose: () => void }) {
  const [ask, setAsk] = useState("");
  useEffect(() => setAsk(""), [editing]);
  const send = () => {
    if (!editing) return;
    giveEdit(editing, ask);
    onClose();
  };
  return (
    <Dialog
      open={Boolean(editing)}
      onClose={onClose}
      title={t("link-linkpanel.edit_title")}
      footer={
        <>
          <span className="mr-auto truncate text-[11.5px] text-ink-3">{t("link-linkpanel.send_to", { target: handoffLabel() })}</span>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" icon="send" onClick={send}>
            {t("link-linkpanel.send")}
          </Button>
        </>
      }
    >
      <blockquote className="line-clamp-4 border-l-2 border-line-strong pl-2.5 text-[12.5px] leading-[1.6] whitespace-pre-wrap text-ink-2">{editing?.text}</blockquote>
      <textarea
        autoFocus
        rows={3}
        value={ask}
        onChange={(e) => setAsk(e.target.value)}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing || e.keyCode === 229) return;
          if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            send();
          }
        }}
        placeholder={t("link-linkpanel.edit_placeholder")}
        aria-label={t("link-linkpanel.edit_label")}
        className="mt-3 w-full resize-none rounded-[10px] bg-field px-3 py-2 text-[13.5px] leading-[1.55] text-ink outline-none placeholder:text-ink-3 focus:shadow-hairline"
      />
    </Dialog>
  );
}
