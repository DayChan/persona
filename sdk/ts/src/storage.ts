import type { Hub } from "./index.ts";
import type * as wire from "./wire.ts";

/** Installation-scoped business bytes. This client grants nothing: every key,
 * queue and blob is checked by the manager against the actual declaration.
 * Keep the request ID and payload when a mutation's outcome is unknown. */
export class ExtensionStorage {
  constructor(private readonly hub: Pick<Hub, "call">) {}

  request(): Promise<wire.StorageRequestResult> {
    return this.hub.call("storage.request", {});
  }
  get(key: string): Promise<wire.StorageEntry> {
    return this.hub.call("storage.get", { key });
  }
  list(params: wire.StorageListParams = {}): Promise<wire.StorageListResult> {
    return this.hub.call("storage.list", params);
  }
  transaction(params: wire.StorageTransactionParams): Promise<wire.StorageTransactionResult> {
    return this.hub.call("storage.transaction", params);
  }
  queueList(params: wire.StorageQueueListParams): Promise<wire.StorageQueueListResult> {
    return this.hub.call("storage.queue.list", params);
  }
  enqueue(params: wire.StorageEnqueueParams): Promise<wire.StorageJobResult> {
    return this.hub.call("storage.queue.enqueue", params);
  }
  claim(params: wire.StorageClaimParams): Promise<wire.StorageClaimResult> {
    return this.hub.call("storage.queue.claim", params);
  }
  ack(params: wire.StorageSettleParams): Promise<wire.StorageJobResult> {
    return this.hub.call("storage.queue.ack", params);
  }
  forget(params: wire.StorageSettleParams): Promise<wire.StorageDeleteResult> {
    return this.hub.call("storage.queue.forget", params);
  }
  retry(params: wire.StorageRetryParams): Promise<wire.StorageJobResult> {
    return this.hub.call("storage.queue.retry", params);
  }
  createBlob(params: wire.StorageBlobCreateParams): Promise<wire.StorageBlobCreateResult> {
    return this.hub.call("storage.blob.create", params);
  }
  writeBlob(params: wire.StorageBlobWriteParams): Promise<wire.StorageBlobWriteResult> {
    return this.hub.call("storage.blob.write", params);
  }
  finishBlob(params: wire.StorageBlobMutateParams): Promise<wire.StorageBlobFinishResult> {
    return this.hub.call("storage.blob.finish", params);
  }
  readBlob(params: wire.StorageBlobReadParams): Promise<wire.StorageBlobReadResult> {
    return this.hub.call("storage.blob.read", params);
  }
  deleteBlob(params: wire.StorageBlobMutateParams): Promise<wire.StorageDeleteResult> {
    return this.hub.call("storage.blob.delete", params);
  }
}

/** Canonical base64 bytes for a bounded value or chunk, not filesystem access. */
export function storageBytes(bytes: Uint8Array): string {
  if (bytes.length > 65536) throw new RangeError("storage values and chunks are at most 64 KiB");
  let binary = "";
  for (let at = 0; at < bytes.length; at += 8192) binary += String.fromCharCode(...bytes.subarray(at, at + 8192));
  return btoa(binary);
}

export function storageValue(value: string): Uint8Array {
  if (value.length > 87384) throw new RangeError("storage value exceeds the encoded bound");
  const binary = atob(value), bytes = Uint8Array.from(binary, char => char.charCodeAt(0));
  if (bytes.length > 65536 || storageBytes(bytes) !== value) throw new TypeError("noncanonical storage base64");
  return bytes;
}
