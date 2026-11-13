"""测试公共引导：把 ``src/`` 加入导入路径（不依赖第三方测试库）。"""

from __future__ import annotations

import sys
from pathlib import Path

SRC = Path(__file__).resolve().parents[1] / "src"
if str(SRC) not in sys.path:
    sys.path.insert(0, str(SRC))

RUNNER_ROOT = SRC.parent
