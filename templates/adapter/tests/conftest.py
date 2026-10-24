import sys
from pathlib import Path

# Lets the tests import the adapter before the package is installed anywhere.
sys.path.insert(0, str(Path(__file__).parents[1] / "src"))
