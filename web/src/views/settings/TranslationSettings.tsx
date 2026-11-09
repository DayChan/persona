// Settings › Translation (docs/apple/ios.md "Translation", the app's TranslationSettingsView): on / off, the target language, and what gets
// translated: mail (on), and the Feishu chats picked here (none: Feishu has too many messages to translate them all;
// only on a hub that reads Feishu, feature `lark`).
// Kept in this browser. The hub translates new mail ahead of time for the phones that ask; here a mail is translated
// when it is opened.
import { useEffect, useMemo, useState } from "react";
import { t } from "../../i18n";
import { errorText, getToken } from "../../lib/api";
import { modelPurpose, modelSummary, type ModelPurposeBody } from "../../lib/model-config";
import { useLarkOn } from "../../lib/store";
import { LANGUAGES, larkChatChoices, setTranslatePrefs, useTranslatePrefs, type TranslateChat } from "../../lib/translate";
import { Button, cx, Dialog, IconButton, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Monogram } from "../inbox/InboxParts";
import { Group, Row, Switch } from "./SettingsParts";

const controlClass = "h-8 rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]";

export function TranslationSettings({ onConfigure }: { onConfigure: () => void }) {
  const prefs = useTranslatePrefs();
  const [model, setModel] = useState<ModelPurposeBody | null>(null);
  const [picking, setPicking] = useState(false);
  const lark = useLarkOn();

  useEffect(() => {
    modelPurpose("translate").then(setModel, () => {});
  }, []);

  return (
    <>
      <h2 className="mb-3 px-1 text-[15px] font-semibold text-ink">{t("settings-nav.tab_translate")}</h2>
      <Group footer={t("settings-translationsettings.footer")}>
        <Row label={<span className="flex items-center gap-2"><Icon name="translate" size={14} className="text-ink-3" />{t("settings-translationsettings.auto")}</span>}>
          <Switch on={prefs.enabled} onChange={(on) => setTranslatePrefs({ enabled: on })} label={t("settings-translationsettings.auto")} />
        </Row>
        <Row label={t("settings-translationsettings.model")} hint={model ? modelSummary(model.purpose, model.providers) : undefined} onClick={onConfigure} />
      </Group>
      {prefs.enabled && (
        <>
          <Group>
            <Row label={t("settings-translationsettings.target")}>
              <select value={prefs.target} onChange={(e) => setTranslatePrefs({ target: e.target.value })} className={cx(controlClass, "min-w-[140px]")} aria-label={t("settings-translationsettings.target")}>
                {LANGUAGES.map((l) => <option key={l.code} value={l.code}>{l.name}</option>)}
              </select>
            </Row>
          </Group>
          <Group title={t("settings-translationsettings.scope")} footer={lark ? t("settings-translationsettings.scope_footer") : undefined}>
            <Row label={<span className="flex items-center gap-2"><Icon name="mail" size={14} className="text-ink-3" />{t("settings-translationsettings.mail")}</span>}>
              <Switch on={prefs.mail} onChange={(on) => setTranslatePrefs({ mail: on })} label={t("settings-translationsettings.mail_switch")} />
            </Row>
            {lark && (
              <Row label={<span className="flex items-center gap-2"><Icon name="chat" size={14} className="text-ink-3" />{t("settings-translationsettings.lark_chats")}</span>} onClick={() => setPicking(true)}>
                <span className="text-ink-3">{prefs.chats.length ? t("settings-translationsettings.chat_count", { count: prefs.chats.length }) : t("settings-translationsettings.none")}</span>
              </Row>
            )}
          </Group>
          {lark && prefs.chats.length > 0 && (
            <Group title={t("settings-translationsettings.picked_title")}>
              {prefs.chats.map((c) => (
                <Row key={c.id} label={c.name}>
                  <IconButton icon="x" label={t("settings-translationsettings.stop", { name: c.name })} size={24} iconSize={12} onClick={() => setTranslatePrefs({ chats: prefs.chats.filter((x) => x.id !== c.id) })} />
                </Row>
              ))}
            </Group>
          )}
        </>
      )}
      <p className="px-1 text-[11.5px] leading-relaxed text-ink-3">
        {t("settings-translationsettings.local_note")}
      </p>
      {lark && picking && <ChatPicker selected={prefs.chats} onClose={() => setPicking(false)} onSave={(chats) => setTranslatePrefs({ chats })} />}
    </>
  );
}

/** Picks the Feishu chats to translate: the picked ones first (so one no longer recent can still be unpicked), then
 * the pinned and recent ones, searchable by name. */
function ChatPicker({ selected, onClose, onSave }: { selected: TranslateChat[]; onClose: () => void; onSave: (chats: TranslateChat[]) => void }) {
  const [picked, setPicked] = useState(selected);
  const [choices, setChoices] = useState<{ id: string; name: string; avatar?: string; is_group?: boolean }[] | null>(null);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");

  useEffect(() => {
    let live = true;
    larkChatChoices(getToken())
      .then((list) => live && setChoices(list))
      .catch((e) => {
        if (!live) return;
        setError(errorText(e));
        setChoices([]);
      });
    return () => {
      live = false;
    };
  }, []);

  const rows = useMemo(() => {
    const out: TranslateChat[] = [...selected];
    for (const c of choices ?? []) if (!out.some((x) => x.id === c.id)) out.push({ id: c.id, name: c.name });
    const q = query.trim().toLocaleLowerCase();
    return q ? out.filter((c) => c.name.toLocaleLowerCase().includes(q)) : out;
  }, [selected, choices, query]);

  const toggle = (c: TranslateChat) => setPicked((p) => (p.some((x) => x.id === c.id) ? p.filter((x) => x.id !== c.id) : [...p, c]));

  return (
    <Dialog
      open
      onClose={onClose}
      title={t("settings-translationsettings.picker_title")}
      width={460}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>{t("common.cancel")}</Button>
          <Button
            tone="accent"
            onClick={() => {
              onSave(picked);
              onClose();
            }}
          >
            {t("settings-translationsettings.done")}
          </Button>
        </>
      }
    >
      <label className="flex h-8 items-center gap-2 rounded-control bg-field px-2.5 shadow-inset-field focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
        <Icon name="search" size={13} stroke={2} className="shrink-0 text-ink-3" />
        <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("settings-translationsettings.search")} aria-label={t("settings-translationsettings.search")} className="min-w-0 flex-1 bg-transparent text-[12.5px] text-ink outline-none placeholder:text-ink-3" />
      </label>
      {error && <p className="mt-2 text-[12px] text-red">{error}</p>}
      <div className="mt-2 max-h-[46vh] overflow-y-auto">
        {choices === null && !selected.length ? (
          <div className="py-4"><Loading label={t("settings-translationsettings.loading")} /></div>
        ) : !rows.length ? (
          <p className="py-6 text-center text-[12.5px] text-ink-3">{query ? t("settings-translationsettings.no_match") : t("settings-translationsettings.no_chats")}</p>
        ) : (
          rows.map((c) => {
            const on = picked.some((x) => x.id === c.id);
            const conv = choices?.find((x) => x.id === c.id);
            return (
              <button
                key={c.id}
                type="button"
                role="checkbox"
                aria-checked={on}
                onClick={() => toggle(c)}
                className="flex w-full items-center gap-3 rounded-[8px] px-2 py-1.5 text-left transition-colors hover:bg-hover"
              >
                <Monogram name={c.name} size={28} square={conv?.is_group ?? true} src={conv?.avatar} />
                <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{c.name}</span>
                <span className={cx("flex size-[18px] shrink-0 items-center justify-center rounded-full", on ? "bg-accent text-white" : "shadow-[inset_0_0_0_1.5px_var(--line-strong)]")}>
                  {on && <Icon name="check" size={11} stroke={3} />}
                </span>
              </button>
            );
          })
        )}
        {choices === null && selected.length > 0 && <div className="py-2"><Loading label={t("settings-translationsettings.loading")} /></div>}
      </div>
    </Dialog>
  );
}
