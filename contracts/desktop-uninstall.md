# Desktop uninstall data choice and helper

Status: implemented and locally verified, 2026-11-24. This contract is the only
alignment source for the Windows uninstaller and the bundled helper. Native
model deletion was verified against a synthetic `.flashcli`/SQLite fixture;
real WSL distribution deletion, signed release and per-machine native paths
remain separate gates. It does not authorize deleting real WSL distributions,
bundles, runtimes or user files.

## Ownership and lifecycle

- The NSIS uninstaller owns the page and the final program-directory removal.
- `yaomu-uninstall-helper.exe` is staged under
  `$INSTDIR\resources\core` and is invoked by `customUnInstall` **before** the
  program directory is removed.
- The helper never calls `wsl --terminate`, `wsl --unregister`, `wsl --shutdown`,
  `flashcli bundle clean`, or any command that removes a bundle, venv, runtime,
  distribution or the whole userData root.
- Application processes must be stopped before `apply`. The uninstaller's
  `CHECK_APP_RUNNING` and the helper's process check are both required; the
  helper fails closed when it cannot prove that no owned process is running.
- Automatic updates (`--updated`) do not show the page and do not call the
  destructive modes. Silent uninstalls keep both choices unselected.

## Command line

```text
yaomu-uninstall-helper inventory --user-data <dir> --install-dir <dir> \
  --documents <dir> --result <ini> [--flashcli-root <dir>]

yaomu-uninstall-helper apply --user-data <dir> --install-dir <dir> \
  --documents <dir> --result <ini> [--personal] [--models] \
  [--snapshot <hex>] [--flashcli-root <dir>]
```

- `--user-data` is Electron `app.getPath("userData")`, normally
  `%APPDATA%\@yaomustudio\desktop`.
- `--install-dir` is `$INSTDIR`.
- `--documents` is the user's Documents directory.
- `--result` is a UTF-8 INI file written by the helper. Values used by NSIS are
  ASCII only.
- `--flashcli-root` exists only for isolated tests and is not emitted by the
  production NSIS include. Production resolves the `yaomu` WSL home itself.
- `--snapshot` is the opaque inventory token. `apply` re-inventories and
  rejects a changed snapshot before any deletion.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | success, including no selected targets |
| 2 | usage/contract error |
| 3 | inventory changed (snapshot stale) |
| 4 | export backup failed; no data deletion performed |
| 5 | unsafe/unknown target or permission failure; no broad deletion |
| 6 | owned application process still running |

## Inventory result

`inventory` writes `[uninstall]` with exact ASCII keys:

```ini
[uninstall]
schema_version=1
status=ok
personal_bytes=123
models_bytes=456
models_download_bytes=456
models_known=1
models_size_display=456 B
snapshot=0123456789abcdef...
message_key=
```

- `status`: `ok`, `no_targets`, `unknown` or `failed`.
- `models_known=1` only when every app-owned FlashCLI ref, bundle manifest,
  download manifest and candidate file was validated without links/reparse
  points, path escape, shared unknown repo or parse failure.
- `models_bytes` is the sum of unique existing files selected for deletion;
  `models_download_bytes` is the manifest download total. They may differ for
  partially downloaded or shared files.
- `models_size_display` is a human-readable ASCII string, never `0` when the
  size is unknown. Use an empty value for unknown.
- `snapshot` is a lowercase SHA-256 over the canonical selected-scope manifest.

## Apply result

`apply` writes `[uninstall]` with:

```ini
[uninstall]
schema_version=1
status=ok
backup_status=ok
backup_path=<absolute Documents target or empty>
personal_deleted=0
models_deleted=0
message_key=
```

- `backup_status`: `ok`, `none`, `failed`.
- `backup_path` is empty when nothing was backed up. It is an absolute
  user-visible Documents target, never an internal userData/WSL path.
- `personal_deleted` / `models_deleted` are `0` or `1`.
- A partial failure reports `status=failed` and a fixed `message_key`; the
  uninstaller must stop before program-directory removal. `message_key` values
  are installer-local hyphen tokens (`uninstall-error-*`) mapped in NSIS, not
  renderer i18n dictionary keys.

## Data boundaries

### Personal data (only with `--personal`)

Delete only these app-owned paths under `--user-data`, after link/reparse checks:

```text
core-data/yaomu.db
core-data/yaomu.db-wal
core-data/yaomu.db-shm
core-data/media/
core-data/cache/
core-data/exports/
core-data/work/
core-data/file-commits/
core-data/logs/
desktop-preferences.json
onboarding.json
Local Storage/
Session Storage/
Cache/
Code Cache/
GPUCache/
Network/
blob_storage/
Service Worker/
```

Never delete `core-data/models`, `core-data/runtime`, `wsl`, the userData root
itself, an unknown top-level entry, a symlink/reparse point, or anything outside
`--user-data`.

### Models (only with `--models`)

1. Open `<user-data>/core-data/yaomu.db` read-only. Read `installations` rows
   with `phase='done'` and their `plan_id`; read
   `app_meta['installation_plan.' || plan_id]` and collect admitted
   `flashcli_ref` values.
2. Resolve the production FlashCLI root as
   `\\wsl$\<distro>\<home>\.flashcli`; `<distro>` is `YAOMU_WSL_DISTRO` or
   `yaomu`, and `<home>` comes from a bounded `wsl.exe ... python3` probe.
3. For each ref, read `bundles/<name>/<version>/flashcli-bundle.json`;
   collect `defaults.weights.repo`, top-level `weights.repo` (format 3) and
   `defaults.extra_weights.*.repo`.
4. Walk `models/**/.flashcli_download.json`; collect files only for repos owned
   by the app refs. If any file is also referenced by another repo, or any
   manifest is missing/corrupt, mark the whole model scope unknown and delete
   nothing.
5. Reject absolute paths, `..`, backslashes in manifest paths, links/reparse
   points at the file or any ancestor, and paths outside the manifest directory.
6. Delete only regular files listed by validated manifests. Never delete
   `flashcli-bundle.json`, `bundle`, `runtime`, `runtimes`, `.cache`, a venv, a
   distribution or a real user file. Removing empty weight-cache directories is
   optional and never required for success.

### Export backup (always, regardless of selections)

- Back up `<install-dir>/outputs` and, if present,
  `<user-data>/core-data/exports` to a unique new directory under
  `<documents>/YaomuStudio Exports/`.
- Never overwrite an existing target. Reject links/reparse points and paths
  escaping the source. Copy regular files, then verify size and SHA-256 for
  every file. On failure remove the partial new backup and return exit 4 before
  any data deletion.
- The original outputs are not deleted by the helper; the normal uninstaller
  removes the program directory after the helper succeeds.

## Verification requirements

- Contract/unit tests for inventory/apply, four selection combinations,
  snapshot mismatch, shared/unknown manifests, path escape, links/reparse
  points, partial download, zero models, backup conflict/failure and process
  check.
- NSIS static tests for two default-off checkboxes, no WSL terminate/unregister,
  no whole-userData removal, Enter/No default, `--delete-app-data` rejection and
  failure blocking program-directory removal.
- Isolated Windows VM native run for page language/selection/confirmation,
  silent/update behavior, onboarding reset, export backup and helper failure.
- Real WSL model deletion is not accepted unless performed against a synthetic
  cache with before/after file and distribution/runtime evidence.
