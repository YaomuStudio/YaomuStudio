# Shared release installations and slot references

Status: implemented with controlled local installation, reference and migration/recovery verification on 2026-11-09; evidence is owned by the implementation record. This is installation/control-plane behavior, not a claim of real bundle-path binding, signed manifests or offline inference.

## Ownership and identity

Core remains the sole business writer. One validated physical installation may cover several independently configured capability slots. Do not create a Done record per slot or infer sharing from a display name/release ID.

- Coverage comes from one admitted catalog release declaration (`slots`), is sorted/unique, contains the requested slot and uses known capability keys. Legacy internal static providers with no coverage field resolve only the requested slot; the real catalog preserves its explicit declaration.
- Physical `storage_key` is lowercase SHA-256 of the canonical complete ReleaseRef (`release_id`, `version`, `digest`, optional `channel`), matching existing exact-lock equality. New files live under `models/releases/<storage_key>/`. Different digests/versions/channels cannot overwrite one another. Identity is not just release_id.
- Persist the immutable covered slots and validated file manifest with each new installation. Same identity with different coverage or file content manifest is a conflict, not an opportunity to widen scope. Mirror URLs may differ only while relative paths, sizes and SHA-256 values are identical and the new plan remains within the admitted URL policy.
- Installation remains an actual installation operation/verified physical record. At most one nonterminal or Done record owns a new storage_key. Failed/canceled attempts may remain as history; their still-running worker tails retain filesystem ownership until cleanup ends.
- Slot-to-installation references identify current and retained versions. A slot has at most one current reference; one installation may be referenced by several slots. Current activation is not inferred from random ID or latest physical-record creation time.

## API additions and compatibility

Existing endpoints are retained. Wire fields are snake_case.

### InstallationPlan

Add `covered_slots: CapabilitySlotKey[]` and optional `reuse_installation_id: string`.

- New plans always include nonempty covered_slots, including singleton plans. Freeze coverage, exact release, validated file list, permissions/license/size facts and reuse target into plan_hash. The requested `slot` remains an entrypoint, not the whole consent scope.
- UI shows every covered slot before confirmation and explains that successful installation selects this version for those slots. No implicit loop submitting one installation per slot.
- A pre-upgrade stored plan lacking frozen coverage must be re-planned (`PREFLIGHT_STALE`), not silently expanded using today's catalog. Old Core responses lacking coverage may only be presented with their existing single-slot semantics.
- Plan sizes describe one physical installation, never multiplied by covered-slot count. Joining an active installation points at its single operation/progress and starts no second transfer. A verified Done reuse has zero additional download/staging and reports reusable bytes; file validation failure requires re-planning/repair, not an unapproved replacement download.

### Installation list/detail/write responses

Add these read-only projections:

- `covered_slots`: immutable originally consented coverage (legacy records expose their original singleton).
- `bound_slots`: slots still referencing this physical record, including retained history.
- `current_slots`: slots for which this Done record is currently selected; empty for a pending/failed/canceled record.

Entity fields and their bound/current reference projections must come from one read snapshot; an installing record must not be combined with current references from a later completion. New list/detail responses always provide arrays, including empty arrays. Absence is reserved for old Core/old response compatibility; empty never falls back to `[slot]`. `slot` and `plan_hash` retain the physical operation's original initiating slot/plan even when another request joins or reuses it; the new request's plan hash is bound by its own idempotency receipt. Detaching the initiating slot does not rewrite this history. No storage path, private manifest URL or storage_key is exposed through these additions. Existing id/progress/phase remain one physical operation's facts.

`POST /installations` still returns 202. Concurrent plans from two covered slots join the same active installation ID. If the exact verified installation already exists, reuse it after final-file verification and explicitly activate the consented references; do not download again or append a fake Done row. A stale reuse target fails safely rather than silently replacing it. Durable idempotency replay remains its original response snapshot, not an instruction to re-activate or delete again; UI re-reads list/capabilities after writes.

## Activation, concurrency and cancellation

1. Establish references for the frozen covered slots when an installation is created, but leave old current references untouched.
2. Only after all files pass hash/size checks and are committed may one Core transaction mark Done and select all covered slots together, with existing completion events. Failure/cancellation must activate none of the new references and must retain previously active versions.
3. Equal storage_key requests join one owner. Different releases with overlapping covered slots cannot install concurrently; return the existing resource-busy conflict rather than letting late older completion overwrite a newer selection. Unrelated installations may proceed independently.
4. Verify final-file hashes outside SQLite transactions and the global business write mutex; retain directory lifecycle exclusion, then re-read the exact target/scope/current references and relevant active-work guards in the short commit transaction. Idempotent replay must not redo hash/file side effects. Serialize installation lifecycle decisions (create/join/delete/rollback/model cleanup) through one Core ownership boundary. Do not hold the short worker-map mutex while waiting for SQLite; update-preparation reads that map inside its transaction. A canceled worker tail still prevents a new writer/deletion on the same physical root.
5. `POST /installations/{id}/cancel` cancels that physical operation for all its bound/covered slots, not a single slot's view. UI labels this shared effect; prior current versions stay selected. Cancellation acknowledgement is not worker-exit proof.
6. Completion/notification/progress remain keyed by one installation ID. Do not publish duplicate physical progress or multiply disk accounting by references.

## Per-slot deletion, rollback and frozen consumers

The existing capability-slot endpoints keep per-slot scope:

- `DELETE /capabilities/{slot}/installation` detaches that slot's current and retained installation references/history. It does not detach another slot. Remove a physical directory only when no other Done/current/retained reference needs it and no relevant writer/executable frozen reference is active. Remove metadata/bytes once, not per alias.
- Delete and rollback conservatively reject while any Task in the workspace is active, preserving the existing deletion guard. Additionally protect the requested slot's exact model locks in nonterminal Runs that can still create work, including dependency locks. This is not a per-slot active-Task filter. An explicit deletion after work is no longer active retains existing history semantics: retry of a historical FAILED/INTERRUPTED task must fail its missing-lock check, never substitute another version.
- `POST /capabilities/{slot}/rollback` selects the previous **different exact release** retained by that slot, after checking available verified files. It changes only this slot and creates no Done row. Reject rollback while relevant work/installation is active. Other slots keep their current release; existing frozen Task/Run locks are not rewritten.
- Legacy rollback may use its originally persisted plan's manifest to verify files. If evidence needed for verification is absent, return a readable conflict rather than claiming the old directory is valid.
- Freeze reads current slot references; dispatch checks exact retained references plus existing A2 evidence (Done, verified_files>0, complete ReleaseRef, real probe flag preserved). A deleted slot is not re-admitted solely because covered_slots still names it.
- Deletion file effects are derived only from the executed transaction. An idempotent replay must not repeat removal and delete a newly reinstalled directory. Keep lifecycle exclusion until file removal finishes.
- Model storage cleanup recognizes the shared release namespace and retained references, never deletes the whole `models/releases` directory because it is not a slot. It joins the same lifecycle protection and blocks unfinished installer tails. Disk usage counts the actual physical tree once.

## Installation events

Installation events remain separate from Task events and carry no task_id. Physical progress and completion are keyed by one installation_id, not emitted once per covered slot.

- `installation.created`, `installation.phase_changed`, `installation.progress`, `installation.downloaded`, `installation.completed` and `installation.failed` retain `data.installation_id` and the original initiating `data.slot`; new `data.slots` gives the frozen complete coverage. It is not a current-reference projection.
- `installation.reused` is a durable event committed with verified Done reactivation. Its data is `{installation_id, slots, release_id}`; it has no initiating slot field, does not fabricate another completed operation, and is not emitted again on idempotent replay.
- `installation.deleted` carries only `{slot}` because one requested slot can detach several retained records; it does not assert that shared physical bytes were deleted.
- `installation.rollback` carries `{installation_id, slot, release_id}` for the selected retained entity and requested slot. Other slot references are unchanged.

Only installation.progress is discardable here; existing durable-event ordering remains unchanged. Re-read list/detail/capabilities to recover current and retained references; neither event coverage nor a write receipt is the latest reference state.

## Schema 10 and legacy data

Add to `installations`: nullable `storage_key`, `covered_slots_json`, `manifest_json`. Null storage_key denotes the old per-slot directory layout; no inferred shared identity is fabricated for legacy records.

Add `installation_slot_bindings`:

- `installation_id` references installations (cascade when the record is intentionally removed).
- `slot` is a known capability key.
- `is_current` is boolean; `activated_at` records selection order for current/retained Done versions.
- Primary key `(installation_id, slot)`; a partial unique index permits only one current binding per slot.
- A partial unique index on non-null storage_key covers planning/downloading/verifying/installing/done, preventing duplicate active/verified physical owners.

Migration backfills only each legacy row's original slot. For each slot, preserve the previous current selection (`created_at DESC, id ASC`, first Done); other rows remain retained history. Do not move/delete/deduplicate legacy directories, widen coverage, recompute a fake release digest or set probe_passed=true. Legacy API tests/records still have singleton semantics.

Production continues the existing migration-before-ready, consistent backup, dirty-state rejection and newer-schema downgrade refusal. A schema-10 Down must not erase new storage/reference facts that an older Core cannot represent: reject if new-layout rows or selection changes would be lost. For migration-only legacy data, prove Down/re-Up preserves rows and original selection. After using new shared state, recovery to the old executable uses the verified pre-migration backup, with the explicit loss-of-new-data caveat; no automatic restore. New shared files are not silently moved/deleted during backup restoration.

## Validation matrix

| Condition | Behavior |
|---|---|
| Unknown/duplicate coverage or requested slot not covered | Reject incomplete release/plan |
| Missing legacy plan coverage or changed hash/scope | PREFLIGHT_STALE; no widened activation |
| Two matching plans race | Same ID, one physical writer/transfer, one set of progress facts |
| Overlapping slots, different releases | Resource-busy conflict |
| Any verification/commit/cancel failure | No new current reference; old current version retained |
| Done reuse missing/corrupt files or stale target | Readable conflict/verification failure; no fake Done or implicit download |
| Delete one alias while another retains the release | Other alias still valid and physical bytes retained |
| Active Task/Run/installer tail references target | Reject delete/rollback/unsafe cleanup |
| Rollback one slot | Only that current pointer changes; frozen locks unchanged |
| Same release_id, different digest | Distinct new physical roots |
| New shared state followed by unsafe Down | Refuse; use verified backup recovery path explicitly |

## Required verification

Use the real downloader against a controlled loopback source with fixed bytes, not prefilled Done records or real model downloads. Assert transfer count, SHA-256 of final files, one physical directory/usage count, simultaneous TTS and voice-clone admission and real clone preflight without repeated installation. Cover concurrent join, worker-tail cancellation, overlapping upgrade rejection, failed upgrade, per-slot deletion/rollback, retained old locks, idempotent replay, model cleanup and restart recovery.

Run schema migration/backup/Down refusal and recovery tests with actual legacy rows and directories; repeat relevant Go race/contract tests. UI tests cover Models and Onboarding coverage confirmation, shared progress/cancel scope and deleted-slot non-reappearance. Actual isolated Electron/Core installation verifies displayed consent and Core-derived readiness. Real FlashCLI bundle binding, signed release pipelines, model quality and Windows/WSL remain separate gates.
