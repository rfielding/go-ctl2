#!/usr/bin/env python3

from pathlib import Path
import sys


def main() -> int:
    if len(sys.argv) != 4:
        raise SystemExit("usage: build_doc.py <doc_src> <snippets_src> <out>")

    doc_src = Path(sys.argv[1]).read_text()
    snippets = Path(sys.argv[2]).read_text()
    Path(sys.argv[3]).write_text(doc_src.replace("{{DIAGRAM_SECTIONS}}", snippets))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
