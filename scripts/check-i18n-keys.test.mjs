// 消息键守护的可重复负向测试：临时 fixture 树 + node:test，无新依赖。
//
// 覆盖：
// - 文案区域文件存在但未在 register.ts 实际注册 → 失败；
// - Vue 模板单引号 t('...') 缺键 → 失败；
// - camelCase 用户键缺键 → 失败；
// - en/zh-CN 占位参数不一致 → 失败；
// - 领域事件名/日志标签不被误收为文案键；
// - Go MessageKey 缺键 → 失败。
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { runGuard } from "./check-i18n-keys.mjs";

const BASE = `const base = {
  en: {
    "error.unknown": "Unknown",
  },
  zhCN: {
    "error.unknown": "未知",
  },
};

export default base;
`;

const ALPHA = `const messages = {
  en: {
    "page.title": "Title",
  },
  zhCN: {
    "page.title": "标题",
  },
};

export default messages;
`;

const BETA = `const messages = {
  en: {
    "page.beta": "Beta",
  },
  zhCN: {
    "page.beta": "测试",
  },
};

export default messages;
`;

const REGISTER = `import { registerMessages } from "./index";
import base from "./base";
import alpha from "@/pages/Alpha/messages";

const areas = [base, alpha];

for (const area of areas) registerMessages(area);

export {};
`;

function fixture(files) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "yaomu-i18n-guard-"));
  fs.mkdirSync(path.join(root, "app-server"), { recursive: true });
  for (const [relative, content] of Object.entries(files)) {
    const file = path.join(root, relative);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, content, "utf8");
  }
  return root;
}

function withFixture(files, run) {
  const root = fixture(files);
  try {
    return run(root);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
}

function baseFixture(extra = {}) {
  return {
    "ui/src/i18n/base.ts": BASE,
    "ui/src/i18n/register.ts": REGISTER,
    "ui/src/i18n/errorCodes.json": "{}\n",
    "ui/src/pages/Alpha/messages.ts": ALPHA,
    ...extra,
  };
}

test("passes when every area file is registered and all keys exist", () => {
  withFixture(baseFixture(), (root) => {
    const result = runGuard({ repoRoot: root });
    assert.equal(result.ok, true);
    assert.deepEqual(result.unregisteredAreaFiles, []);
  });
});

test("fails when an area file is imported but removed from the registered areas array", () => {
  const registerWithDroppedArea = `import { registerMessages } from "./index";
import base from "./base";
import alpha from "@/pages/Alpha/messages";
import beta from "@/pages/Beta/messages";

const areas = [base, alpha];

for (const area of areas) registerMessages(area);

export {};
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/register.ts": registerWithDroppedArea,
      "ui/src/pages/Beta/messages.ts": BETA,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.unregisteredAreaFiles.length, 1);
      assert.ok(
        result.unregisteredAreaFiles[0]
          .replaceAll("\\", "/")
          .endsWith("pages/Beta/messages.ts"),
      );
    },
  );
});

test("fails on single-quoted and camelCase UI keys that are missing", () => {
  const page = `<template>
  <p>{{ t('page.missingSingle') }}</p>
  <p>{{ t("error.missingCamel") }}</p>
</template>
`;
  withFixture(
    baseFixture({
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.missingEn.some((line) => line.startsWith("page.missingSingle ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("page.missingSingle ")));
      assert.ok(result.missingEn.some((line) => line.startsWith("error.missingCamel ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("error.missingCamel ")));
    },
  );
});

test("fails when en and zh-CN placeholders differ", () => {
  const baseWithMismatch = `const base = {
  en: {
    "page.count": "Count {count}",
  },
  zhCN: {
    "page.count": "数量",
  },
};

export default base;
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": baseWithMismatch,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.placeholderMismatches.some((line) => line.startsWith("page.count")));
    },
  );
});

test("fails on a missing camelCase Go MessageKey", () => {
  const goProbe = `package service

import "example.com/yaomu/internal/domain"

func guardProbe() domain.ErrorInfo {
	return domain.ErrorInfo{
		Code:       domain.ErrorCodeInvalidRequest,
		MessageKey: "error.guardProbeMissing",
	}
}
`;
  withFixture(
    baseFixture({
      "app-server/internal/service/guard_probe.go": goProbe,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.missingEn.some((line) => line.startsWith("error.guardProbeMissing ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("error.guardProbeMissing ")));
    },
  );
});

test("ignores domain event names and log labels", () => {
  const goLog = `package service

type guardEvent struct {
	Type string
}

var _ = guardEvent{Type: "task.guardProbeEvent"}
var _ = "installation.guard_probe_log_label"
`;
  withFixture(
    baseFixture({
      "app-server/internal/service/guard_log.go": goLog,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, true);
      assert.deepEqual(result.missingEn, []);
      assert.deepEqual(result.missingZh, []);
    },
  );
});

const PLURAL_BASE = `const base = {
  en: {
    "error.unknown": "Unknown",
    "page.count.one": "{count} item",
    "page.count.other": "{count} items",
  },
  zhCN: {
    "error.unknown": "未知",
    "page.count.one": "{count} 项",
    "page.count.other": "{count} 项",
  },
};

export default base;
`;

const PLURAL_PAGE = `<template>
  <p>{{ tCount("page.count", 2) }}</p>
</template>
`;

test("passes when a tCount family has both one and other keys", () => {
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": PLURAL_BASE,
      "ui/src/pages/Alpha/Page.vue": PLURAL_PAGE,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, true);
      assert.deepEqual(result.dynamicKeys, []);
    },
  );
});

test("fails when a tCount family is missing .one", () => {
  const baseWithoutOne = `const base = {
  en: {
    "error.unknown": "Unknown",
    "page.count.other": "{count} items",
  },
  zhCN: {
    "error.unknown": "未知",
    "page.count.other": "{count} 项",
  },
};

export default base;
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": baseWithoutOne,
      "ui/src/pages/Alpha/Page.vue": PLURAL_PAGE,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.missingEn.some((line) => line.startsWith("page.count.one ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("page.count.one ")));
    },
  );
});

test("fails when a tCount family is missing .other", () => {
  const baseWithoutOther = `const base = {
  en: {
    "error.unknown": "Unknown",
    "page.count.one": "{count} item",
  },
  zhCN: {
    "error.unknown": "未知",
    "page.count.one": "{count} 项",
  },
};

export default base;
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": baseWithoutOther,
      "ui/src/pages/Alpha/Page.vue": PLURAL_PAGE,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.missingEn.some((line) => line.startsWith("page.count.other ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("page.count.other ")));
    },
  );
});

test("fails when a plural family has inconsistent placeholders", () => {
  const baseWithPluralMismatch = `const base = {
  en: {
    "error.unknown": "Unknown",
    "page.count.one": "{count} item",
    "page.count.other": "{count} items",
  },
  zhCN: {
    "error.unknown": "未知",
    "page.count.one": "项目",
    "page.count.other": "{count} 项",
  },
};

export default base;
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": baseWithPluralMismatch,
      "ui/src/pages/Alpha/Page.vue": PLURAL_PAGE,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.placeholderMismatches.some((line) => line.startsWith("page.count.one")));
    },
  );
});

test("fails on dynamic tCount keys instead of silently excluding them", () => {
  const dynamicPage = `<script setup lang="ts">
const key = "page.count";
const label = tCount(key, 2);
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": PLURAL_BASE,
      "ui/src/pages/Alpha/Page.vue": dynamicPage,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.dynamicKeys.length, 1);
      assert.ok(result.dynamicKeys[0].includes("tCount(key"));
    },
  );
});

test("fails on missing messageNotice, errorNotice and fallbackErrorNotice literal keys", () => {
  const page = `<script setup lang="ts">
const success = messageNotice("page.missingSuccess");
const failure = errorNotice(err, "page.missingPrefix");
const fallback = fallbackErrorNotice(err, "page.missingFallback");
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": PLURAL_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.missingEn.some((line) => line.startsWith("page.missingSuccess ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("page.missingPrefix ")));
      assert.ok(result.missingEn.some((line) => line.startsWith("page.missingFallback ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("page.missingFallback ")));
    },
  );
});

test("fails on dynamic messageNotice keys", () => {
  const page = `<script setup lang="ts">
const key = "page.count.other";
const success = messageNotice(key);
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": PLURAL_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.dynamicKeys.length, 1);
      assert.ok(result.dynamicKeys[0].includes("messageNotice(key"));
    },
  );
});

const NOTICE_PARAM_BASE = `const base = {
  en: {
    "error.unknown": "Unknown",
    "page.saved": "Saved for {name}",
    "slot.asr": "Speech recognition",
  },
  zhCN: {
    "error.unknown": "未知",
    "page.saved": "已保存 {name}",
    "slot.asr": "语音识别",
  },
};

export default base;
`;

test("passes when noticeKey nested params use registered literal keys", () => {
  const page = `<script setup lang="ts">
const saved = messageNotice("page.saved", { name: noticeKey("slot.asr") });
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": NOTICE_PARAM_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, true);
      assert.deepEqual(result.missingEn, []);
      assert.deepEqual(result.missingZh, []);
      assert.deepEqual(result.dynamicKeys, []);
    },
  );
});

test("fails on missing noticeKey nested param literals", () => {
  const page = `<script setup lang="ts">
const saved = messageNotice("page.saved", { name: noticeKey("slot.missing") });
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": NOTICE_PARAM_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.missingEn.some((line) => line.startsWith("slot.missing ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("slot.missing ")));
    },
  );
});

test("fails on dynamic noticeKey nested params instead of treating them as safe", () => {
  const page = `<script setup lang="ts">
const name = "slot.asr";
const saved = messageNotice("page.saved", { name: noticeKey(name) });
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": NOTICE_PARAM_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.dynamicKeys.length, 1);
      assert.ok(result.dynamicKeys[0].includes("noticeKey(name"));
    },
  );
});

const HELPER_ARG_BASE = `const base = {
  en: {
    "error.unknown": "Unknown",
    "page.count.one": "{count} item",
    "page.count.other": "{count} items",
    "page.saved": "Saved",
    "page.prefix": "Failed",
  },
  zhCN: {
    "error.unknown": "未知",
    "page.count.one": "{count} 项",
    "page.count.other": "{count} 项",
    "page.saved": "已保存",
    "page.prefix": "失败",
  },
};

export default base;
`;

test("passes helper keys that are complete literal arguments across lines and comments", () => {
  const page = `<script setup lang="ts">
const a = tCount(
  /* count */ "page.count" /* key */,
  2,
);
const b = messageNotice(\`page.saved\`);
const c = errorNotice(makeError(1, 2), /* fallback */ "page.prefix");
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": HELPER_ARG_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, true);
      assert.deepEqual(result.missingEn, []);
      assert.deepEqual(result.missingZh, []);
      assert.deepEqual(result.dynamicKeys, []);
    },
  );
});

test("fails on concatenated or interpolated helper key arguments", () => {
  const page = `<script setup lang="ts">
const suffix = ".other";
const a = tCount("page.count" + suffix, 1);
const b = countNotice(\`page.count\${suffix}\`, 1);
const c = messageNotice("page.saved" + suffix);
const d = noticeKey("page.saved" + suffix);
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": HELPER_ARG_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.dynamicKeys.length, 4);
      assert.ok(result.dynamicKeys.some((line) => line.includes("tCount(")));
      assert.ok(result.dynamicKeys.some((line) => line.includes("countNotice(")));
      assert.ok(result.dynamicKeys.some((line) => line.includes("messageNotice(")));
      assert.ok(result.dynamicKeys.some((line) => line.includes("noticeKey(")));
    },
  );
});

test("fails on concatenated errorNotice/fallbackErrorNotice key arguments", () => {
  const page = `<script setup lang="ts">
const suffix = "x";
const a = errorNotice(err, "page.prefix" + suffix);
const b = fallbackErrorNotice(err, "page.prefix" + suffix);
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": HELPER_ARG_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.dynamicKeys.length, 2);
      assert.ok(result.dynamicKeys.some((line) => line.includes("errorNotice(...,")));
      assert.ok(result.dynamicKeys.some((line) => line.includes("fallbackErrorNotice(...,")));
    },
  );
});

test("fails when a comment hides a non-literal helper key argument", () => {
  const page = `<script setup lang="ts">
const key = "page.count";
const a = tCount(/* "page.count" */ key, 1);
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": HELPER_ARG_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.dynamicKeys.length, 1);
      assert.ok(result.dynamicKeys[0].includes("tCount(key"));
    },
  );
});

test("fails on multi-line concatenated helper key arguments", () => {
  const page = `<script setup lang="ts">
const suffix = ".other";
const a = tCount(
  "page.count"
  + suffix,
  1,
);
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": HELPER_ARG_BASE,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.dynamicKeys.length, 1);
      assert.ok(result.dynamicKeys[0].includes("tCount("));
    },
  );
});

test("fails when a countNotice family is missing .one or .other", () => {
  const baseWithoutFamily = `const base = {
  en: {
    "error.unknown": "Unknown",
  },
  zhCN: {
    "error.unknown": "未知",
  },
};

export default base;
`;
  const page = `<script setup lang="ts">
const notice = countNotice("page.attention.review", 2);
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": baseWithoutFamily,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.ok(result.missingEn.some((line) => line.startsWith("page.attention.review.one ")));
      assert.ok(result.missingEn.some((line) => line.startsWith("page.attention.review.other ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("page.attention.review.one ")));
      assert.ok(result.missingZh.some((line) => line.startsWith("page.attention.review.other ")));
    },
  );
});

test("fails when countNotice uses a dynamically constructed family key", () => {
  const baseWithFamily = `const base = {
  en: {
    "error.unknown": "Unknown",
    "page.attention.review.one": "{count} project",
    "page.attention.review.other": "{count} projects",
  },
  zhCN: {
    "error.unknown": "未知",
    "page.attention.review.one": "{count} 个项目",
    "page.attention.review.other": "{count} 个项目",
  },
};

export default base;
`;
  const page = `<script setup lang="ts">
const key = "page.attention.review";
const notice = countNotice(key, 2);
</script>
`;
  withFixture(
    baseFixture({
      "ui/src/i18n/base.ts": baseWithFamily,
      "ui/src/pages/Alpha/Page.vue": page,
    }),
    (root) => {
      const result = runGuard({ repoRoot: root });
      assert.equal(result.ok, false);
      assert.equal(result.dynamicKeys.length, 1);
      assert.ok(result.dynamicKeys[0].includes("countNotice(key"));
    },
  );
});
