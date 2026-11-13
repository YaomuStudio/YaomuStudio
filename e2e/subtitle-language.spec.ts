import { expect, test } from "@playwright/test";
import { seedOnboardedSettings } from "./support";

function srt(text: string): string {
  return `1\n00:00:00,000 --> 00:00:02,000\n${text}\n`;
}

test("subtitle language detection prefers content script over filename tokens", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/");

  const result = await page.evaluate(
    async ({ chinese, thai, latin }) => {
      const mod = await import("/src/domain/subtitleLanguage.ts");
      const file = (name: string, body: string) => new File([body], name, { type: "text/plain" });
      return {
        chinese: await mod.detectFileLanguage(file("wsy泰语cs_原文字幕.srt", chinese)),
        thai: await mod.detectFileLanguage(file("wsy泰语cs_译文字幕.srt", thai)),
        nameCode: await mod.detectFileLanguage(file("movie.cs.srt", latin)),
        latinFallback: await mod.detectFileLanguage(file("subtitles.srt", latin)),
      };
    },
    { chinese: srt("你好，这是原文字幕。"), thai: srt("สวัสดี นี่คือคำบรรยาย"), latin: srt("Hello world") },
  );

  // 文件名里的 cs 是“泰语cs”的一部分，不能盖过中文/泰文内容。
  expect(result.chinese).toBe("zh-CN");
  expect(result.thai).toBe("th");
  // 明确的独立语言码仍然有效。
  expect(result.nameCode).toBe("cs");
  expect(result.latinFallback).toBe("en");
});

test("voice clone detects Chinese subtitles despite a cs token in the file name", async ({
  page,
}) => {
  await seedOnboardedSettings(page);
  await page.goto("/#/voice-clone");

  await page.locator('input[accept=".srt"]:not([multiple])').setInputFiles({
    name: "wsy泰语cs_原文字幕.srt",
    mimeType: "text/plain",
    buffer: Buffer.from(srt("你好，这是原文字幕。")),
  });

  await expect(page.getByText("Detected language: 简体中文")).toBeVisible();

  // 译文字幕同样按内容识别：泰语内容 + 文件名里的 cs 不能变成捷克语。
  await page.locator('input[accept=".srt"][multiple]').setInputFiles({
    name: "wsy泰语cs_译文字幕.srt",
    mimeType: "text/plain",
    buffer: Buffer.from(srt("สวัสดี นี่คือคำบรรยาย")),
  });
  await expect(page.locator(".language-chip").filter({ hasText: "ไทย" })).toBeVisible();
  await expect(page.getByText("Czech")).toHaveCount(0);
  await expect(page.getByText("Čeština")).toHaveCount(0);
});
