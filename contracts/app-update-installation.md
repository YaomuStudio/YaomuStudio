# Application update installation guard

Status: implemented and control-plane verified, 2026-11-09; evidence is tracked in the implementation record. Actual hidden Electron/Main/preload/Core uses a fake updater; this is neither authorization to download/install a real update nor evidence of Windows upgrade, signature or rollback readiness.

## Ownership and conservative waiting

Core is the only business writer. Main owns downloaded-version consent and installation progress, never a second business queue. Calling Install explicitly consents to the currently downloaded version on the current authenticated Core generation. Ordinary quit never installs implicitly (`electron-updater` 6.x `autoInstallOnAppQuit=false`). Existing check/download preferences remain unchanged.

When work exists, installation waits without pausing, canceling or altering it. New work may prolong waiting. Cancellation is allowed while waiting; after atomic preparation starts it is not offered because Runner closure is irreversible. Pending consent is memory-only and is discarded on quit, downloaded-version change, Core identity/generation change or communication failure. It is not resumed after application restart.

Explicit blocking sets:

- Run: DRAFT, QUEUED, RUNNING, PAUSE_REQUESTED, PAUSED, WAITING_REVIEW.
- Task: QUEUED, WAITING_RESOURCE, RUNNING, PAUSE_REQUESTED, PAUSED, CANCEL_REQUESTED. FAILED/INTERRUPTED historical tasks alone do not block; do not use `!TaskStatus.Terminal()`.
- Attempt: PENDING, RUNNING, COMMITTING.
- Installation: planning, downloading, verifying, installing, plus any still-owned installation worker even if its persisted record has finished/canceled.
- In-memory scheduler execution/reservations, executor isolation/unknown ownership, unfinished Runner bootstrap or unconfirmed bootstrap cleanup also block. An unavailable executor is not proof of clean shutdown; retained lease/cleanup errors remain latched.

A waiting/paused Run must be resolved through existing review, resume and task actions before this conservative guard can become idle. There is no direct cancel-Run endpoint in the current API; this guard does not invent one, automatically cancel work, force-stop a queue or reinterpret restart recovery.

## Core preparation endpoint

`POST /api/v1/system/update-preparation` is authenticated with the existing private instance token and Host/Origin rules. Main calls it through its captured private connection; it is not added to Renderer generic API allowlists or the third-party API. Request header `request_id` is required. No durable Idempotency-Key receipt is used: the operation is instance-local and returns current preparation state, never an old receipt from another Core epoch.

Request (exact keys):

```json
{"mode":"check","operation_id":"opaque-operation","workspace_id":"workspace-id","instance_id":"instance-id","server_epoch":1}
```

`mode` is `check` or `prepare`. Check is read-only and may return idle, never prepared; Main polls check while cancelable, switches to noncancelable preparing **before** sending prepare. Prepare always rechecks atomically and never trusts the prior idle observation. This avoids a canceled in-flight readiness request unexpectedly closing Runners. IDs are nonempty strings of at most 200 characters; epoch is a positive safe integer. Identity must exactly match this Core. Invalid shape gives INVALID_REQUEST; a well-formed different identity gives REVISION_CONFLICT. `operation_id` identifies one Main installation attempt.

Result (exact keys, HTTP 200):

```json
{"schema_version":1,"operation_id":"opaque-operation","workspace_id":"workspace-id","instance_id":"instance-id","server_epoch":1,"state":"blocked","reason":"active_work"}
```

- `state=idle`: reason is null, returned only by check. This is a transient observation with no permit or side effect.
- `state=blocked`: reason is `active_work`, `request_in_flight`, `executor_starting`, `executor_unconfirmed`, `preparing` or `restart_required`. No prepared proof is implied.
- `state=prepared`: reason is null. All ordinary requests and business writes are sealed, scheduler ownership has settled, Runner bootstrap has settled and all owned execution resources/leases have confirmed cleanup. This is permission to stop this Core, **not** permission for Renderer to invoke an installer.
- Query/storage/infrastructure failure returns the existing safe 503 envelope, never a fake zero count. Error details, paths, tokens and media text are excluded.

## Atomicity and failure behavior

1. A request-admission owner covers the entire ordinary API handler, including file IO outside transactions and GET project side effects. Health, pure system handshake, SSE and update-preparation are the only exclusions. Preparation fails fast with request_in_flight if any ordinary handler is active; it does not cancel/wait indefinitely for that handler. While exclusive admission is held, new ordinary requests receive CORE_NOT_READY with a safe update-preparing message.
2. Store serializes normal DoTx against `SealIfIdle`: obtain exclusive write admission, validate active database sets and in-memory owners in one transaction, commit the read/check and seal **before** releasing write admission. An already-entered creation/claim either finishes before the check and is observed, or loses admission after seal. An unsuccessful check reopens admission with no business row/event/receipt changes.
3. DoTx sealing covers all creation, claim and background installation writes. Event-retention auto-commit writes must also use guarded DoTx. No active completion is interrupted: sealing requires both persisted active sets and in-memory owners to be empty. It must not reject a downstream creation inside a still-active success transaction and roll back a valid result.
4. Only after seal succeeds, stop the empty scheduler and close idle execution resources. Concurrent/repeated cleanup shares one completion and retains its error; a second Close must not falsely report success while the first is unfinished or failed. Bootstrap cleanup/lease-acquisition uncertainty must be retained even if readiness later reads unavailable/closed.
5. Preparation control-plane waiting has a 15-second budget; Main uses a 20-second prepare transport budget and a 5-second check budget. Once cleanup starts, its ownership is independent of client disconnect; an unfinished cleanup stays tracked after a bounded response. Once resource cleanup begins, the instance remains sealed on timeout, transport loss or error. There is no timer-based unseal, cancellation endpoint or reusable cross-restart permit. Recovery is explicit application restart. Bounded waiting does not abandon ownership or treat an unfinished goroutine/process as clean.
6. Same operation on a prepared instance may read the same in-memory proof; another operation returns restart_required. A new Core always requires fresh identity, preparation and explicit user consent.

Main accepts prepared only for the captured operation/version/workspace/instance/epoch and unchanged runtime generation/connection. It then synchronously starts the existing owner’s stop, which disables retry/restart and revokes connection, waits for confirmed owned Core exit, checks consent/version ownership again, and invokes `quitAndInstall(false,true)` **once**. Any failure or stale reply gives zero installer calls. Runner proof precedes Core termination; Core exit code or parent PID death alone is insufficient. Ordinary quit retains its existing fallback but auto-install remains disabled.

## Host installation-state bridge

Additive `window.yaomu.update` members:

```ts
getInstallState(): Promise<UpdateInstallResult>;
install(): Promise<UpdateInstallResult>; // start/join current consent, return current snapshot
cancelInstall(): Promise<UpdateInstallResult>; // only waiting can cancel
onInstallState(cb: (state: UpdateInstallSnapshot) => void): () => void;
```

Invoke result is `{ok:true,data:snapshot}` or `{ok:false,error:{code:"UPDATE_INSTALL_UNAVAILABLE"|"UPDATE_INSTALL_ACCESS_DENIED",message:string}}` with fixed safe messages. Main reuses DesktopAuthorization before every side effect/read. Broadcast only to registered, currently trusted live contents; preload listens before attaching and removes all own listeners on unsubscribe. Transport channels are `update:install-state`, `update:get-install-state`, `update:cancel-install`, `update:install-state:attach` and `update:install-state:detach`; existing `update:install` is retained. Payloads must contain no connection credentials, Core identity strings or filesystem paths.

Snapshot (camelCase, exact keys):

```json
{"schemaVersion":1,"revision":1,"phase":"idle","reason":null,"version":null,"canCancel":false}
```

- `revision`: positive safe integer, monotonically increasing within this host.
- `phase`: idle, waiting, preparing, stopping, installing, blocked.
- `reason`: null or active_work, request_in_flight, executor_starting, runtime_unavailable, runtime_changed, executor_unconfirmed, prepare_failed, stop_unconfirmed, restart_required, update_unavailable, install_failed.
- `version`: null in idle or when no downloaded version exists; otherwise nonempty, bounded to 200 characters. It is an updater-provided display version, not executable input.
- `canCancel`: true only in waiting. Preparing/stopping/installing are not cancelable. Cancel returns idle, clears consent and pending timer, and does not mutate Core tasks.

UI keeps the existing download phase separate from install state. Unknown/malformed or older snapshots cannot advance installation. Finite reasons map to registered English/Chinese messages; do not display arbitrary exception.message. Waiting shows a cancel action; preparing/stopping disables repeated install/check/download actions. Reload hydrates current host state. Pending consent does not become a persistent UI queue.

## Required evidence

- Red signal: existing authenticated install calls fake updater while active; auto-quit bypass also modeled. No real updater network/install during tests.
- Core: same-transaction creation/claim vs seal barriers, HTTP file-IO/GET admission, all blocking sets (including WAITING_RESOURCE/history distinction), failed DB read, installation worker tail, empty success, bootstrap/lease uncertainty, shared cleanup error, stale identity/retry/restart. No failed preparation business writes/events.
- Desktop: identity/version changes, duplicate calls, waiting cancellation, ordinary quit, stop timeout/late reply, zero install on every failure, exactly one install only after prepared and actual owned stop. Authorization and subscription cleanup negative tests.
- Actual hidden isolated Electron + Core + fake updater: waiting UI, cancellation, empty prepared path, safe failure, screenshot/console/request inspection. Synthetic workload only; no real update or model/Windows acceptance claim.
