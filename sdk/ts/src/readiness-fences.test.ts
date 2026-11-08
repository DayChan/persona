import { expect, test } from 'bun:test';
import { componentPage, PageBridge } from './page-bridge.ts';
import { createElement, View, Section, Button, callback } from './components.ts';
import { useLoadEffect, useState } from './hooks.ts';
import { mount, type Renderer, type Snapshot } from './renderer.ts';
import type { Hub } from './index.ts';
const hub = {notify() {}} as unknown as Hub;

test('more than 512 asynchronous finite loads do not consume a lifetime loop budget', async () => {
  let completed = 0;
  function Root() {
    const [n, set] = useState(0);
    useLoadEffect(async signal => {
      if (n === 520) return;
      await new Promise(resolve => setTimeout(resolve, 1));
      if (!signal.aborted) {completed++; set(n + 1);}
    }, [n]);
    return createElement(View, {key: 'root', title: String(n)});
  }
  const renderer = mount(createElement(Root, {key: 'page'}), {authorize: () => true, limits: {pendingEffects: 1}});
  try {
    expect((await renderer.settle(new AbortController().signal, 1000)).view.title).toBe('520');
    expect(completed).toBe(520); expect(renderer.suspended).toBe(false);
  } finally {renderer.dispose();}
}, 2000);

test('snapshot event guards finite self-invalidation before awaiting an asynchronous callback', async () => {
  function Root() {
    const [n, set] = useState(0);
    useLoadEffect(() => {if (n) set(n + 1);}, [n]);
    return createElement(View, {key: 'root', children: createElement(Section, {key: 'main', children:
      createElement(Button, {key: 'write', title: 'Write', action: callback(async (_values, {signal}) => {
        set(1);
        await new Promise<void>((resolve, reject) => {
          const timer = setTimeout(resolve, 50);
          signal.addEventListener('abort', () => {clearTimeout(timer); reject(new Error('canceled'));}, {once: true});
        });
      })})})});
  }
  const bridge = new PageBridge({hot: componentPage(() => createElement(Root, {key: 'hot'}), {snapshotTimeoutMs: 25})}, hub);
  try {
    const first = await bridge.call('mount', {session: 'loop', transport: 'snapshot', params: {contribution: 'hot'}}) as Snapshot;
    const started = performance.now();
    await expect(bridge.call('event', {session: 'loop', ticket: 'host', interaction: {generation: first.generation, version: first.version, eventId: 'once', key: 'write', actionId: (first.view.sections[0].items[0] as any).action.id, values: {}}})).rejects.toThrow(/work limit|timeout/);
    expect(performance.now() - started).toBeLessThan(100);
  } finally {bridge.close();}
});

test('recovery that synchronously suspends itself cannot return success', async () => {
  for (const transport of ['stream', 'snapshot']) {
    let renderer!: Renderer;
    const page = componentPage(() => createElement(View, {key: 'root', title: 'cached'}));
    const wrapped = {...page, createRenderer: (host: Parameters<typeof page.createRenderer>[0]) => {
      renderer = page.createRenderer({...host, onRender: snapshot => {host.onRender?.(snapshot); if (snapshot.version === 2) renderer.suspend();}});
      return renderer;
    }};
    const bridge = new PageBridge({hot: wrapped}, hub);
    try {
      await bridge.call('mount', {session: 'recover', transport, params: {contribution: 'hot'}});
      await bridge.call('suspend', {session: 'recover'});
      await expect(bridge.call('resume', {session: 'recover'})).rejects.toThrow('inactive page recovery');
      expect(renderer.suspended).toBe(true);
    } finally {bridge.close();}
  }
});

test('synchronous readiness flush rechecks abort and the visibility epoch after effects', async () => {
  for (const mode of ['abort', 'suspend', 'suspend-resume']) {
    let renderer!: Renderer, set!: (n: number) => void, once = false;
    const controller = new AbortController();
    function Root() {
      const [n, update] = useState(0); set = update;
      useLoadEffect(() => {
        if (n && !once) {
          once = true;
          if (mode === 'abort') controller.abort();
          else {renderer.suspend(); if (mode === 'suspend-resume') renderer.resume();}
        }
      }, [n]);
      return createElement(View, {key: 'root', title: String(n)});
    }
    const queue: (() => void)[] = [];
    renderer = mount(createElement(Root, {key: 'page'}), {authorize: () => true, schedule: task => queue.push(task)});
    try {
      set(1);
      await expect(renderer.settle(controller.signal, 25)).rejects.toThrow(mode === 'abort' ? 'canceled' : 'inactive');
      expect(renderer.suspended).toBe(mode !== 'suspend-resume');
      for (const task of queue) task();
      expect(renderer.suspended).toBe(mode !== 'suspend-resume');
    } finally {renderer.dispose();}
  }
});
