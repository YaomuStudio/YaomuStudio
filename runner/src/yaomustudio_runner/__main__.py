"""``python -m yaomustudio_runner`` 入口。"""

import sys

from .main import main

if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
