#!/usr/bin/env python3

from __future__ import annotations

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

def go_binary() -> str:
    direct_snap = Path("/snap/go/current/bin/go")
    if direct_snap.exists() and os.access(direct_snap, os.X_OK):
        return str(direct_snap)
    candidate = shutil.which("go")
    if candidate is None:
        raise RuntimeError("go executable not found")
    return candidate


def go_env() -> dict[str, str]:
    env = dict(os.environ)
    env.setdefault("GOCACHE", "/tmp/go-ctl2-gocache")
    return env


def load_manifest():
    go = go_binary()
    with tempfile.NamedTemporaryFile() as tmp:
        subprocess.run(
            [
                go,
                "test",
                "./...",
                "-run",
                "TestEmitDocPlotDataForDocs$",
                "-count=1",
                "-args",
                "--doc-plot-mode=manifest",
                f"--doc-plot-out={tmp.name}",
            ],
            check=True,
            capture_output=True,
            text=True,
            env=go_env(),
        )
        return json.loads(Path(tmp.name).read_text())


def load_plot_data(name: str):
    go = go_binary()
    with tempfile.NamedTemporaryFile() as tmp:
        subprocess.run(
            [
                go,
                "test",
                "./...",
                "-run",
                "TestEmitDocPlotDataForDocs$",
                "-count=1",
                "-args",
                "--doc-plot-mode=data",
                f"--doc-plot-name={name}",
                f"--doc-plot-out={tmp.name}",
            ],
            check=True,
            capture_output=True,
            text=True,
            env=go_env(),
        )
        return json.loads(Path(tmp.name).read_text())


def format_number(value: float) -> str:
    if abs(value - round(value)) < 1e-9:
        return str(int(round(value)))
    return format(value, "g")


def render_plot_mermaid(data: dict) -> str:
    values = [point["Value"] for point in data["series"]]
    steps = ", ".join(str(point["Step"]) for point in data["series"])
    min_value = min([0.0] + values)
    max_value = max([0.0] + values)
    if min_value == max_value:
        if min_value == 0:
            max_value = 1.0
        elif min_value > 0:
            min_value = 0.0
        else:
            max_value = 0.0

    series = ", ".join(format_number(point["Value"]) for point in data["series"])
    return "\n".join(
        [
            "xychart-beta",
            f'    title "{data["title"]}"',
            f'    x-axis "applied runtime step" [{steps}]',
            f'    y-axis "{data["ylabel"]}" {format_number(min_value)} --> {format_number(max_value)}',
            f"    line [{series}]",
        ]
    )


def title(name: str) -> str:
    return name.replace("_", " ").replace("-", " ").title()


def main() -> int:
    if len(sys.argv) != 3:
        raise SystemExit("usage: generate_xyplot.py <generated_dir> <sections_out>")

    generated_dir = Path(sys.argv[1])
    sections_out = Path(sys.argv[2])
    generated_dir.mkdir(parents=True, exist_ok=True)

    manifest = load_manifest()
    parts: list[str] = []
    for entry in manifest:
        data = load_plot_data(entry["name"])

        parts.append(f"### {title(entry['name'])}\n")
        parts.append("```mermaid")
        parts.append(render_plot_mermaid(data))
        parts.append("```\n")
        parts.append("<details>")
        parts.append(f"<summary>XY Plot Source: <code>{entry['name']}</code></summary>")
        parts.append('<pre><code class="language-lisp">')
        parts.append(
            (
                f'(xyplot {entry["name"]}\n'
                f'  (title "{data["title"]}")\n'
                f'  (steps {entry["steps"]})\n'
                f'  (metric {entry.get("metric", "unknown")}))'
            ).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        )
        parts.append("</code></pre>")
        parts.append("</details>\n")

    sections_out.write_text("\n".join(parts) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
