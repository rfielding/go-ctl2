#!/usr/bin/env python3

from __future__ import annotations

import json
from pathlib import Path
import subprocess
import sys


BG = "#0f1115"
PANEL = "#151922"
TEXT = "#e7edf5"
MUTED = "#a9b7c8"
BORDER = "#2a3140"
SEND = "#8bc3ff"
RECV = "#79d2a6"


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


def difference_series(sends, recvs, xmax):
    send_map = {step: value for step, value in sends}
    recv_map = {step: value for step, value in recvs}
    out = []
    last_send = 0.0
    last_recv = 0.0
    for step in range(1, xmax + 1):
        if step in send_map:
            last_send = send_map[step]
        if step in recv_map:
            last_recv = recv_map[step]
        out.append((step, last_send - last_recv))
    return out


def axis_ticks(count):
    if count <= 10:
        return list(range(1, count + 1))
    step = max(10, count // 10)
    ticks = list(range(0, count + 1, step))
    if ticks[-1] != count:
        ticks.append(count)
    if ticks[0] == 0:
        ticks = ticks[1:]
    return ticks


def load_runtime_series(steps):
    proc = subprocess.run(
        ["go", "run", ".", "doc-xyplot-data", str(steps)],
        check=True,
        capture_output=True,
        text=True,
    )
    return json.loads(proc.stdout)


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: generate_xyplot.py <out.svg>")

    out = Path(sys.argv[1])
    data = load_runtime_series(100)
    sends = [(point["Step"], point["Value"]) for point in data["sends"]]
    recvs = [(point["Step"], point["Value"]) for point in data["receives"]]
    xmax = max(1, int(data["steps"]))
    backlog = difference_series(sends, recvs, xmax)
    ymax = max(1, int(max([point[1] for point in backlog] + [1])))

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
    for y in range(0, ymax + 1, max(1, ymax // 5)):
        py = margin_top + plot_h - (y / ymax) * plot_h
        y_labels.append(
            f'  <text x="{margin_left - 18}" y="{py + 6:.1f}" text-anchor="end" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">{y}</text>'
        )

    svg = f"""<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">
  <rect width="100%" height="100%" fill="{BG}"/>
  <rect x="20" y="20" width="{width - 40}" height="{height - 40}" rx="14" fill="{PANEL}" stroke="{BORDER}"/>
  <text x="{margin_left}" y="38" fill="{TEXT}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="22">Outstanding Messages By Step</text>
  <text x="{margin_left}" y="58" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">{data["subtitle"]}</text>

  <line x1="{margin_left}" y1="{margin_top}" x2="{margin_left}" y2="{margin_top + plot_h}" stroke="{MUTED}" stroke-width="1.5"/>
  <line x1="{margin_left}" y1="{margin_top + plot_h}" x2="{margin_left + plot_w}" y2="{margin_top + plot_h}" stroke="{MUTED}" stroke-width="1.5"/>
{chr(10).join(grid_lines)}
{chr(10).join(y_labels)}
{chr(10).join(x_labels)}

  <text x="{margin_left + plot_w / 2}" y="{height - 20}" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">applied runtime step</text>
  <text x="26" y="{margin_top + plot_h / 2}" transform="rotate(-90 26 {margin_top + plot_h / 2})" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">sent - received</text>

  <polyline fill="none" stroke="{SEND}" stroke-width="3" points="{polyline(backlog, margin_left, margin_top, plot_w, plot_h, xmax, ymax)}"/>
{circles(backlog, SEND, margin_left, margin_top, plot_w, plot_h, xmax, ymax)}

  <line x1="{width - 220}" y1="78" x2="{width - 184}" y2="78" stroke="{SEND}" stroke-width="3"/>
  <text x="{width - 174}" y="82" fill="{TEXT}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">outstanding = sends - receives</text>
</svg>
"""

    out.write_text(svg)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
