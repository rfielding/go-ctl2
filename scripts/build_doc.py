#!/usr/bin/env python3

from pathlib import Path
import sys


def main() -> int:
    if len(sys.argv) != 5:
        raise SystemExit("usage: build_doc.py <doc_src> <diagram_snippets> <plot_snippets> <out>")

    doc_src = Path(sys.argv[1]).read_text()
    diagram_snippets = Path(sys.argv[2]).read_text()
    plot_snippets = Path(sys.argv[3]).read_text()
    built = doc_src.replace("{{DIAGRAM_SECTIONS}}", diagram_snippets)
    built = built.replace("{{PLOT_SECTIONS}}", plot_snippets)
    Path(sys.argv[4]).write_text(built)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
