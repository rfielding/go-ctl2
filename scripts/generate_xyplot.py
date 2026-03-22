#!/usr/bin/env python3

from pathlib import Path
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


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: generate_xyplot.py <out.svg>")

    out = Path(sys.argv[1])

    # Client -> Relay -> Server example:
    # step 1: Client transition + send to Relay
    # step 2: Relay transition + recv + send to Server
    # step 3: Server transition + recv
    sends = [(1, 1), (2, 2), (3, 2)]
    recvs = [(1, 0), (2, 1), (3, 2)]

    width = 960
    height = 420
    margin_left = 84
    margin_right = 24
    margin_top = 56
    margin_bottom = 54
    plot_w = width - margin_left - margin_right
    plot_h = height - margin_top - margin_bottom
    xmax = 3
    ymax = 2

    svg = f"""<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">
  <rect width="100%" height="100%" fill="{BG}"/>
  <rect x="20" y="20" width="{width - 40}" height="{height - 40}" rx="14" fill="{PANEL}" stroke="{BORDER}"/>
  <text x="{margin_left}" y="38" fill="{TEXT}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="22">Message Counts By Step</text>
  <text x="{margin_left}" y="58" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">Same Client → Relay → Server example as the state and sequence diagrams.</text>

  <line x1="{margin_left}" y1="{margin_top}" x2="{margin_left}" y2="{margin_top + plot_h}" stroke="{MUTED}" stroke-width="1.5"/>
  <line x1="{margin_left}" y1="{margin_top + plot_h}" x2="{margin_left + plot_w}" y2="{margin_top + plot_h}" stroke="{MUTED}" stroke-width="1.5"/>

  <line x1="{margin_left}" y1="{margin_top + plot_h / 2}" x2="{margin_left + plot_w}" y2="{margin_top + plot_h / 2}" stroke="{BORDER}" stroke-width="1"/>
  <line x1="{margin_left + plot_w / 3}" y1="{margin_top}" x2="{margin_left + plot_w / 3}" y2="{margin_top + plot_h}" stroke="{BORDER}" stroke-width="1"/>
  <line x1="{margin_left + 2 * plot_w / 3}" y1="{margin_top}" x2="{margin_left + 2 * plot_w / 3}" y2="{margin_top + plot_h}" stroke="{BORDER}" stroke-width="1"/>

  <text x="{margin_left - 18}" y="{margin_top + plot_h + 6}" text-anchor="end" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">0</text>
  <text x="{margin_left - 18}" y="{margin_top + plot_h / 2 + 6}" text-anchor="end" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">1</text>
  <text x="{margin_left - 18}" y="{margin_top + 6}" text-anchor="end" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">2</text>

  <text x="{margin_left}" y="{margin_top + plot_h + 28}" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">1</text>
  <text x="{margin_left + plot_w / 3}" y="{margin_top + plot_h + 28}" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">2</text>
  <text x="{margin_left + 2 * plot_w / 3}" y="{margin_top + plot_h + 28}" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">3</text>

  <text x="{margin_left + plot_w / 2}" y="{height - 20}" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">scheduler step</text>
  <text x="26" y="{margin_top + plot_h / 2}" transform="rotate(-90 26 {margin_top + plot_h / 2})" text-anchor="middle" fill="{MUTED}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">cumulative messages</text>

  <polyline fill="none" stroke="{SEND}" stroke-width="3" points="{polyline(sends, margin_left, margin_top, plot_w, plot_h, xmax, ymax)}"/>
  <polyline fill="none" stroke="{RECV}" stroke-width="3" points="{polyline(recvs, margin_left, margin_top, plot_w, plot_h, xmax, ymax)}"/>

  <circle cx="{margin_left + (1 / xmax) * plot_w}" cy="{margin_top + plot_h - (1 / ymax) * plot_h}" r="4" fill="{SEND}"/>
  <circle cx="{margin_left + (2 / xmax) * plot_w}" cy="{margin_top + plot_h - (2 / ymax) * plot_h}" r="4" fill="{SEND}"/>
  <circle cx="{margin_left + (3 / xmax) * plot_w}" cy="{margin_top + plot_h - (2 / ymax) * plot_h}" r="4" fill="{SEND}"/>

  <circle cx="{margin_left + (1 / xmax) * plot_w}" cy="{margin_top + plot_h - (0 / ymax) * plot_h}" r="4" fill="{RECV}"/>
  <circle cx="{margin_left + (2 / xmax) * plot_w}" cy="{margin_top + plot_h - (1 / ymax) * plot_h}" r="4" fill="{RECV}"/>
  <circle cx="{margin_left + (3 / xmax) * plot_w}" cy="{margin_top + plot_h - (2 / ymax) * plot_h}" r="4" fill="{RECV}"/>

  <line x1="{width - 220}" y1="78" x2="{width - 184}" y2="78" stroke="{SEND}" stroke-width="3"/>
  <text x="{width - 174}" y="82" fill="{TEXT}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">sends</text>
  <line x1="{width - 220}" y1="102" x2="{width - 184}" y2="102" stroke="{RECV}" stroke-width="3"/>
  <text x="{width - 174}" y="106" fill="{TEXT}" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">receives</text>
</svg>
"""

    out.write_text(svg)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
