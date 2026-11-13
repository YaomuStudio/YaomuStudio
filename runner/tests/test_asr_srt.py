"""ASR 词级时间戳到 SRT 的纯函数测试。"""

from __future__ import annotations

import unittest

import _helpers  # noqa: F401

from yaomustudio_runner.asr_srt import (
    AsrResultError,
    build_cues,
    format_srt_time,
    normalize_words,
    parse_asr_result,
    render_srt,
)


def _words(*items):
    return normalize_words(
        [
            {"text": text, "start_time": start, "end_time": end}
            for text, start, end in items
        ]
    )


class SegmentationTests(unittest.TestCase):
    def test_english_sentence_punctuation(self):
        words = _words(
            ("Hello", 0.0, 0.4),
            ("world", 0.4, 0.9),
            (".", 0.9, 0.9),
            ("Again", 1.0, 1.4),
            ("!", 1.4, 1.4),
        )
        cues = build_cues("Hello world. Again!", words)
        self.assertEqual([cue.text for cue in cues], ["Hello world.", "Again!"])
        self.assertAlmostEqual(cues[0].start, 0.0)
        self.assertAlmostEqual(cues[0].end, 0.9)
        self.assertAlmostEqual(cues[1].start, 1.0)
        self.assertAlmostEqual(cues[1].end, 1.4)

    def test_chinese_comma_and_sentence_breaks(self):
        words = _words(
            ("你", 0.0, 0.2),
            ("好", 0.2, 0.4),
            ("，", 0.4, 0.4),
            ("世", 0.5, 0.7),
            ("界", 0.7, 0.9),
            ("。", 0.9, 0.9),
            ("再", 1.0, 1.2),
            ("见", 1.2, 1.4),
            ("！", 1.4, 1.4),
        )
        cues = build_cues("你好，世界。再见！", words)
        self.assertEqual([cue.text for cue in cues], ["你好，", "世界。", "再见！"])

    def test_fallback_joins_words_when_text_missing(self):
        words = _words(("Hello", 0.0, 0.5), ("world", 0.6, 1.0))
        cues = build_cues("", words)
        self.assertEqual([cue.text for cue in cues], ["Hello world"])
        self.assertAlmostEqual(cues[0].start, 0.0)
        self.assertAlmostEqual(cues[0].end, 1.0)

    def test_long_gap_splits_fallback_cue(self):
        words = _words(("A", 0.0, 0.5), ("B", 3.0, 3.5))
        cues = build_cues("", words)
        self.assertEqual([cue.text for cue in cues], ["A", "B"])

    def test_overlapping_word_timestamps_use_envelope(self):
        words = _words(("A", 0.0, 1.0), ("B", 0.5, 0.8))
        cues = build_cues("", words)
        self.assertEqual(len(cues), 1)
        self.assertAlmostEqual(cues[0].start, 0.0)
        self.assertAlmostEqual(cues[0].end, 1.0)

    def test_cues_are_monotonic_and_non_overlapping(self):
        words = _words(("A", 0.0, 1.0), ("B", 0.5, 1.5))
        cues = build_cues("A. B.", words)
        self.assertEqual([cue.text for cue in cues], ["A.", "B."])
        self.assertLessEqual(cues[0].end, cues[1].start)

    def test_invalid_word_entries_are_skipped(self):
        words = normalize_words(
            [
                {"text": "ok", "start_time": 0.0, "end_time": 0.5},
                {"text": "bad", "start_time": "x", "end_time": 1.0},
                {"text": "", "start_time": 1.0, "end_time": 1.5},
                {"text": "nan", "start_time": float("nan"), "end_time": 2.0},
            ]
        )
        self.assertEqual([word.text for word in words], ["ok"])


class ResultAndRenderTests(unittest.TestCase):
    def test_parse_rejects_empty_result(self):
        with self.assertRaises(AsrResultError):
            parse_asr_result({"text": "", "time_stamps": []})

    def test_parse_rejects_missing_timestamps(self):
        with self.assertRaises(AsrResultError):
            parse_asr_result({"text": "hello", "time_stamps": []})

    def test_parse_accepts_words_without_text(self):
        result = parse_asr_result(
            {"time_stamps": [{"text": "hi", "start_time": 0, "end_time": 0.3}]}
        )
        self.assertEqual(result.text, "")
        self.assertEqual(len(result.words), 1)

    def test_srt_time_format(self):
        self.assertEqual(format_srt_time(0), "00:00:00,000")
        self.assertEqual(format_srt_time(3661.234), "01:01:01,234")
        self.assertEqual(format_srt_time(-1), "00:00:00,000")

    def test_rounding_keeps_strictly_positive_duration(self):
        # Core parseSRT 要求 end > start；不足 1 ms 的块必须抬到下一毫秒。
        words = _words(("A", 0.0006, 0.0011))
        cues = build_cues("", words)
        self.assertEqual(
            render_srt(cues),
            "1\n00:00:00,001 --> 00:00:00,002\nA\n",
        )

    def test_render_srt(self):
        words = _words(("Hello", 0.0, 0.4), (".", 0.4, 0.4))
        cues = build_cues("Hello.", words)
        self.assertEqual(
            render_srt(cues),
            "1\n00:00:00,000 --> 00:00:00,400\nHello.\n",
        )


if __name__ == "__main__":
    unittest.main()
