// Whether a page may try the native UI stream, without loading it: the stream (extension-ui-stream.ts and the JSON
// schemas it validates against, about 110 kB) is its own chunk, loaded when a native extension page first draws.
export const UI_STREAM_FEATURE = "extension_ui_stream_v1";
/** Both the backend hello and the client's explicit stream context are required. */
export const canTryUIStream = (features: readonly string[]) => features.includes(UI_STREAM_FEATURE);
