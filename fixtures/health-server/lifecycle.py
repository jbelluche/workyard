#!/usr/bin/env python3
import json
import sys
from datetime import datetime, timezone
from pathlib import Path


def main():
    name = sys.argv[1] if len(sys.argv) > 1 else "unknown"
    root = Path(".workyard-fixture")
    root.mkdir(exist_ok=True)
    marker = root / f"{name}.json"
    payload = {
        "name": name,
        "time": datetime.now(timezone.utc).isoformat(),
    }
    marker.write_text(json.dumps(payload, sort_keys=True) + "\n")
    print(f"{name} complete", flush=True)


if __name__ == "__main__":
    main()

