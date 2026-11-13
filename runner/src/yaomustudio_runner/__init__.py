"""曜幕执行适配器（Runner）。

按 docs/architecture.md：与 UI/Core 分进程，受控执行 FlashCLI / FFmpeg，
不进入 Core/UI 地址空间；native-abi 也留在子进程。
"""

__version__ = "0.1.0"
