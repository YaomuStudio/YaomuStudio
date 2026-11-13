"""OmniVoice serve 会话：SRT、健康检查、预测拼轨（假 HTTP/进程）。"""

from __future__ import annotations

import io
import tempfile
import threading
import unittest
import wave
from pathlib import Path

import _helpers  # noqa: F401

from yaomustudio_runner.omnivoice_serve import (
    OmniVoiceCloneSession,
    concat_wav_timeline,
    instruct_for_language,
    parse_srt,
    slice_reference_wav,
)


def _silent_wav(*, duration_ms: int = 200, rate: int = 24000) -> bytes:
    frames = int(rate * duration_ms / 1000)
    buf = io.BytesIO()
    with wave.open(buf, "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(2)
        handle.setframerate(rate)
        handle.writeframes(b"\x00\x00" * frames)
    return buf.getvalue()


class SrtAndInstructTests(unittest.TestCase):
    def test_parse_srt(self):
        cues = parse_srt(
            "1\n00:00:00,000 --> 00:00:01,000\n你好\n\n"
            "2\n00:00:01,500 --> 00:00:02,000\n世界\n"
        )
        self.assertEqual(len(cues), 2)
        self.assertEqual(cues[0].text, "你好")
        self.assertEqual(cues[1].start_ms, 1500)

    def test_instruct(self):
        self.assertIsNone(instruct_for_language("chinese"))
        self.assertEqual(instruct_for_language("chinese-河南话"), "河南话")
        self.assertIsNone(instruct_for_language("chinese-粤语"))


class TimelineTests(unittest.TestCase):
    def test_concat_places_clip(self):
        clip = _silent_wav(duration_ms=100)
        out = concat_wav_timeline([(0, clip)], total_ms=200)
        self.assertGreater(len(out), len(clip))

    def test_slice_reference_wav(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "ref.wav"
            dest = root / "slice.wav"
            source.write_bytes(_silent_wav(duration_ms=1000))
            slice_reference_wav(source, 100, 300, dest)
            with wave.open(str(dest), "rb") as handle:
                self.assertEqual(handle.getframerate(), 24000)
                self.assertEqual(handle.getnframes(), 4800)


class SessionTests(unittest.TestCase):
    def test_run_with_fakes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            voice = root / "ref.wav"
            sub = root / "sub.srt"
            orig = root / "orig.srt"
            out = root / "attempt" / "out.wav"
            work = root / "attempt" / "work"
            voice.write_bytes(_silent_wav())
            sub.write_text(
                "1\n00:00:00,000 --> 00:00:00,500\nhello\n", encoding="utf-8"
            )
            orig.write_text(
                "1\n00:00:00,000 --> 00:00:00,500\n你好\n", encoding="utf-8"
            )
            out.parent.mkdir()

            wav = _silent_wav(duration_ms=100)

            class FakeServe:
                def __init__(self, argv, env=None):
                    self.argv = argv
                    self.env = env

                def start(self):
                    return self

                def terminate(self, **kwargs):
                    return True

            class FakeStatic:
                def shutdown(self):
                    return None

            cancel = threading.Event()
            session = OmniVoiceCloneSession(
                env={},
                cancel_event=cancel,
                flashcli_bin="/usr/bin/flashcli",
                serve_factory=lambda argv, env=None: FakeServe(argv, env),
                health_waiter=lambda *a, **k: None,
                predictor=lambda *a, **k: wav,
                static_starter=lambda root: (FakeStatic(), 9, threading.Thread()),
                pick_port=lambda: 18081,
            )
            # predictor returns raw wav bytes already decoded by post_prediction;
            # session expects predictor to return bytes — use wav directly.
            meta = session.run(
                voice_path=str(voice),
                subtitle_path=str(sub),
                original_subtitle_path=str(orig),
                language="chinese",
                output_path=str(out),
                work_dir=str(work),
            )
            self.assertEqual(meta["segments"], 1)
            self.assertTrue(out.is_file())
            self.assertGreater(out.stat().st_size, 0)


    def test_run_segment_with_fakes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            voice = root / "ref.wav"
            out = root / "attempt" / "candidate.wav"
            work = root / "attempt" / "work"
            voice.write_bytes(_silent_wav(duration_ms=1000))
            out.parent.mkdir()

            captured: dict[str, object] = {}
            wav = _silent_wav(duration_ms=100)

            class FakeServe:
                def __init__(self, argv, env=None):
                    self.argv = argv
                    self.env = env

                def start(self):
                    return self

                def terminate(self, **kwargs):
                    return True

            class FakeStatic:
                def shutdown(self):
                    return None

            def predictor(base_url, payload, *, cancel_event):
                captured["payload"] = dict(payload)
                return wav

            session = OmniVoiceCloneSession(
                env={},
                cancel_event=threading.Event(),
                flashcli_bin="/usr/bin/flashcli",
                serve_factory=lambda argv, env=None: FakeServe(argv, env),
                health_waiter=lambda *a, **k: None,
                predictor=predictor,
                static_starter=lambda directory: (FakeStatic(), 9, threading.Thread()),
                pick_port=lambda: 18081,
            )
            meta = session.run_segment(
                voice_path=str(voice),
                start_ms=100,
                end_ms=400,
                text="你好",
                original_text="Hello",
                language="chinese",
                output_path=str(out),
                work_dir=str(work),
            )
            self.assertEqual(meta["segments"], 1)
            self.assertTrue(out.is_file())
            self.assertGreater(out.stat().st_size, 0)
            payload = captured["payload"]
            self.assertEqual(payload["text"], "你好")
            self.assertEqual(payload["reference_text"], "Hello")
            self.assertTrue(str(payload["reference_audio"]).endswith("/ref-slice.wav"))
            with wave.open(str(work / "ref-slice.wav"), "rb") as handle:
                self.assertEqual(handle.getnframes(), 7200)


class CloseLogPrivacyTests(unittest.TestCase):
    def test_close_failure_log_omits_exception_text(self):
        raw = "虚构字幕：这是不该落盘的一句台词 FLASHCLI_TOKEN=虚构密钥 /mnt/d/private"

        class ExplodingStatic:
            def shutdown(self):
                raise RuntimeError(raw)

        session = OmniVoiceCloneSession(env={}, cancel_event=threading.Event())
        session._static = ExplodingStatic()
        with self.assertLogs("yaomustudio_runner.omnivoice_serve", level="ERROR") as captured:
            session.close()
        joined = "\n".join(captured.output)
        for marker in ("虚构字幕", "虚构密钥", "/mnt/d/private"):
            self.assertNotIn(marker, joined)
        self.assertIn("关闭参考音频 HTTP 服务失败", joined)
        self.assertIsNone(session._static)


if __name__ == "__main__":
    unittest.main()
