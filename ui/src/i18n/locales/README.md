# Interface Language Packs

Language packs are plain JSON resources loaded on demand by
`ui/src/i18n/index.ts`. They are pure UI assets: they never affect the Core
service, contracts, model slots, or stored project data.

## Format

- One file per interface locale: `ui/src/i18n/locales/<locale>.json`.
- `<locale>` must exactly match a code from `UI_LANGUAGES`
  (`ui/src/domain/languages.ts`), including case (`zh-TW`, `pt`, `no`, ...).
- Flat key/value pairs. Every key must exist in the English dictionary (except
  valid extra CLDR plural categories for a known family; see below); a pack is
  a valid subset, not necessarily a full copy:

```json
{
  "settings.language.title": "語言",
  "settings.language.desc": "介面語言與翻譯完整度。切換即時生效。"
}
```

Do not nest objects. Do not add comments (JSON has none). Keep placeholders
such as `{percent}` and `{language}` exactly as they are in English.

Count messages use flat plural suffix keys instead of nested objects. For
example `projects.list.count.one` and `projects.list.count.other`. `en` and
`zh-CN` register both forms; other locales add only the categories their rules
need (a usable family requires at least `.other`).

## Loading and fallback

- Files are discovered with `import.meta.glob("./locales/*.json")` and loaded
  the first time the locale is selected. Vite emits one chunk per pack.
- zh-CN and en are compiled into the main bundle via
  `ui/src/i18n/register.ts` (`base.ts` plus each page's `messages.ts`); they do
  not use JSON packs.
- A missing pack, a failed dynamic import, or invalid JSON is treated as an
  empty pack. The UI falls back to English and keeps working; nothing throws
  and no request is retried forever.
- A loaded pack is filtered before use: the key must exist in English, the
  value must be a non-empty string, and placeholders must match English. Unknown
  keys, blank values, non-strings, and placeholder mismatches are dropped and
  fall back to English. Exception: the CLDR plural categories `zero`, `two`,
  `few`, and `many` are accepted under a known English plural family (a valid
  `<key>.other`) and are validated against that family's `.other` placeholders.
  Unknown families and arbitrary suffixes are still dropped.
- Completion counts only entries whose key exists in the English dictionary.
  Extra plural categories can render but never raise the English coverage
  percentage, so invalid or additive entries cannot inflate it.
- Any key missing from a pack falls back to English, then to the key name.

## Plural counts

`tCount(key, count, params?)` selects `<key>.<category>` from the current
locale when that locale has a usable family (at least `.other`). If it does
not, the template is re-selected from English with English plural rules rather
than reusing the current language's category. Only non-negative safe integers
are accepted; negative, fractional, `NaN`, `Infinity`, and unsafe values render
`—`. The `{count}` placeholder always comes from the independent `count`
argument and cannot be overridden by `params.count`.

## Completion

`getLocaleCompletion(code)` reports:

- `100` for `zh-CN` and `en`.
- `0` when the pack is missing, empty, or failed to load.
- Otherwise `round(validPackEntries / validEnglishEntries * 100)`, capped at
  `100`.

Valid entries are exactly the entries accepted by loading (known English key,
non-empty string, matching placeholders). The settings page preloads every pack
that exists so the current locale's completion is based on real keys, not a
hardcoded value. The selector shows native language names only; the completion
badge and progress bar are hidden when the current locale is 100%. Do not
hardcode completion percentages and do not claim a language is complete based
on intent, machine translation, or a partial seed file.

## Right-to-left

`ar`, `he`, and `fa` set `document.documentElement.dir = "rtl"` when selected;
all others set `ltr`. This is a direction flag only. Selected values use
value-level isolation: unknown device names and file names use `<bdi dir="auto">`,
count phrases and visible notices use a plain `<bdi>` (auto), while
protocol-style values (timestamps, dates, byte/duration units) use
`<bdi dir="ltr">`. Full mirrored layout, icons, spacing, and visual QA are not
implemented or verified yet; do not claim complete RTL support.

## Current coverage status

Measured on 2026-11-12 against the current English dictionary after the
remediation merge, phase-2 correction UI, UI branch convergence batch 1,
UI branch round-2 batch 1 (projects search), batches 2–3 (correction
workspace/stage/inline candidates), batches 4–5 (correction export/remix
dialogs), batches 6–7 (read-only erase panel and axe/i18n closeout), the UI
copy revision, the three-platform device probe keys, the language/creation
page merge, and the sidebar model status translations
(1788 valid English entries; `workbench.card.mat.unit` is a single-space
formatting value and is excluded by the non-empty rule):

- `zh-CN` / `en`: 100% (compiled dictionaries).
- All 24 non-compiled locales have a pack: `zh-TW`, `ar`, `de`, `es`, `fa`,
  `fr`, `he`, `hi`, `id`, `it`, `ja`, `ko`, `pl`, `pt`, `ru`, `tr`, `cs`,
  `da`, `fi`, `ms`, `no`, `th`, `uk`, `vi`.
  - `zh-TW`: 96% (1711 of 1788 valid English keys; merged with the previous
    curated Traditional Chinese values for keys the imported pack does not cover).
  - The other 23 packs: 90% each (1602–1605 of 1788 valid English keys).
  - These are translations imported from the UI reference branch
    (`feat/manual-correction-ui` at `92ec182` and `eefa01e`), filtered to keys
    that exist in the current English dictionary. They are not 100% complete
    and must not be described as full translations.
  - Missing keys are English fallback. Main-only features (`mcs.*`,
    `pipeline.delivery/editor/export/import`, `projects.folder.*`,
    `projectDetail.folder.*`, `workbench.card.dl.*`, ...) remain untranslated.
  - The round-2 UI removed the abandoned standalone candidates tab and the
    unused phase note. Their keys (`mcs.tab.candidates`, `mcs.phaseNote`) are
    no longer in the English dictionary; `zh-TW` dropped the old tab label so
    the pack stays a valid English subset.
  - Three stale authorization strings from the reference branch
    (`settings.privacy.consentTitle`, `settings.privacy.consentBody`,
    `voiceClone.result.emptyDesc`) are intentionally omitted: their English copy
    changed when per-task voice authorization was removed, so the old
    translations would reintroduce a removed step. They fall back to the
    current English copy.
  - Two per-file language strings (`voiceClone.sub.translatedHint`,
    `voiceClone.sub.translatedAuto`) are also omitted: the English copy changed
    when per-file language selection was replaced by filename-based detection,
    so the old translations would tell users to pick a language per file.
- Every selectable locale now has a pack, so there is no default 0% path in the
  UI. The missing-pack fallback stays implemented and is covered by an
  API-level E2E test with a fake locale (`xx`).

Do not claim that all 26 interface languages, or all 24 bundled packs, are
fully translated. Completion shown in Settings comes from real valid keys and
is hidden entirely for compiled 100% locales.

## Adding or extending a language

1. Pick the locale code from `UI_LANGUAGES` and create
   `ui/src/i18n/locales/<locale>.json`.
2. Translate only keys you can verify. Copying English values verbatim is not a
   translation; leaving keys out is preferred because they fall back cleanly.
   Never add unknown keys or placeholder-inconsistent values to raise the
   percentage.
3. Run `npm run typecheck -w ui`, `npm run build -w ui`,
   `npx playwright test e2e/language.spec.ts e2e/i18n-rtl-plurals.spec.ts --project=chromium`,
   and `npm run check:i18n`.
4. Update the coverage notes in this README and, if the pack changes behavior,
   the language-pack specification.

Do not generate machine translations and present them as a finished pack. The
application surfaces measured completion, so an incomplete pack must remain
visibly incomplete.

## Tests

`e2e/language.spec.ts` covers:

- all 26 locales selectable, no "unavailable" blocking;
- every bundled pack is a valid subset of the English dictionary with matching
  placeholders, no unknown keys, and measured completion between 90% and 100%
  (`zh-TW` at least 95%); no authorization keys;
- `zh-TW` translated text, English fallback for a missing key, measured partial
  completion, and the partial notice;
- `ja` with a pack: nonzero measured completion, no missing-pack notice;
- every selectable locale has a pack; the missing-pack fallback path is covered
  at API level with a fake locale (`xx`): 0%, English fallback, no blank page;
- 100% locales (`en`/`zh-CN`) hide the completion badge and progress bar;
  partial packs still show them;
- `ar` → `dir="rtl"`, `en` → `dir="ltr"`;
- follow-system exact match and unknown-locale fallback to English.

`e2e/i18n-rtl-plurals.spec.ts` covers:

- `0/1/2/many` in English, Chinese, `ar` English fallback, and `zh-TW`;
- invalid counts and `params.count` non-override;
- valid-pack filtering and completion with unknown/blank/non-string/placeholder
  entries;
- value-level bidi (`bdi` auto vs LTR) and LTR timecode inputs under RTL;
- an already displayed notice re-rendering on locale change without resending
  the save.
