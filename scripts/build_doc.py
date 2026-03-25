#!/usr/bin/env python3

from pathlib import Path
import sys


def main() -> int:
    if len(sys.argv) != 8:
        raise SystemExit("usage: build_doc.py <doc_src> <diagram_snippets> <plot_snippets> <assertion_snippets> <language_snippets> <example_snippets> <out>")

    doc_src = Path(sys.argv[1]).read_text()
    diagram_snippets = Path(sys.argv[2]).read_text()
    plot_snippets = Path(sys.argv[3]).read_text()
    assertion_snippets = Path(sys.argv[4]).read_text()
    language_snippets = Path(sys.argv[5]).read_text()
    example_snippets = Path(sys.argv[6]).read_text()
    built = doc_src.replace("{{DIAGRAM_SECTIONS}}", diagram_snippets)
    built = built.replace("{{PLOT_SECTIONS}}", plot_snippets)
    built = built.replace("{{ASSERTION_SECTIONS}}", assertion_snippets)
    built = built.replace("{{LANGUAGE_SECTIONS}}", language_snippets)
    built = built.replace("{{EXAMPLE_SECTIONS}}", example_snippets)
    Path(sys.argv[7]).write_text(built)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
