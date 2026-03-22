#!/usr/bin/env python3

from pathlib import Path
import sys
from html import escape


def title(name: str) -> str:
    return name.replace("_", " ").replace("-", " ").title()


def main() -> int:
    if len(sys.argv) != 3:
        raise SystemExit("usage: generate_diagram_sections.py <mermaid_dir> <out>")

    mermaid_dir = Path(sys.argv[1])
    out = Path(sys.argv[2])

    parts: list[str] = []
    for path in sorted(mermaid_dir.glob("*.mmd")):
        stem = path.stem
        src = path.read_text().rstrip()
        parts.append(f"### {title(stem)}\n")
        parts.append(f"![{title(stem)}](../generated/{stem}.svg)\n")
        parts.append("<details>")
        parts.append(f"<summary>Mermaid Source: <code>{path.name}</code></summary>")
        parts.append('<pre><code class="language-mermaid">')
        parts.append(escape(src))
        parts.append("</code></pre>")
        parts.append("</details>\n")

    out.write_text("\n".join(parts) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
