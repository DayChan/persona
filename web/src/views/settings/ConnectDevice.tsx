// Settings › Devices › Connect a new device (#2731, docs/web/web.md "Settings"): a one-time pairing code from the hub, for an app (its QR code;
// "Copy pairing link" for the Mac and Android, which pair by pasting) or for a browser (the sign-in link and its QR code, which
// a phone's camera opens). The code lasts ten minutes and is traded once; the dialog says which device took it.
import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { BASE } from "../../lib/base";
import { clock, loopbackOnly, pairedDevice, secondsLeft, webBase, type PairCodeReply } from "../../lib/connect-device";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import { Button, copyText, Dialog, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Segmented } from "../assistant/parts";
import { QRCode } from "./LoginSteps";

type Target = "app" | "web";

interface Credential {
  id: string;
  name?: string;
  revoked_at?: string;
}

const POLL_MS = 3000;

export function ConnectDevice({ open, onClose, onPaired }: { open: boolean; onClose: () => void; onPaired: () => void }) {
  const [target, setTarget] = useState<Target>("app");
  const [code, setCode] = useState<PairCodeReply | null>(null);
  const [error, setError] = useState("");
  const [paired, setPaired] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const known = useRef<ReadonlySet<string>>(new Set());

  const make = async () => {
    setCode(null);
    setError("");
    setPaired(null);
    try {
      // The credentials there are now: the one that shows up later is the device that used this code.
      const list = await api<{ tokens?: Credential[] }>("/auth/tokens");
      known.current = new Set((list.tokens ?? []).map((c) => c.id));
      const reply = await api<PairCodeReply>("/auth/pair-codes", { method: "POST", json: { web: webBase(location.origin, BASE) } });
      setNow(Date.now());
      setCode(reply);
    } catch (e) {
      setError(errorText(e));
    }
  };

  useEffect(() => {
    if (open) void make();
    else setCode(null);
  }, [open]);

  const left = code ? secondsLeft(code.expires_at, now) : 0;
  const live = open && !!code && !paired && left > 0;

  useEffect(() => {
    if (!live) return;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    // Every 3 s while the tab is shown, and once on coming back, so a device paired meanwhile shows at once. make() has
    // just read the list, so visiblePoll's immediate first read is skipped.
    let started = false;
    const stop = visiblePoll(() => {
      if (!started) return;
      api<{ tokens?: Credential[] }>("/auth/tokens")
        .then((r) => {
          const d = pairedDevice(known.current, r.tokens ?? []);
          if (!d) return;
          setPaired(d.name || d.id);
          onPaired();
        })
        .catch(() => {});
    }, POLL_MS, documentPage);
    started = true;
    return () => (clearInterval(tick), stop());
  }, [live]);

  const qr = target === "app" ? code?.qr : code?.web_qr;
  const link = target === "app" ? code?.pair_url : code?.web_url;

  let body: React.ReactNode;
  if (error) {
    body = (
      <div className="flex flex-col items-center gap-3 py-6 text-center">
        <p className="text-[13px] text-red">{error}</p>
        <Button onClick={() => void make()}>{t("settings-connectdevice.retry")}</Button>
      </div>
    );
  } else if (!code) {
    body = <Loading />;
  } else if (paired) {
    body = (
      <div className="flex flex-col items-center gap-2 py-8 text-center">
        <Icon name="check" size={28} stroke={2.2} className="text-green" />
        <p className="text-[14px] font-medium text-ink">{t("settings-connectdevice.paired", { name: paired })}</p>
        <p className="text-[12px] text-ink-3">{t("settings-connectdevice.paired_hint")}</p>
      </div>
    );
  } else if (left === 0) {
    body = (
      <div className="flex flex-col items-center gap-3 py-8 text-center">
        <p className="text-[13px] text-ink-2">{t("settings-connectdevice.expired")}</p>
        <Button tone="primary" onClick={() => void make()}>{t("settings-connectdevice.new_code")}</Button>
      </div>
    );
  } else {
    body = (
      <div className="flex flex-col items-center gap-3">
        {qr ? <QRCode rows={qr.rows} size={196} label={t(target === "app" ? "settings-connectdevice.qr_app" : "settings-connectdevice.qr_web")} /> : null}
        <p className="max-w-[340px] text-center text-[12.5px] leading-relaxed text-ink-2">
          {t(target === "app" ? "settings-connectdevice.hint_app" : "settings-connectdevice.hint_web")}
        </p>
        {target === "web" && loopbackOnly(location.origin) && (
          <p className="max-w-[340px] text-center text-[12px] leading-relaxed text-orange">{t("settings-connectdevice.loopback")}</p>
        )}
        {link && (
          <Button icon="copy" onClick={() => void copyText(link)}>
            {t(target === "app" ? "settings-connectdevice.copy_app" : "settings-connectdevice.copy_web")}
          </Button>
        )}
        <p className="text-[11.5px] tabular-nums text-ink-3">{t("settings-connectdevice.expires", { time: clock(left) })}</p>
      </div>
    );
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={420}
      title={t("settings-connectdevice.title")}
      footer={
        <>
          {paired && <Button tone="quiet" onClick={() => void make()}>{t("settings-connectdevice.another")}</Button>}
          <Button tone="primary" onClick={onClose}>{t("settings-connectdevice.done")}</Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {!paired && (
          <Segmented<Target>
            label={t("settings-connectdevice.target")}
            value={target}
            onChange={setTarget}
            options={[
              { v: "app", label: t("settings-connectdevice.target_app") },
              { v: "web", label: t("settings-connectdevice.target_web") },
            ]}
          />
        )}
        {body}
      </div>
    </Dialog>
  );
}
