# Packaged WSL Runner resources

Status: application-layer implementation locally verified on 2026-11-09, and actual Windows/WSL acceptance completed (isolated 92fa7d0 package in a Chinese/space path, source checkout moved away, bundled hello plus same-startup Core, real read/write transcode with PCM equality and normal exit, missing-manifest negative). Owned by the implementation record. Real model inference, release signing and strict offline execution remain separate gates. This contract covers application Runner resources and launch/verification boundaries, not a model bundle, signature or offline-inference guarantee.

## Resource ownership and layout

Reuse the existing asar-external `resources/core/runner_src/yaomustudio_runner` tree staged by `scripts/build-core.sh`. Add `resources/core/runner-manifest.json`; do not create another Runner source copy, install into the distribution, or rely on a repository checkout. Existing host launchers/resources remain compatibility artifacts, not proof of the Windows WSL route.

The manifest is a deterministic build artifact from the actual staged files, validated against `packaged-runner-resources.schema.json`:

- `kind = "yaomu_runner_resources"`, `schema_version = 1`.
- `runner_version`: the agreed version from `runner/pyproject.toml` and the package `__version__`; staging rejects disagreement. Do not execute imported package code just to obtain metadata.
- `requires_python = ">=3.11"`: the current declared runtime floor, not a claim that an interpreter was tested.
- `files`: a nonempty, path-sorted list of `{path, size, sha256}`. Paths are relative to runner_src, inside yaomustudio_runner, and cannot be absolute, contain drive/ADS syntax, backslashes or dot traversal. Hashes are lowercase SHA-256 of actual bytes; sizes are nonnegative safe integers. Paths must be unique including case-folded Windows comparison.
- Entry files `yaomustudio_runner/__init__.py`, `__main__.py` and `main.py` must exist in the list and on disk. The module entry is fixed by code, never a manifest-supplied command.

No URLs, credentials, machine paths, model weights or executable command strings belong in this manifest. Filesystem enumeration must match the manifest. Deployed source trees and manifests MUST reject Python bytecode caches (`__pycache__` path segments and `.pyc`/`.pyo` suffixes, case-insensitive); they are not ignorable input because `-B` disables writes but still permits cached code execution. Build staging removes caches before manifest generation; the generator refuses any remaining cache. Reject links/special files and any resolved path escaping the owned source root. A missing/stale/corrupt manifest is not legacy success.

The consumer bounds manifest reads to 1 MiB and uses bounded-memory file hashing. Hash/version checks prove local package assembly consistency only: an attacker replacing both files and manifest, or the app/interpreter/image, is outside this integrity claim. OS signing/release trust remains a separate gate. A consumer may compute the manifest file's SHA-256 for safe diagnostic identity without adding a self-referential manifest field.

## Main preparation and Core handoff

1. Main owns `app.isPackaged` and `process.resourcesPath`. On the default packaged Windows route it asynchronously validates the owned resource tree before preparing the Runner command; cancellation cannot later start a Core/Runner from stale preparation.
2. Pass a narrow internal verified descriptor to the existing pure WSL environment projection. It contains the native source directory, Runner version and manifest digest; it is not renderer input or business state.
3. Default packaged source resolution never consults app.getAppPath's parent/repoRoot or development source paths. Existing development source resolution remains available in non-packaged mode.
4. Preserve the existing explicitly configured `YAOMU_WSL_RUNNER_COMMAND`/JSON `YAOMU_WSL_RUNNER_ARGS` compatibility route. Label it external, do not claim bundled-source verification for it, and do not silently select it as a fallback after bundled validation fails. Default packaged mode without an explicit command requires valid bundled resources.
5. Continue the configured `yaomu` distribution (`YAOMU_WSL_DISTRO` override), `YAOMU_WSL_EXE` and `YAOMU_WSL_PYTHON` conventions. Use argument arrays, no shell interpolation. Do not install/start another distribution by guessing, change the default distribution, or invoke global WSL shutdown.
6. The packaged Python module command uses `-B -P -s -m yaomustudio_runner`, with PYTHONPATH replaced by only the verified mapped source directory and bytecode writes disabled. `-P` (Python 3.11+) prevents cwd from shadowing the bundle; `-s` excludes user site packages. The interpreter/image is still an explicitly prepared runtime, not authenticated by the source manifest.
7. Preserve the existing explicit FlashCLI/media configuration and confirmed argv. No guessed model switches, package synchronization, fallback cloud call, implicit model download, host Runner injection or mock READY.
8. WSL probe or Runner-resource failures keep Core available for editing/diagnostics, as required by architecture §3; do not throw the entire Core preparation merely because the Runner resource is invalid. Keep the Runner unavailable, emit a fixed safe diagnostic identifying resource repair/reinstall and app restart, and let strict smoke fail its Runner check. Cancellation/unconfirmed owned cleanup still aborts preparation. Do not redefine HTTP ready as Runner ready. No new generic renderer IPC or I03 fields are needed.

Windows drive paths, spaces and non-ASCII installation roots must retain their identity. Existing Core request/result path translation remains authoritative for business files; this change does not add another bridge. UNC/custom automount layouts and actual Windows file/exit behavior require platform evidence rather than assumed support.

## Strict packaged smoke

- Resolve Core, source and manifest resources for **every** selected application, including `--app`. A missing or ambiguous artifact is failure, not a skipped check with default success.
- Run with isolated data/userData and a neutral cwd. Remove known launch-source/development/mock overrides case-insensitively, including Core/host Runner/WSL Runner command+args and dev URL; retain only deliberately selected image/interpreter configuration. Never dump the resulting environment or authentication data.
- On Windows, validate packaged files and use the same production source/WSL argv resolver. Require a real WSL protocol hello with matching Runner version/protocol and expected adapters, plus evidence that the actual app's current Core accepted its model-side Runner. The current Core log name is `core.runner_ready` with `runner=model`, not the historical literal `runner=wsl`.
- Core evidence must belong to the isolated current startup (correlate its ready epoch/port and ordering, reject failed/restarted/ambiguous evidence); a standalone hello or HTTP ready alone is insufficient. Do not add a fake production readiness flag to satisfy the smoke.
- Finish owned hello/application transports through shutdown and observed exit/close. Timeout, overflow, malformed/missing hello, early/failed exit, unconfirmed cleanup or Core stop failure makes the smoke fail even if a payload says ok. After any unconfirmed cleanup, stop the entire smoke immediately; never start the next scenario or transport. Error/kill acknowledgements are not exit evidence. Bound retained and forwarded stdout/stderr; raw frame/output must not become diagnostics.
- On non-Windows, explicitly report WSL not-run and control-plane/resource-only coverage. A Mac pass cannot satisfy the task's Windows checkbox. Fake process fixtures may test this verifier's rejection behavior, never count as the real app/WSL acceptance.

A hosted CI machine without the prepared distribution may run a separately named **artifact-only** check of the actual Core/source/manifest tree and report WSL **not-run**. This is not packaged WSL smoke success and must not close the Windows acceptance gate. Keep default Windows strict smoke unchanged; do not add an implicit skip-to-PASS path, auto-provision an image or invent self-hosted permissions/labels to hide the missing environment.

## Validation and acceptance

| Condition | Outcome |
|---|---|
| Valid staged tree and default packaged Windows source | WSL command references only verified asar-external sources |
| Manifest missing/invalid, version disagreement, file mismatch/escape/duplicate | Readable failure; no checkout/host/mock fallback |
| Explicit external WSL command | Existing compatibility route, marked external; not bundled evidence |
| Working directory contains a same-named module | Packaged command still imports the verified source |
| Missing image/Python or failed hello | Finite unavailable/failure; never HTTP-only smoke PASS |
| Explicit app lacks resources or has no WSL proof | Smoke failure, no SKIP-to-PASS |
| Cancellation/cleanup cannot be confirmed | Smoke failure; no global WSL/process kill |

Required evidence: deterministic staging and schema/negative tests; exact WSL argv/path/default-vs-override tests; actual isolated Python cwd-shadow regression; fake-child negative smoke tests; real package resource inspection; and finally a prepared Windows/WSL installation with no application dependency on checkout, genuine hello/Core route, special-character paths and owned exit. Real model execution/offline bundles, image provisioning and signing are separate tasks/gates.
