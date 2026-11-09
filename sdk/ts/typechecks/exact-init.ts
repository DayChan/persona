import { withExactInit, type Handler, type Program } from "../src/index.ts";

const exactInit = withExactInit(function(init, hub, call) {
  void init.config;
  void hub;
  void this.id;
  if (call) {
    void call.params?.field("config")?.raw;
    // @ts-expect-error Original initialization metadata is readonly.
    call.rawParams = "fixture";
    // @ts-expect-error The exact params projection is readonly.
    call.params = undefined;
  }
});
const program: Program = { id: "example", version: "1", init: exactInit };
void program;
// @ts-expect-error An initializer is not a namespace handler.
const wrongNamespace: Handler = exactInit;
void wrongNamespace;
