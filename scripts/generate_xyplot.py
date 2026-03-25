#!/usr/bin/env python3

from __future__ import annotations

import json
import os
from html import escape
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


BG = "#0f1115"
PANEL = "#151922"
TEXT = "#e7edf5"
MUTED = "#a9b7c8"
BORDER = "#2a3140"
LINE = "#8bc3ff"


def go_binary() -> str:
    direct_snap = Path("/snap/go/current/bin/go")
    if direct_snap.exists() and os.access(direct_snap, os.X_OK):
        return str(direct_snap)
    candidate = shutil.which("go")
    if candidate is None:
        raise RuntimeError("go executable not found")
    return candidate


def polyline(points, x0, y0, w, h, xmax, ymax):
    coords = []
    for x, y in points:
        px = x0 + (x / xmax) * w
        py = y0 + h - (y / ymax) * h
        coords.append(f"{px:.1f},{py:.1f}")
    return " ".join(coords)


def circles(points, color, x0, y0, w, h, xmax, ymax):
    out = []
    for x, y in points:
        cx = x0 + (x / xmax) * w
        cy = y0 + h - (y / ymax) * h
        out.append(f'  <circle cx="{cx:.1f}" cy="{cy:.1f}" r="3.2" fill="{color}"/>')
    return "\n".join(out)


def axis_ticks(count):
    if count <= 10:
        return list(range(0, count + 1))
    step = max(10, count // 10)
    ticks = list(range(0, count + 1, step))
    if ticks[-1] != count:
        ticks.append(count)
    return ticks


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
            env=os.environ,
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
            env=os.environ,
        )
        return json.loads(Path(tmp.name).read_text())


def render_plot_svg(data: dict) -> str:
    series = [(point["Step"], point["Value"]) for point in data["series"]]
    xmax = max(1, int(data["steps"]))
    ymax = max(1, int(max([point[1] for point in series] + [1])))

    width = 960
    height = 420
    margin_left = 84
    margin_right = 24
    margin_top = 56
    margin_bottom = 54
    plot_w = width - margin_left - margin_right
    plot_h = height - margin_top - margin_bottom

    grid_lines = []
    for y in range(0, ymax + 1):
        py = margin_top + plot_h - (y / ymax) * plot_h
        grid_lines.append(
            f'  <line x1="{margin_left}" y1="{py:.1f}" x2="{margin_left + plot_w}" y2="{py:.1f}" stroke="{BORDER}" stroke-width="1"/>'
        )

    x_labels = []
    for tick in axis_ticks(xmax):
        px = margin_left + (tick / xmax) * plot_w
        grid_lines.append(
            f'  <line x1="{px:.1f}" y1="{margin_top}" x2="{px:.1f}" y2="{margin_top + plot_h}" stroke="{BORDER}" stroke-width="1"/>'
        )
        x_labels.append(
            f'  <text x="{px:.1f}" y="{margin_top + plot_h + 28}" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">{tick}</text>'
        )

    y_labels = []
    tick_step = max(1, ymax // 5)
    for y in range(0, ymax + 1, tick_step):
        py = margin_top + plot_h - (y / ymax) * plot_h
        y_labels.append(
            f'  <text x="{margin_left - 18}" y="{py + 6:.1f}" text-anchor="end" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">{y}</text>'
        )

    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">
  <rect width="100%" height="100%" fill="{BG}"/>
  <rect x="20" y="20" width="{width - 40}" height="{height - 40}" rx="14" fill="{PANEL}" stroke="{BORDER}"/>
  <text x="{margin_left}" y="38" fill="{TEXT}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="22">{escape(data["title"])}</text>
  <text x="{margin_left}" y="58" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">{escape(data["subtitle"])}</text>

  <line x1="{margin_left}" y1="{margin_top}" x2="{margin_left}" y2="{margin_top + plot_h}" stroke="{MUTED}" stroke-width="1.5"/>
  <line x1="{margin_left}" y1="{margin_top + plot_h}" x2="{margin_left + plot_w}" y2="{margin_top + plot_h}" stroke="{MUTED}" stroke-width="1.5"/>
{chr(10).join(grid_lines)}
{chr(10).join(y_labels)}
{chr(10).join(x_labels)}

  <text x="{margin_left + plot_w / 2}" y="{height - 20}" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">applied runtime step</text>
  <text x="26" y="{margin_top + plot_h / 2}" transform="rotate(-90 26 {margin_top + plot_h / 2})" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">{escape(data["ylabel"])}</text>

  <polyline fill="none" stroke="{LINE}" stroke-width="3" points="{polyline(series, margin_left, margin_top, plot_w, plot_h, xmax, ymax)}"/>
{circles(series, LINE, margin_left, margin_top, plot_w, plot_h, xmax, ymax)}

  <line x1="{width - 260}" y1="78" x2="{width - 224}" y2="78" stroke="{LINE}" stroke-width="3"/>
  <text x="{width - 214}" y="82" fill="{TEXT}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">{escape(data["legend"])}</text>
</svg>
"""


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
        svg_path = generated_dir / data["image_name"]
        svg_path.write_text(render_plot_svg(data))

        parts.append(f"### {title(entry['name'])}\n")
        parts.append(f"![{escape(data['title'])}](../generated/{data['image_name']})\n")
        parts.append("<details>")
        parts.append(f"<summary>XY Plot Source: <code>{entry['name']}</code></summary>")
        parts.append('<pre><code class="language-lisp">')
        parts.append(
            escape(
                f'(xyplot {entry["name"]}\n'
                f'  (title "{data["title"]}")\n'
                f'  (steps {entry["steps"]})\n'
                f'  (metric {entry.get("metric", "unknown")}))'
            )
        )
        parts.append("</code></pre>")
        parts.append("</details>\n")

    sections_out.write_text("\n".join(parts) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
