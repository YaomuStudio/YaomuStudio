# Desktop event-stream connection notification

Additive bridge contract, schema version 1 (2026-11-08). Core business events remain defined in OpenAPI DomainEvent/TaskProgressEvent; this is a transport notification, not a business event, queue or cursor.

## API

```ts
window.yaomu.api.subscribeEvents(
  onEvent: (event: unknown) => void,
  onError?: (error: { code: string; message: string; retryable: boolean }) => void,
  options?: { afterSeq?: number; onConnected?: () => void },
): () => void;
```

`onConnected` is optional and called after the Main-owned SSE request receives a successful response with a body, on both the initial connection and an automatic transport reconnect. It does not assert that historical replay has drained or that models are ready. It may be called again after an explicit reattach. Clients without the callback preserve existing behavior.

Preload listens before `yaomu:events:attach`, does not serialize the callback across IPC, and unsubscribes the connection listener together with event/error listeners. A disposed subscription cannot receive callbacks. Invoke the callback only for the exact notification payload below; user callback errors must not cause a new transport retry.

## Main → preload signal

- Channel: `yaomu:api:events-connected`.
- Payload: `{ "schemaVersion": 1 }`, no additional fields.
- Only currently subscribed, live, authorized app contents receive it. Existing top-level sender checks on attach/detach and navigation/destruction cleanup remain unchanged.
- No port, URL, token, path, raw error, task, epoch or sequence number is exposed. An obsolete/stopped stream must not publish a late connection signal or late event/error callback.
- This channel is never a Core `DomainEvent` and must not be inserted into the durable or seq=0 business event stream.

## Consumer behavior

On connection notification, refresh the relevant read-only task snapshots once (coalescing concurrent notifications). Do not reattach just because the refresh succeeded: that would form a connection→read→reattach loop. Do not replay business writes, invent a percentage, alter task status or advance the durable cursor from this signal. Identity/status/version read guards must protect newer events against stale snapshot responses.

Lost transient progress is recovered from Core snapshots; only seq>0 DomainEvents or a real snapshot high-watermark may advance the business cursor. Cursor expiry still uses the existing explicit snapshot/reattach recovery path. Runtime readiness generation is independent.

## Verification

Cover initial success, reconnect without a new business event, invalid notification, no callback on failed/stopped streams, idempotent unsubscribe, and unauthorized contents. Verify seq=0 progress does not alter after_seq on reconnect. A running percentage of 100 remains RUNNING until Core terminal evidence arrives.
