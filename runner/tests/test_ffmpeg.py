"""FFmpeg 适配：白名单 argv 构造、注入拒绝、路径注入与门控错误。"""

from __future__ import annotations

import os
import stat
import tempfile
import unittest
from unittest import mock
import subprocess
from pathlib import Path

import _helpers  # noqa: F401

from yaomustudio_runner import ffmpeg as ffmpeg_module
from yaomustudio_runner.ffmpeg import (
    AudioMixInput,
    FfmpegAdapter,
    MediaToolError,
    MediaToolFailed,
    MediaToolPaths,
    MediaToolTimeout,
    MediaToolUnavailable,
    MediaToolStopUnconfirmed,
    build_audio_extract_argv,
    build_burn_subtitles_argv,
    build_mix_argv,
    build_probe_argv,
    build_transcode_argv,
    build_video_merge_argv,
    describe_media_tools,
    resolve_media_tool_paths,
)

FFMPEG = "/opt/yaomu/bin/ffmpeg"
FFPROBE = "/opt/yaomu/bin/ffprobe"


def _write_fake_tool(directory: str, name: str, body: str) -> str:
    path = Path(directory) / name
    path.write_text("#!/bin/sh\n" + body + "\n", encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    return str(path)


class ProbeArgvTests(unittest.TestCase):
    def test_probe_argv_is_a_whitelisted_list(self):
        argv = build_probe_argv("/data/input.mp4", FFPROBE)
        self.assertEqual(
            argv,
            [
                FFPROBE,
                "-v",
                "error",
                "-print_format",
                "json",
                "-show_format",
                "-show_streams",
                "/data/input.mp4",
            ],
        )
        self.assertIsInstance(argv, list)
        self.assertTrue(all(isinstance(part, str) for part in argv))

    def test_injection_in_path_is_rejected(self):
        with self.assertRaises(ValueError):
            build_probe_argv("/data/in; rm -rf /", FFPROBE)
        with self.assertRaises(ValueError):
            build_probe_argv("/data/$(touch /tmp/pwned).mp4", FFPROBE)
        with self.assertRaises(ValueError):
            build_probe_argv("/data/`id`.mp4", FFPROBE)
        with self.assertRaises(ValueError):
            build_probe_argv("relative.mp4", FFPROBE)
        with self.assertRaises(ValueError):
            build_probe_argv("/data/bad\nname.mp4", FFPROBE)


class AudioExtractArgvTests(unittest.TestCase):
    def test_audio_extract_argv_is_fixed_16k_mono(self):
        argv = build_audio_extract_argv("/data/input.mp4", "/out/asr.wav", FFMPEG)
        self.assertEqual(
            argv,
            [
                FFMPEG,
                "-hide_banner",
                "-nostdin",
                "-loglevel",
                "warning",
                "-i",
                "/data/input.mp4",
                "-vn",
                "-ac",
                "1",
                "-ar",
                "16000",
                "-f",
                "wav",
                "-y",
                "/out/asr.wav",
            ],
        )

    def test_audio_extract_rejects_same_input_output(self):
        with self.assertRaises(ValueError):
            build_audio_extract_argv("/data/input.wav", "/data/input.wav", FFMPEG)


class MixArgvTests(unittest.TestCase):
    def test_two_input_mix_argv(self):
        argv = build_mix_argv(
            [
                AudioMixInput("/data/voice.wav", 0.8),
                AudioMixInput("/data/bgm.wav", 1.0),
            ],
            "/data/out.m4a",
            FFMPEG,
            bitrate_kbps=192,
            sample_rate=48000,
        )
        self.assertEqual(
            argv,
            [
                FFMPEG,
                "-hide_banner",
                "-nostdin",
                "-loglevel",
                "warning",
                "-i",
                "/data/voice.wav",
                "-i",
                "/data/bgm.wav",
                "-filter_complex",
                "[0:a]volume=0.80[a0];[1:a]volume=1.00[a1];"
                "[a0][a1]amix=inputs=2:duration=longest:normalize=0[mix]",
                "-map",
                "[mix]",
                "-c:a",
                "aac",
                "-b:a",
                "192k",
                "-ar",
                "48000",
                "-n",
                "/data/out.m4a",
            ],
        )

    def test_single_input_mix_argv(self):
        argv = build_mix_argv(
            [AudioMixInput("/data/only.wav", 1.25)], "/data/out.wav", FFMPEG
        )
        graph = argv[argv.index("-filter_complex") + 1]
        self.assertEqual(graph, "[0:a]volume=1.25[mix]")
        self.assertEqual(argv[-1], "/data/out.wav")

    def test_overwrite_flag(self):
        argv = build_mix_argv(
            [AudioMixInput("/data/a.wav")], "/data/out.wav", FFMPEG, overwrite=True
        )
        self.assertIn("-y", argv)
        self.assertNotIn("-n", argv)

    def test_start_ms_adds_adelay(self):
        argv = build_mix_argv(
            [
                AudioMixInput("/data/voice.wav", 1.0, start_ms=1500),
                AudioMixInput("/data/bgm.wav", 0.4, start_ms=0),
            ],
            "/data/out.m4a",
            FFMPEG,
        )
        graph = argv[argv.index("-filter_complex") + 1]
        self.assertIn("[0:a]volume=1.00,adelay=1500|1500[a0]", graph)
        self.assertIn("[1:a]volume=0.40[a1]", graph)
        self.assertIn("amix=inputs=2", graph)

    def test_mix_rejects_bad_parameters(self):
        bad_cases = [
            {"inputs": [], "output": "/data/out.wav"},
            {"inputs": [AudioMixInput("/data/a.wav", 3.0)], "output": "/data/out.wav"},
            {"inputs": [AudioMixInput("relative.wav")], "output": "/data/out.wav"},
            {"inputs": [AudioMixInput("/data/a.wav")], "output": "/data/a.wav"},
            {"inputs": [AudioMixInput("/data/a.wav")], "output": "/data/o; rm.wav"},
        ]
        for case in bad_cases:
            with self.subTest(case=case):
                with self.assertRaises(ValueError):
                    build_mix_argv(case["inputs"], case["output"], FFMPEG)
        with self.assertRaises(ValueError):
            build_mix_argv(
                [AudioMixInput("/data/a.wav")], "/data/out.wav", FFMPEG, audio_codec="mp4"
            )
        with self.assertRaises(ValueError):
            build_mix_argv(
                [AudioMixInput("/data/a.wav")],
                "/data/out.wav",
                FFMPEG,
                sample_rate=12345,
            )
        with self.assertRaises(ValueError):
            build_mix_argv(
                [AudioMixInput("/data/a.wav")],
                "/data/out.wav",
                FFMPEG,
                bitrate_kbps=10000,
            )


class BurnSubtitlesArgvTests(unittest.TestCase):
    def test_burn_argv_with_style_and_time_window(self):
        argv = build_burn_subtitles_argv(
            "/data/input.mp4",
            "/data/subs:zh.srt",
            "/data/output.mp4",
            FFMPEG,
            style={
                "font_name": "Noto Sans CJK SC",
                "font_size": 22,
                "primary_colour": "#FFCC00",
                "outline": 2,
                "alignment": 2,
                "margin_v": 36,
            },
            start_ms=1500,
            end_ms=4200,
            crf=20,
        )
        filter_value = argv[argv.index("-vf") + 1]
        self.assertEqual(
            filter_value,
            "subtitles=filename=/data/subs\\:zh.srt:force_style='"
            "FontName=Noto Sans CJK SC,FontSize=22,PrimaryColour=&H0000CCFF,"
            "Outline=2.0,Alignment=2,MarginV=36'",
        )
        # 输入侧参数先于输出侧时间窗，时间窗用毫秒转秒的固定格式。
        input_index = argv.index("-i")
        self.assertGreater(argv.index("-ss"), input_index)
        self.assertEqual(argv[argv.index("-ss") + 1], "1.500")
        self.assertEqual(argv[argv.index("-to") + 1], "4.200")
        self.assertIn("libx264", argv)
        self.assertIn("-crf", argv)
        self.assertIn("-preset", argv)
        self.assertEqual(argv[-1], "/data/output.mp4")

    def test_burn_escapes_single_quote_in_subtitle_path(self):
        argv = build_burn_subtitles_argv(
            "/data/input.mp4", "/data/it's.srt", "/data/output.mp4", FFMPEG
        )
        filter_value = argv[argv.index("-vf") + 1]
        self.assertEqual(filter_value, "subtitles=filename=/data/it\\'s.srt")
        self.assertNotIn("force_style", filter_value)

    @unittest.skipUnless(os.name == "nt", "Windows 盘符路径语义")
    def test_burn_quotes_windows_subtitle_path(self):
        argv = build_burn_subtitles_argv(
            r"C:\work\input.mp4",
            r"C:\work dir\subs,1[2].srt",
            r"C:\work\output.mp4",
            r"C:\tools\ffmpeg.exe",
        )
        filter_value = argv[argv.index("-vf") + 1]
        self.assertEqual(
            filter_value,
            r"subtitles=filename='C\:/work dir/subs,1[2].srt'",
        )

    @unittest.skipUnless(os.name == "nt", "Windows UNC 路径语义")
    def test_burn_quotes_unc_subtitle_path(self):
        argv = build_burn_subtitles_argv(
            r"\\server\share\input.mp4",
            r"\\server\share\subs.srt",
            r"\\server\share\output.mp4",
            r"C:\tools\ffmpeg.exe",
        )
        filter_value = argv[argv.index("-vf") + 1]
        self.assertEqual(filter_value, r"subtitles=filename='//server/share/subs.srt'")

    @unittest.skipUnless(os.name == "nt", "Windows 盘符路径语义")
    def test_burn_rejects_windows_subtitle_path_with_single_quote(self):
        with self.assertRaises(ValueError):
            build_burn_subtitles_argv(
                r"C:\work\input.mp4",
                r"C:\work\it's.srt",
                r"C:\work\output.mp4",
                r"C:\tools\ffmpeg.exe",
            )

    def test_burn_rejects_injection_and_bad_style(self):
        cases = [
            {"subtitle_path": "/data/sub; rm -rf.srt"},
            {"style": {"font_name": "Arial; rm -rf /"}},
            {"style": {"font_name": "Arial,Italic"}},
            {"style": {"unknown_field": 1}},
            {"style": {"primary_colour": "red"}},
            {"style": {"font_size": 999}},
            {"style": {"border_style": 2}},
            {"style": {"outline": 11}},
            {"style": {"alignment": 10}},
        ]
        for case in cases:
            kwargs = {
                "style": None,
                "start_ms": None,
                "end_ms": None,
            }
            kwargs.update(case)
            with self.subTest(case=case):
                with self.assertRaises(ValueError):
                    build_burn_subtitles_argv(
                        "/data/input.mp4",
                        kwargs.pop("subtitle_path", "/data/sub.srt"),
                        "/data/output.mp4",
                        FFMPEG,
                        **kwargs,
                    )

    def test_burn_rejects_negative_or_inverted_window(self):
        with self.assertRaises(ValueError):
            build_burn_subtitles_argv(
                "/data/in.mp4",
                "/data/s.srt",
                "/data/out.mp4",
                FFMPEG,
                start_ms=-1,
            )
        with self.assertRaises(ValueError):
            build_burn_subtitles_argv(
                "/data/in.mp4",
                "/data/s.srt",
                "/data/out.mp4",
                FFMPEG,
                start_ms=5000,
                end_ms=5000,
            )
        with self.assertRaises(ValueError):
            build_burn_subtitles_argv(
                "/data/in.mp4",
                "/data/s.srt",
                "/data/in.mp4",
                FFMPEG,
            )
        with self.assertRaises(ValueError):
            build_burn_subtitles_argv(
                "/data/in.mp4",
                "/data/s.srt",
                "/data/out.mp4",
                FFMPEG,
                audio_codec="copy",
                audio_bitrate_kbps=128,
            )


class VideoMergeArgvTests(unittest.TestCase):
    def test_video_merge_builds_filters_and_maps(self):
        argv = build_video_merge_argv(
            "/data/source.mp4",
            [
                AudioMixInput("/data/voice0.wav", 1.0, start_ms=500),
                AudioMixInput("/data/source.mp4", 0.3),
            ],
            "/data/out.mp4",
            FFMPEG,
            subtitle_path="/data/subs:zh.srt",
            style={"font_name": "Arial", "font_size": 24, "alignment": 2},
            crf=20,
            audio_bitrate_kbps=192,
            sample_rate=48000,
        )
        filter_graph = argv[argv.index("-filter_complex") + 1]
        self.assertIn("[0:v]subtitles=filename=/data/subs\\:zh.srt", filter_graph)
        self.assertIn("[1:a]volume=1.00,adelay=500|500[a0]", filter_graph)
        self.assertIn("[2:a]volume=0.30[a1]", filter_graph)
        self.assertIn("amix=inputs=2:duration=longest:normalize=0[mix]", filter_graph)
        self.assertEqual(argv[argv.index("-map") + 1], "[vout]")
        self.assertIn("libx264", argv)
        self.assertIn("-crf", argv)
        self.assertIn("-shortest", argv)
        self.assertEqual(argv[-1], "/data/out.mp4")

    @unittest.skipUnless(os.name == "nt", "Windows 盘符路径语义")
    def test_video_merge_quotes_windows_subtitle_path(self):
        argv = build_video_merge_argv(
            r"C:\work\source.mp4",
            [],
            r"C:\work\out.mp4",
            r"C:\tools\ffmpeg.exe",
            subtitle_path=r"C:\work dir\subs,1[2].srt",
        )
        filter_graph = argv[argv.index("-filter_complex") + 1]
        self.assertIn(
            r"[0:v]subtitles=filename='C\:/work dir/subs,1[2].srt'[vout]",
            filter_graph,
        )

    def test_video_merge_without_audio_disables_audio(self):
        argv = build_video_merge_argv(
            "/data/source.mp4", [], "/data/out.mp4", FFMPEG
        )
        self.assertIn("-an", argv)
        self.assertNotIn("-filter_complex", argv)
        self.assertEqual(argv[argv.index("-map") + 1], "0:v:0")

    def test_video_merge_rejects_bad_parameters(self):
        cases = [
            {"audio_codec": "copy"},
            {"video_codec": "vp9"},
            {"style": {"unknown_field": 1}},
            {"preset": "turbo"},
        ]
        for case in cases:
            with self.subTest(case=case):
                with self.assertRaises(ValueError):
                    build_video_merge_argv(
                        "/data/source.mp4",
                        [AudioMixInput("/data/voice.wav")],
                        "/data/out.mp4",
                        FFMPEG,
                        **case,
                    )
        with self.assertRaises(ValueError):
            build_video_merge_argv(
                "/data/source.mp4",
                [AudioMixInput("/data/voice.wav", start_ms=-1)],
                "/data/out.mp4",
                FFMPEG,
            )
        with self.assertRaises(ValueError):
            build_video_merge_argv(
                "/data/source.mp4",
                [],
                "/data/out.mp4",
                FFMPEG,
                style={"font_name": "Arial"},
            )


class TranscodeArgvTests(unittest.TestCase):
    def test_default_is_stream_copy_remux(self):
        argv = build_transcode_argv("/data/in.mkv", "/data/out.mp4", FFMPEG)
        self.assertEqual(argv[argv.index("-c:v") + 1], "copy")
        self.assertEqual(argv[argv.index("-c:a") + 1], "copy")
        self.assertEqual(argv[-1], "/data/out.mp4")

    def test_explicit_codecs(self):
        argv = build_transcode_argv(
            "/data/in.mov",
            "/data/out.mp4",
            FFMPEG,
            video_codec="h264",
            audio_codec="mp3",
            crf=23,
            preset="fast",
            audio_bitrate_kbps=192,
        )
        self.assertEqual(argv[argv.index("-c:v") + 1], "libx264")
        self.assertEqual(argv[argv.index("-crf") + 1], "23")
        self.assertEqual(argv[argv.index("-preset") + 1], "fast")
        self.assertEqual(argv[argv.index("-c:a") + 1], "libmp3lame")
        self.assertEqual(argv[argv.index("-b:a") + 1], "192k")

    def test_transcode_rejects_conflicting_options(self):
        with self.assertRaises(ValueError):
            build_transcode_argv(
                "/data/in.mkv", "/data/out.mp4", FFMPEG, crf=20
            )
        with self.assertRaises(ValueError):
            build_transcode_argv(
                "/data/in.mkv",
                "/data/out.mp4",
                FFMPEG,
                audio_codec="copy",
                audio_bitrate_kbps=128,
            )
        with self.assertRaises(ValueError):
            build_transcode_argv(
                "/data/in.mkv", "/data/out.mp4", FFMPEG, video_codec="vp9"
            )


class ToolResolutionTests(unittest.TestCase):
    def test_missing_tools_raise_clear_error(self):
        env = {"PATH": "/definitely-not-a-real-dir", "FFMPEG_BIN": "", "FFPROBE_BIN": ""}
        with self.assertRaises(MediaToolUnavailable) as raised:
            resolve_media_tool_paths(env)
        self.assertEqual(raised.exception.code, "MEDIA_TOOL_UNAVAILABLE")
        self.assertIn("FFMPEG_BIN", raised.exception.message)

    def test_describe_media_tools_never_raises(self):
        env = {"PATH": "/definitely-not-a-real-dir", "FFMPEG_BIN": "", "FFPROBE_BIN": ""}
        status = describe_media_tools(env)
        self.assertFalse(status.ffmpeg_available)
        self.assertFalse(status.ffprobe_available)
        self.assertIsNotNone(status.error)

    def test_injected_paths_are_used(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            ffmpeg = _write_fake_tool(temp_dir, "fake_ffmpeg", "exit 0")
            ffprobe = _write_fake_tool(temp_dir, "fake_ffprobe", "exit 0")
            env = {"PATH": "", "FFMPEG_BIN": ffmpeg, "FFPROBE_BIN": ffprobe}
            paths = resolve_media_tool_paths(env)
            self.assertEqual(paths.ffmpeg, ffmpeg)
            self.assertEqual(paths.ffprobe, ffprobe)
            status = describe_media_tools(env)
            self.assertTrue(status.ffmpeg_available)
            self.assertEqual(status.ffmpeg_source, "env")

    def test_bad_injected_path_is_rejected(self):
        env = {
            "PATH": "",
            "FFMPEG_BIN": "/definitely-not-a-real-dir/ffmpeg",
            "FFPROBE_BIN": "/definitely-not-a-real-dir/ffprobe",
        }
        with self.assertRaises(MediaToolUnavailable):
            resolve_media_tool_paths(env)

    def test_production_resolution_does_not_fall_back_to_path(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            _write_fake_tool(temp_dir, "ffmpeg", "exit 0")
            _write_fake_tool(temp_dir, "ffprobe", "exit 0")
            with self.assertRaises(MediaToolUnavailable) as raised:
                resolve_media_tool_paths({"PATH": temp_dir})
            self.assertEqual(raised.exception.code, "MEDIA_TOOL_UNAVAILABLE")
            self.assertIn("FFMPEG_BIN", raised.exception.message)

    @unittest.skipIf(os.name == "nt", "fake tools use POSIX shell")
    def test_describe_media_tools_labels_path_fallback_for_diagnostics(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            _write_fake_tool(temp_dir, "ffmpeg", "exit 0")
            _write_fake_tool(temp_dir, "ffprobe", "exit 0")
            status = describe_media_tools({"PATH": temp_dir})
            self.assertTrue(status.ffmpeg_available)
            self.assertTrue(status.ffprobe_available)
            self.assertEqual(status.ffmpeg_source, "path")
            self.assertEqual(status.ffprobe_source, "path")


@unittest.skipIf(os.name == "nt", "fake tools use POSIX shell")
class FfmpegAdapterExecutionTests(unittest.TestCase):
    def setUp(self):
        self._temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self._temp_dir.cleanup)
        self.temp = Path(self._temp_dir.name)

    def _adapter(self, ffmpeg_body: str) -> FfmpegAdapter:
        ffmpeg = _write_fake_tool(self._temp_dir.name, "fake_ffmpeg", ffmpeg_body)
        ffprobe = _write_fake_tool(
            self._temp_dir.name,
            "fake_ffprobe",
            "printf '%s\\n' '{\"format\": {\"format_name\": \"mov,mp4\"}}'",
        )
        return FfmpegAdapter(MediaToolPaths(ffmpeg=ffmpeg, ffprobe=ffprobe))

    def test_probe_parses_json_output(self):
        adapter = self._adapter("exit 0")
        data = adapter.probe(str(self.temp / "in.mp4"))
        self.assertEqual(data["format"]["format_name"], "mov,mp4")

    def test_transcode_success(self):
        adapter = self._adapter("exit 0")
        result = adapter.transcode(str(self.temp / "in.mp4"), str(self.temp / "out.mp4"))
        self.assertEqual(result.exit_code, 0)
        self.assertEqual(result.operation, "transcode")

    def test_nonzero_exit_raises_media_tool_failed(self):
        adapter = self._adapter("echo boom >&2; exit 3")
        with self.assertLogs("yaomustudio_runner.ffmpeg", level="WARNING"):
            with self.assertRaises(MediaToolFailed) as raised:
                adapter.mix(
                    [AudioMixInput(str(self.temp / "a.wav"))],
                    str(self.temp / "out.wav"),
                )
        self.assertEqual(raised.exception.exit_code, 3)

    def test_media_tool_stderr_log_drops_raw_content(self):
        body = (
            "printf '%s\\n' '虚构字幕：这是不该落盘的一句台词' "
            "'FLASHCLI_TOKEN=虚构密钥' 'metadata={\"title\":\"虚构元数据\"}' >&2\n"
            "exit 3"
        )
        adapter = self._adapter(body)
        with self.assertLogs("yaomustudio_runner.ffmpeg", level="WARNING") as captured:
            with self.assertRaises(MediaToolFailed) as raised:
                adapter.transcode(str(self.temp / "in.mp4"), str(self.temp / "out.mp4"))
        self.assertEqual(raised.exception.exit_code, 3)
        joined = "\n".join(captured.output)
        for marker in ("虚构字幕", "虚构密钥", "虚构元数据"):
            self.assertNotIn(marker, joined)
        self.assertIn("媒体工具 stderr 输出已省略", joined)
        self.assertIn("transcode", joined)
        self.assertIn("3", joined)

    def test_media_metadata_failure_log_drops_path_and_exception_text(self):
        adapter = self._adapter("exit 0")
        raw = (
            "虚构字幕：这是不该落盘的一句台词 "
            "FLASHCLI_TOKEN=虚构密钥 /mnt/d/private/out.mp4"
        )
        with mock.patch.object(adapter, "probe", side_effect=MediaToolError(raw)):
            with self.assertLogs("yaomustudio_runner.ffmpeg", level="WARNING") as captured:
                self.assertIsNone(adapter._media_metadata(str(self.temp / "out.mp4")))
        joined = "\n".join(captured.output)
        for marker in ("虚构字幕", "虚构密钥", "/mnt/d/private", str(self.temp)):
            self.assertNotIn(marker, joined)
        self.assertIn("产物媒体探测失败", joined)
        self.assertIn("MEDIA_TOOL_FAILED", joined)

    def test_timeout_kills_media_process(self):
        adapter = self._adapter("sleep 30")
        with self.assertRaises(MediaToolTimeout) as raised:
            adapter.transcode(
                str(self.temp / "in.mp4"),
                str(self.temp / "out.mp4"),
                timeout_s=0.2,
            )
        self.assertTrue(raised.exception.kill_confirmed)

    def test_unconfirmed_tree_stop_retains_owner_and_blocks_next_command(self):
        adapter = self._adapter("exit 0")
        proc = mock.Mock(pid=12345, returncode=None)
        proc.communicate.side_effect = [subprocess.TimeoutExpired("media", 0.1), (b"", b"")]
        with mock.patch("yaomustudio_runner.ffmpeg.subprocess.Popen", return_value=proc) as start:
            with mock.patch("yaomustudio_runner.ffmpeg.terminate_process_tree", return_value=False):
                with self.assertRaises(MediaToolStopUnconfirmed):
                    adapter.transcode(str(self.temp / "in.mp4"), str(self.temp / "out.mp4"), timeout_s=0.1)
                self.assertFalse(adapter.cancel())
                with self.assertRaises(MediaToolStopUnconfirmed):
                    adapter.transcode(str(self.temp / "in.mp4"), str(self.temp / "other.mp4"))
        self.assertEqual(start.call_count, 1)

    def test_windows_run_owns_job_and_passes_it_to_tree_check(self):
        adapter = self._adapter("exit 0")
        proc = mock.Mock(pid=12345, returncode=0)
        proc.communicate.return_value = (b"", b"")
        proc.poll.return_value = 0
        job = mock.Mock()
        job.close.return_value = True
        with (
            mock.patch.object(ffmpeg_module.os, "name", "nt"),
            mock.patch("yaomustudio_runner.ffmpeg.subprocess.Popen", return_value=proc),
            mock.patch(
                "yaomustudio_runner.ffmpeg.attach_windows_process_job",
                return_value=job,
            ),
            mock.patch(
                "yaomustudio_runner.ffmpeg.terminate_process_tree",
                return_value=True,
            ) as terminate,
        ):
            adapter.transcode(
                str(self.temp / "in.mp4"),
                str(self.temp / "out.mp4"),
            )
        self.assertIs(terminate.call_args.kwargs["windows_job"], job)
        job.close.assert_called_once_with()


if __name__ == "__main__":
    unittest.main()
