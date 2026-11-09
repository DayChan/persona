// Browser acceptance for the web's voice chat (docs/web/web.md "Voice", #2273), against the mock hub's scripted protocol 2
// voice socket (scripts/mock/voice_mock.py). Everything is made up; nothing reaches a real hub or a real voice service.
//
// The caller owns the services: a mock hub with MOCK_VOICE=1 (MOCK_VOICE_PACE=0.5 halves the wait, and the default pace 1
// checks the uplink over 40 s of each call: run both; --tls-port 0 and MOCK_ENDPOINTS=1 give its hello an HTTPS address
// for the "microphone needs HTTPS" check), and the web app served against it on loopback (a secure context: the
// microphone is allowed there):
//
//   MOCK_VOICE=1 MOCK_VOICE_PACE=0.5 MOCK_ENDPOINTS=1 PORT=<hub port> python3 scripts/mock/mock_hub.py --tls-port 0
//   cd web && PERSONA_HUB_URL=http://127.0.0.1:<hub port> npx vite --port <web port> --strictPort
//   node web/e2e/voice.mjs http://127.0.0.1:<web port> --hub http://127.0.0.1:<hub port> --shots <directory> [--quick]
//
// Chrome gets a fake microphone (--use-fake-device-for-media-stream; on a Mac a 16 kHz WAV of speech made with `say`
// and `afconvert`, else Chrome's own tone) and grants it without asking; where the system still holds getUserMedia
// back (macOS asks for the browser's microphone permission even then), the WAV is played into the stream instead. For each chat (an agent's, the main
// conversation), language (en, zh-Hans) and theme it walks the script: ready → captions → run sending → working
// (Stop <agent> on the line) → the orb interrupts the voice (the mock answers clear, queued audio is stopped) → link
// paused / live → the question card marked "Asking by voice" → the late caption put before the answer → the second
// instruction refused for the computer's unsent line, Clear and send (the mock gets clear_draft) → the result read
// out → the browser suspends the audio (Voice ended · Continue) → Continue (history and resume in the new start) → End.
// It checks that 20 ms PCM16 frames of 640 bytes reached the mock, that they keep coming as silence while muted and
// carry sound again after Unmute, that they come at 50 a second all along (at pace 1 the call is held until that spans
// 40 s, the muted window included: Chrome moved a capture context whose output was exact silence for 30 s to a sink
// that ran at a quarter of real time, docs/lessons/voice.md), and shoots each state, plus the explanation an insecure
// page (plain HTTP) gets. `--quick`: one run (agent, en, light). Exits nonzero on failure or its timeout and
// prints a one-line verdict.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i < 0 ? undefined : args[i + 1];
};
const base = new URL(args[0] ?? "");
const hub = new URL(flag("--hub") ?? "");
const output = resolve(flag("--shots") ?? "");
const quick = args.includes("--quick");
for (const u of [base, hub]) assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(u.hostname), "Use loopback servers only");
assert.equal((await (await fetch(new URL("/api/v1/health", hub))).json()).version, "mock", "Only the mock hub is allowed");
const info = await (await fetch(new URL("/api/v1/plugins/voice/info", hub), { headers: { Authorization: "Bearer mock" } })).json();
assert.equal(info.protocol, 2, "Start the mock hub with MOCK_VOICE=1");
// At the mock's real pace the call reaches the mute after about 35 s; it is held until the uplink has been watched this
// long, past the 30 s after which Chrome moves a silent context to its fake sink.
const pace = (await (await fetch(new URL("/mock/voice2", hub))).json()).pace ?? 1;
const UPLINK_SECONDS = pace >= 1 ? 40 : 0;
await mkdir(output, { recursive: true });

const strings = (lang) => JSON.parse(readFileSync(new URL(`../src/i18n/locales/${lang}/voice.json`, import.meta.url), "utf8"));
const sessions = (await (await fetch(new URL("/api/v1/sessions", hub), { headers: { Authorization: "Bearer mock" } })).json()).sessions;
// an idle Claude Code agent, and the main conversation (the page's root)
const agent = sessions.find((s) => s.agent === "claude" && !s.assistant && s.state === "idle" && s.machine_id === "macbook");
assert.ok(agent, "The mock has an idle Claude agent");
const chats = quick ? [{ name: "agent", path: `/s/${agent.machine_id}/${agent.session_id}`, label: "Claude" }]
  : [{ name: "agent", path: `/s/${agent.machine_id}/${agent.session_id}`, label: "Claude" }, { name: "main", path: "/", label: "Persona" }];
const runs = quick ? [{ lang: "en", theme: "light" }] : ["en", "zh-Hans"].flatMap((lang) => ["light", "dark"].map((theme) => ({ lang, theme })));

// The microphone: speech at 16 kHz where the machine can make it, else Chrome's own fake device (a tone).
let wav = flag("--audio");
if (!wav && process.platform === "darwin") {
  try {
    const aiff = join(output, "speech.aiff");
    wav = join(output, "speech.wav");
    execFileSync("say", ["-o", aiff, "Please fix the failing login page test, and the lint errors too."], { timeout: 30_000 });
    execFileSync("afconvert", ["-f", "WAVE", "-d", "LEI16@16000", "-c", "1", aiff, wav], { timeout: 30_000 });
  } catch {
    wav = undefined;
  }
}
const launchArgs = ["--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream", ...(wav && existsSync(wav) ? [`--use-file-for-fake-audio-capture=${wav}`] : [])];
const executable = process.env.PERSONA_E2E_CHROMIUM;
const launch = () => chromium.launch({ headless: true, args: launchArgs, ...(executable ? { executablePath: executable } : { channel: flag("--channel") ?? "chrome" }) });
// about a minute a run at pace 1 (each call held for 40 s), half that at 0.5
const minutes = pace >= 1 ? 18 : 12;
const deadline = setTimeout(() => {
  console.log(`FAIL voice e2e: timed out after ${minutes} minutes`);
  process.exit(124);
}, minutes * 60_000);

// On macOS, Chrome's fake capture device still waits on the system's microphone permission for the browser, which nobody
// can grant in a headless run: getUserMedia never settles. There the same WAV (or a tone) is played through WebAudio
// into the stream getUserMedia hands back, so everything after it (the worklet, resampling, frames, the socket) is real.
// Asked in a browser of its own: one left waiting on that permission does not answer anything afterwards.
async function fakeDeviceAnswers() {
  const probe = await launch();
  try {
    const page = await probe.newPage();
    await page.goto(new URL("/api/v1/health", base).href);
    return await page.evaluate(() => Promise.race([
      navigator.mediaDevices.getUserMedia({ audio: true }).then((s) => (s.getTracks().forEach((t) => t.stop()), true), () => false),
      new Promise((done) => setTimeout(() => done(false), 3000)),
    ]));
  } finally {
    await Promise.race([probe.close(), new Promise((done) => setTimeout(done, 5000))]);
  }
}
const microphone = (await fakeDeviceAnswers()) ? "chrome-fake-device" : "webaudio-stream";
const speech = microphone === "webaudio-stream" && wav && existsSync(wav) ? readFileSync(wav).toString("base64") : "";

/** getUserMedia from a WebAudio graph: the WAV looping, or a 440 Hz tone without one. */
function streamMicrophone(b64) {
  const Ctx = window.AudioContext;
  navigator.mediaDevices.getUserMedia = async () => {
    const c = new Ctx();
    await c.resume();
    const dest = c.createMediaStreamDestination();
    if (b64) {
      const bytes = Uint8Array.from(atob(b64), (ch) => ch.charCodeAt(0));
      const src = c.createBufferSource();
      src.buffer = await c.decodeAudioData(bytes.buffer);
      src.loop = true;
      src.connect(dest);
      src.start();
    } else {
      const osc = c.createOscillator();
      osc.connect(dest);
      osc.start();
    }
    return dest.stream;
  };
}

const result = { status: "running", microphone, wav: Boolean(wav), runs: [], screenshots: [] };
const browser = await launch();
const mockLog = async () => (await fetch(new URL("/mock/voice2", hub))).json();
/** Polls `check` (in this process, not the page: the mock hub is another origin) until it holds or `ms` passes. */
async function until(check, ms, what) {
  const end = Date.now() + ms;
  for (;;) {
    if (await check()) return;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 100));
  }
}

/** The mock's frame count four times a second while a call is on, for `uplinkPace` afterwards. */
function sampleUplink() {
  const samples = [];
  let on = true;
  const loop = (async () => {
    while (on) {
      const { audio } = await mockLog();
      samples.push({ t: performance.now(), audio });
      await new Promise((r) => setTimeout(r, 250));
    }
  })();
  return { from: performance.now(), stop: async () => ((on = false), await loop, samples) };
}

/** Frames a second over the samples: the whole span, and the slowest and fastest window of `windowMs`. */
function uplinkPace(samples, windowMs = 5000) {
  const first = samples[0];
  const last = samples.at(-1);
  const seconds = (last.t - first.t) / 1000;
  const rates = [];
  for (const a of samples) {
    const b = samples.find((s) => s.t - a.t >= windowMs);
    if (!b) break;
    rates.push((b.audio - a.audio) / ((b.t - a.t) / 1000));
  }
  return { seconds, average: (last.audio - first.audio) / seconds, min: Math.min(...rates), max: Math.max(...rates) };
}

/** What the page's audio did: buffer sources started and stopped, which are still to play, and the contexts it made
 *  (to suspend them). */
function instrument() {
  const seen = { starts: 0, stops: 0, contexts: [], live: new Set() };
  window.__voiceE2E = seen;
  /** Sources scheduled to end after now and not stopped: the voice's audio still queued. */
  seen.pending = () => [...seen.live].filter((s) => s.__end > s.context.currentTime).length;
  /** Every kind the bar's second line took, in order: one that lasts half a second (handing, at pace 0.5) is not missed
   *  while the runner is still busy with the screenshot before it. */
  seen.kinds = [];
  new MutationObserver(() => {
    const kind = document.querySelector('[data-testid="voice-line"]')?.getAttribute("data-kind");
    if (kind && seen.kinds.at(-1) !== kind) seen.kinds.push(kind);
  }).observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ["data-kind"] });
  const Base = window.AudioContext;
  window.AudioContext = class extends Base {
    constructor(o) {
      super(o);
      seen.contexts.push(this);
    }
  };
  const start = AudioBufferSourceNode.prototype.start;
  AudioBufferSourceNode.prototype.start = function (when = 0, ...rest) {
    seen.starts++;
    this.__end = Math.max(when, this.context.currentTime) + (this.buffer?.duration ?? 0);
    seen.live.add(this);
    this.addEventListener("ended", () => seen.live.delete(this));
    return start.call(this, when, ...rest);
  };
  const stop = AudioBufferSourceNode.prototype.stop;
  AudioBufferSourceNode.prototype.stop = function (...a) {
    seen.stops++;
    seen.live.delete(this);
    return stop.apply(this, a);
  };
}

try {
  for (const { lang, theme } of runs) {
    const L = strings(lang);
    for (const chat of chats) {
      const name = `${chat.name}-${lang}-${theme}`;
      const run = { name, errors: [], checks: [], guard: [] };
      result.runs.push(run);
      const context = await browser.newContext({ viewport: { width: 1280, height: 860 }, deviceScaleFactor: 2, colorScheme: theme, serviceWorkers: "block" });
      await context.addInitScript(({ lang, theme }) => {
        localStorage.setItem("persona.token", "mock");
        localStorage.setItem("persona.language", lang);
        localStorage.setItem("persona.theme", theme);
      }, { lang, theme });
      if (microphone === "webaudio-stream") await context.addInitScript(streamMicrophone, speech);
      await context.addInitScript(instrument);
      await context.route("**/*", (route) => {
        const u = new URL(route.request().url());
        return [base.origin, hub.origin].includes(u.origin) || u.protocol === "data:" || u.protocol === "blob:" ? route.continue() : route.abort();
      });
      const page = await context.newPage();
      page.setDefaultTimeout(30_000);
      page.on("pageerror", (e) => run.errors.push(e.message));
      page.on("console", (m) => {
        if (m.type() === "error" && !/Failed to load resource/.test(m.text())) run.errors.push(m.text());
        // audio.ts's pace check: the microphone's frames fell behind the wall clock (debug level)
        if (m.type() === "debug" && m.text().startsWith("[voice]")) run.guard.push(m.text());
      });
      const shot = async (state) => {
        await page.evaluate(() => document.fonts.ready);
        // what is still moving into place (the drawer after a resize, a row fading up) settles first
        await page.evaluate(() => Promise.race([
          Promise.all(document.getAnimations().filter((a) => (a.effect?.getComputedTiming().endTime ?? Infinity) < 2000).map((a) => a.finished.catch(() => {}))),
          new Promise((done) => setTimeout(done, 1500)),
        ]));
        await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${name} ${state}: no horizontal overflow`);
        const file = `${name}-${state}.png`;
        await page.screenshot({ path: join(output, file) });
        result.screenshots.push(file);
      };
      const bar = page.getByTestId("voice-bar");
      const line = page.getByTestId("voice-line");
      const phase = (p) => page.waitForFunction((p) => document.querySelector('[data-testid="voice-bar"]')?.getAttribute("data-voice-phase") === p, p, { timeout: 30_000 });
      const lineKind = (k) => page.locator(`[data-testid="voice-line"][data-kind="${k}"]`).waitFor({ timeout: 40_000 });
      const sawLineKind = (k) => page.waitForFunction((k) => window.__voiceE2E.kinds.includes(k), k, { timeout: 40_000 });
      const singleLineButtons = async (state) => {
        const tall = await page.evaluate(() => [...document.querySelectorAll('[data-testid="voice-bar"] button, [data-testid="voice-ended"] button, [data-testid="composer-voice"]')]
          .filter((b) => b.getBoundingClientRect().height > 40 || b.scrollWidth > b.clientWidth + 1).map((b) => b.textContent || b.getAttribute("aria-label")));
        assert.deepEqual(tall, [], `${name} ${state}: every button on one line`);
      };

      await page.goto(new URL(chat.path, base).href);
      await page.waitForFunction((dark) => document.documentElement.classList.contains("dark") === dark, theme === "dark");
      const entry = page.locator('[data-testid="composer-voice"][data-voice-mode="start"]');
      await entry.waitFor();
      assert.equal(await entry.getAttribute("aria-label"), L.voice_chat);
      await shot("entry");
      // with words in the box, send takes its place back and voice steps aside as a plain icon
      const box = page.locator("textarea").first();
      await box.fill("draft");
      await page.locator('[data-testid="composer-voice"][data-voice-mode="start"]:not(.bg-ink)').waitFor();
      assert.equal(await entry.evaluate((b) => b.classList.contains("bg-ink")), false);
      await shot("entry-typing");
      await box.fill("");

      const before = await mockLog();
      await entry.click();
      await bar.waitFor();
      await phase("listening");
      const uplink = sampleUplink();
      await shot("listening");
      await singleLineButtons("listening");
      run.checks.push("ready: listening");

      // the user's words as a caption, then the instruction on its way
      await page.locator('[data-voice-caption="user"][data-final]').first().waitFor();
      await sawLineKind("handing");
      run.checks.push("captions, run sending");

      // the voice speaks, and the orb becomes "Interrupt the voice" (a different button from Stop <agent>)
      await phase("speaking");
      await shot("speaking");
      assert.ok(await page.evaluate(() => window.__voiceE2E.starts > 0), `${name}: the voice's audio played`);
      run.checks.push("speaking");

      // working: the hub's activity words, how long, and Stop <agent> (a different button from the orb)
      await lineKind("working");
      const stop = page.getByRole("button", { name: L.action_stop.replace("{name}", chat.label), exact: true });
      await stop.waitFor();
      assert.notEqual(L.action_stop.replace("{name}", chat.label), L.interrupt);
      await shot("working");
      run.checks.push("run working, Stop <agent>");

      // the upstream pauses while the agent works, and comes back
      await lineKind("paused");
      assert.equal(await page.getByTestId("voice-status").textContent(), L.status_paused);
      await shot("paused");
      await page.waitForFunction(() => document.querySelector('[data-testid="voice-line"]')?.getAttribute("data-kind") !== "paused", null, { timeout: 30_000 });
      run.checks.push("link paused / live");

      // the question card the voice asks: marked, and the line says to just answer
      await lineKind("asking");
      await page.getByTestId("voice-asking").waitFor();
      assert.equal((await page.getByTestId("voice-asking").textContent()).trim(), L.asking_mark);
      await shot("asking");
      // answered by voice: the mark goes, and the late caption goes before the answer it came in
      await page.getByTestId("voice-asking").waitFor({ state: "detached", timeout: 40_000 });
      await page.waitForFunction(() => {
        const rows = [...document.querySelectorAll('[data-testid="voice-tail"] [data-voice-caption]')].map((r) => r.textContent);
        const u = rows.findIndex((t) => t.includes("第一个"));
        const a = rows.findIndex((t) => t.includes("好，告诉它了。"));
        return u >= 0 && a > u;
      }, null, { timeout: 30_000 });
      run.checks.push("ask marker, late caption before its answer");

      // the second instruction refused for the computer's unsent line: Clear and send hands clearable back
      await lineKind("notSent");
      const clear = page.locator('[data-voice-action="clearAndSend"]');
      await clear.waitFor();
      assert.equal((await clear.textContent()).trim(), L.action_clear_send);
      await shot("refused");
      await singleLineButtons("refused");
      await page.setViewportSize({ width: 390, height: 844 });
      await page.waitForTimeout(700); // the sidebar becomes a drawer
      await shot("refused-narrow");
      await singleLineButtons("refused narrow");
      await page.setViewportSize({ width: 1280, height: 860 });
      await clear.click();
      await until(async () => (await mockLog()).resends.length > before.resends.length, 10_000, "the resend");
      const resends = (await mockLog()).resends;
      assert.deepEqual(resends.at(-1), { id: "voice-2b8d0e6f4a1c9375", clear_draft: "fix it" }, `${name}: Clear and send returns clearable`);
      run.checks.push("refused(input_draft), Clear and send");

      // the result is read out; the orb interrupts the voice (never the agent): the page drops what it queued, the mock
      // stops and answers clear, and nothing more plays
      await page.locator('[data-voice-caption="assistant"]', { hasText: "登录页的测试修好了" }).waitFor({ timeout: 40_000 });
      run.checks.push("done, result read out");
      await phase("speaking");
      const orb = page.getByRole("button", { name: L.interrupt, exact: true });
      await orb.waitFor();
      const interrupts = (await mockLog()).interrupts;
      // read the queue and click in one go, in the page: the 20 ms chunks keep coming and going around a click from here
      const at = await page.evaluate(() => new Promise((done, fail) => {
        const seen = window.__voiceE2E;
        const until = performance.now() + 10_000;
        const tick = () => {
          const orb = document.querySelector('button[data-testid="voice-orb"]');
          if (orb && seen.pending() > 0) {
            const before = { stops: seen.stops, pending: seen.pending() };
            orb.click();
            done({ before, after: { stops: seen.stops, pending: seen.pending(), starts: seen.starts } });
          } else if (performance.now() > until) fail(new Error("no audio was queued while the voice spoke"));
          else setTimeout(tick, 2);
        };
        tick();
      }));
      assert.ok(at.before.pending > 0 && at.after.stops - at.before.stops >= at.before.pending, `${name}: clear stopped all ${at.before.pending} queued chunks (${JSON.stringify(at)})`);
      assert.equal(at.after.pending, 0, `${name}: nothing left queued`);
      await phase("listening");
      await page.waitForTimeout(600);
      assert.equal(await page.evaluate(() => window.__voiceE2E.starts), at.after.starts, `${name}: nothing more played after clear`);
      assert.equal((await mockLog()).interrupts, interrupts + 1);
      run.checks.push("orb interrupts, clear stops playback");

      // ⋯: Type a line (the same as saying it: the mock echoes it as the user's caption), All captions, Voice
      await page.getByTestId("voice-more").click();
      await page.getByRole("menuitem", { name: L.type_line }).waitFor();
      await shot("menu");
      await page.getByRole("menuitem", { name: L.type_line }).click();
      const typed = page.getByTestId("voice-type-input");
      await typed.waitFor();
      await typed.fill("check the lint too");
      await shot("type-line");
      await typed.press("Enter");
      await until(async () => (await mockLog()).texts.at(-1) === "check the lint too", 10_000, "the typed line");
      await page.locator('[data-voice-caption="user"]', { hasText: "check the lint too" }).waitFor();
      await page.getByTestId("voice-more").click();
      await page.getByRole("menuitem", { name: L.all_captions }).click();
      const all = page.getByRole("dialog");
      await all.getByText("check the lint too").waitFor();
      await shot("captions");
      await page.keyboard.press("Escape");
      await all.waitFor({ state: "detached" });
      run.checks.push("⋯: type a line, all captions");

      // the microphone's frames reached the mock: 20 ms of 16 kHz PCM16 each
      const sent = await mockLog();
      const frames = sent.audio - before.audio;
      assert.ok(frames > 200, `${name}: PCM frames reached the mock (${frames})`);
      assert.deepEqual(Object.keys(sent.audio_sizes), ["640"], `${name}: every frame is 640 bytes`);
      run.frames = frames;
      run.checks.push(`${frames} PCM frames of 640 bytes`);

      // Mute: the frames go on every 20 ms (the extension's clock), all of them silence; Unmute: the microphone again
      assert.ok(sent.audio - sent.audio_silent > 0, `${name}: the microphone sent something other than silence`);
      const mute = page.getByTestId("voice-mute");
      await mute.click();
      await page.locator('[data-testid="voice-mute"][aria-pressed="true"]').waitFor();
      await page.waitForTimeout(400); // frames already on their way when it was clicked
      const muteFrom = await mockLog();
      await page.waitForTimeout(1200);
      const muteTo = await mockLog();
      const muted = muteTo.audio - muteFrom.audio;
      assert.ok(muted >= 30, `${name}: frames kept coming while muted (${muted})`);
      assert.equal(muteTo.audio_silent - muteFrom.audio_silent, muted, `${name}: every frame while muted is silence`);
      await shot("muted");
      await mute.click();
      await page.locator('[data-testid="voice-mute"][aria-pressed="false"]').waitFor();
      await until(async () => {
        const now = await mockLog();
        return now.audio - now.audio_silent > muteTo.audio - muteTo.audio_silent;
      }, 10_000, "the microphone again after Unmute");
      run.checks.push(`muted: ${muted} frames, all silence; unmuted: sound again`);

      // 50 frames a second from ready to here, the muted window included; at pace 1 over 40 s at least
      const hold = uplink.from + (UPLINK_SECONDS + 1) * 1000 - performance.now();
      if (hold > 0) await page.waitForTimeout(hold);
      const rate = uplinkPace(await uplink.stop());
      run.uplink = { ...rate, guard: run.guard.length };
      const said = `${rate.average.toFixed(1)} frames/s over ${rate.seconds.toFixed(0)} s (5 s windows ${rate.min.toFixed(1)}-${rate.max.toFixed(1)})`;
      assert.ok(rate.seconds >= UPLINK_SECONDS, `${name}: the uplink was watched ${rate.seconds.toFixed(1)} s, ${UPLINK_SECONDS} s needed`);
      assert.ok(rate.average >= 47 && rate.average <= 53, `${name}: the uplink keeps 50 frames/s: ${said}`);
      assert.ok(rate.min >= 40, `${name}: the uplink never slows down: ${said}`);
      run.checks.push(`uplink ${said}${run.guard.length ? `; ${run.guard.length} pace notes` : ""}`);

      // the browser suspends the audio: the call ends by itself and leaves Continue
      await page.evaluate(() => Promise.all(window.__voiceE2E.contexts.filter((c) => c.state === "running").map((c) => c.suspend())));
      await page.getByTestId("voice-ended").waitFor();
      await bar.waitFor({ state: "detached" });
      await until(async () => (await mockLog()).closes > before.closes, 10_000, "the socket to close");
      await shot("ended");
      await singleLineButtons("ended");
      run.checks.push("passive end: Voice ended · Continue");

      // Continue picks it up again with the captions and the runs not over yet
      const starts = (await mockLog()).starts.length;
      await page.getByTestId("voice-ended").getByRole("button", { name: L.continue, exact: true }).click();
      await bar.waitFor();
      await phase("listening");
      const resumed = (await mockLog()).starts.slice(starts)[0];
      assert.ok(resumed?.history?.length > 0, `${name}: the new start hands the captions back`);
      assert.ok(resumed?.resume?.runs?.some((r) => r.id === "voice-2b8d0e6f4a1c9375"), `${name}: and the run still open`);
      run.checks.push("Continue: history + resume");

      // ⋯ › Voice: the extension's voices for the call's model; picking one picks the call up again with it
      await page.getByTestId("voice-more").click();
      await page.getByRole("menuitem", { name: L.voice_menu }).click();
      const calm = page.getByRole("menuitemradio", { name: "沉稳" });
      await calm.waitFor();
      await shot("voices");
      await calm.click();
      await until(async () => (await mockLog()).starts.at(-1)?.voice === "calm", 10_000, "a start with the new voice");
      await phase("listening");
      run.checks.push("⋯ › Voice reconnects with the new voice");

      // End: the composer comes back, nothing stays
      await page.getByTestId("voice-end").click();
      await bar.waitFor({ state: "detached" });
      await entry.waitFor();
      assert.equal(await page.getByTestId("voice-ended").count(), 0);
      run.checks.push("End");
      await context.close();

      // a page that is not a secure context: the button explains, and offers the hub's HTTPS address when it has one
      const insecure = await browser.newContext({ viewport: { width: 1280, height: 860 }, deviceScaleFactor: 2, colorScheme: theme, serviceWorkers: "block" });
      await insecure.addInitScript(({ lang, theme }) => {
        localStorage.setItem("persona.token", "mock");
        localStorage.setItem("persona.language", lang);
        localStorage.setItem("persona.theme", theme);
        Object.defineProperty(window, "isSecureContext", { get: () => false });
      }, { lang, theme });
      const p2 = await insecure.newPage();
      p2.on("pageerror", (e) => run.errors.push(e.message));
      await p2.goto(new URL(chat.path, base).href);
      const explain = p2.locator('[data-testid="composer-voice"][data-voice-mode="explain"]');
      await explain.waitFor();
      await explain.click();
      await p2.getByTestId("voice-unavailable").waitFor();
      const dialog = p2.getByRole("dialog");
      assert.equal((await dialog.getByRole("heading").textContent()).trim(), L.unavailable_insecure_title);
      const https = await dialog.getByRole("link", { name: L.open_https }).getAttribute("href").catch(() => null);
      if (https) assert.ok(https.startsWith("https://"), `${name}: the HTTPS address is https`);
      await p2.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
      await p2.waitForTimeout(300);
      const file = `${name}-http-not-secure.png`;
      await p2.screenshot({ path: join(output, file) });
      result.screenshots.push(file);
      run.checks.push(`insecure page explains${https ? " and offers the HTTPS address" : ""}`);
      await insecure.close();

      assert.deepEqual(run.errors, [], `${name}: no page errors`);
      console.log(`PASS ${name}: ${run.checks.join(" · ")}`);
    }
  }
  result.status = "pass";
} catch (error) {
  result.status = "fail";
  result.error = error.stack;
  console.error(error);
} finally {
  clearTimeout(deadline);
  await browser.close();
  await writeFile(join(output, "results.json"), JSON.stringify(result, null, 2) + "\n");
  console.log(result.status === "pass" ? `PASS voice e2e: ${result.runs.length} runs, ${result.screenshots.length} screenshots in ${output}` : `FAIL voice e2e: ${result.error?.split("\n")[0]}`);
  process.exit(result.status === "pass" ? 0 : 1);
}
