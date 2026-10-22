import sys
from pathlib import Path

# Family adapter packages live outside the engine package; make them importable in tests.
for src in (Path(__file__).parents[1] / "families").glob("*/src"):
    sys.path.insert(0, str(src))
