import { componentPage, type Program } from '../../../../../sdk/ts/src/index.ts';
import { createElement, View, Section, Text, Button, callback } from '../../../../../sdk/ts/src/components.ts';
import { useLoadEffect, useState } from '../../../../../sdk/ts/src/hooks.ts';
let callbacks = 0, canceled = 0, callbackActive = 0;
function Root() {
  const [title, setTitle] = useState('loading');
  useLoadEffect(async signal => {
    const response = await fetch('https://example.com/topics.json', {signal});
    const value = await response.json();
    if (!signal.aborted) setTitle(value.id);
  }, []);
  return createElement(View, {key: 'root', title, children: createElement(Section, {key: 'main', children: [
    createElement(Text, {key: 'topic', text: title}),
    createElement(Button, {key: 'write', title: 'Write', action: callback(async (_values, {signal}) => {
      callbacks++; callbackActive++;
      try {await new Promise<void>((_resolve, reject) => signal.addEventListener('abort', () => {canceled++; reject(new Error('canceled'));}, {once: true}));}
      finally {callbackActive--;}
    })}),
  ]})});
}
export default {id: 'example-readiness', version: '1', ui: {hot: componentPage(() => createElement(Root, {key: 'hot'}), {snapshotTimeoutMs: 1000})},
  namespaces: {test: () => ({callbacks, canceled, callbackActive})},
} satisfies Program;
