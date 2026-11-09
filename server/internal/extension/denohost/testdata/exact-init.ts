import { withExactInit, type Program } from '../../../../../sdk/ts/src/index.ts';

let observed: Record<string, unknown> = {};
export default {
  id: 'example-exact-init', version: '1',
  init: withExactInit(function(init, _hub, call) {
    if (!call?.params) throw new Error('exact initialization metadata missing');
    const config = call.params.field('config');
    const parsed = init.config as Record<string, unknown>;
    observed = {
      raw_config: config?.raw,
      exact_large: config?.field('large')?.raw,
      exact_exponent: config?.field('exponent')?.raw,
      exact_null: config?.field('nil')?.raw,
      parsed_exponent: parsed.exponent,
      parsed_n: parsed.n,
      same_receiver: this.id === 'example-exact-init',
      metadata_frozen: Object.isFrozen(call),
    };
  }),
  namespaces: { fixture: () => observed },
} satisfies Program;
