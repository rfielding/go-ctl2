PANDOC ?= pandoc
MMDC ?= $(if $(wildcard .tools/node_modules/.bin/mmdc),$(abspath .tools/node_modules/.bin/mmdc),mmdc)
GO ?= $(if $(wildcard /snap/go/current/bin/go),/snap/go/current/bin/go,go)
GOCACHE ?= /tmp/go-ctl2-gocache
GO_CMD := env GOCACHE=$(GOCACHE) $(GO)

DOCS_DIR := docs
BUILD_DIR := $(DOCS_DIR)/build
GENERATED_DIR := $(DOCS_DIR)/generated
MERMAID_DIR := $(DOCS_DIR)/mermaid
CSS_FILE := $(DOCS_DIR)/dark.css
MERMAID_CONFIG := $(DOCS_DIR)/mermaid-config.json
MERMAID_HEADER := $(DOCS_DIR)/mermaid-header.html

DOC_SRC := $(DOCS_DIR)/ir.md
DOC_BUILD_SRC := $(BUILD_DIR)/ir.generated.md
HTML_OUT := $(BUILD_DIR)/ir.html
PLOT_SNIPPETS := $(GENERATED_DIR)/plot_sections.md
LANGUAGE_SNIPPETS := $(GENERATED_DIR)/language_sections.md
EXAMPLE_SNIPPETS := $(GENERATED_DIR)/example_sections.md
PLOT_STAMP := $(GENERATED_DIR)/xyplots.stamp

MERMAID_SRC := $(wildcard $(MERMAID_DIR)/*.mmd)
MERMAID_SVG := $(patsubst $(MERMAID_DIR)/%.mmd,$(GENERATED_DIR)/%.svg,$(MERMAID_SRC))
DOC_ASSETS := $(PLOT_STAMP) $(LANGUAGE_SNIPPETS) $(EXAMPLE_SNIPPETS)

.PHONY: all docs diagrams test serve-docs clean

all: test docs

test:
	$(GO_CMD) test ./...

docs: $(DOC_BUILD_SRC) $(HTML_OUT)

diagrams: $(MERMAID_SVG)

$(HTML_OUT): $(DOC_BUILD_SRC) $(DOC_ASSETS) $(CSS_FILE) $(MERMAID_HEADER) | $(BUILD_DIR)
	$(PANDOC) --standalone --toc --css ../dark.css --include-in-header $(MERMAID_HEADER) --from markdown --to html5 -o $@ $(DOC_BUILD_SRC)

$(DOC_BUILD_SRC): $(DOC_SRC) $(PLOT_SNIPPETS) $(LANGUAGE_SNIPPETS) $(EXAMPLE_SNIPPETS) | $(BUILD_DIR)
	python3 scripts/build_doc.py $(DOC_SRC) $(PLOT_SNIPPETS) $(LANGUAGE_SNIPPETS) $(EXAMPLE_SNIPPETS) $(DOC_BUILD_SRC)

$(GENERATED_DIR)/%.svg: $(MERMAID_DIR)/%.mmd $(MERMAID_CONFIG) | $(GENERATED_DIR)
	@if command -v $(MMDC) >/dev/null 2>&1; then \
		$(MMDC) -i $< -o $@ -b transparent -c $(MERMAID_CONFIG); \
	else \
		printf '%s\n' '<svg xmlns="http://www.w3.org/2000/svg" width="960" height="540" viewBox="0 0 960 540">' > $@; \
		printf '%s\n' '<rect width="100%%" height="100%%" fill="#151922"/>' >> $@; \
		printf '%s\n' '<text x="24" y="40" font-family="monospace" font-size="22" fill="#e7edf5">Mermaid source preview (install mmdc for rendered SVG)</text>' >> $@; \
		printf '%s\n' '<foreignObject x="24" y="64" width="912" height="452"><div xmlns="http://www.w3.org/1999/xhtml" style="font-family: monospace; font-size: 18px; white-space: pre; color: #e7edf5;">' >> $@; \
		sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g' $< >> $@; \
		printf '%s\n' '</div></foreignObject></svg>' >> $@; \
	fi

$(PLOT_STAMP): scripts/generate_xyplot.py $(wildcard *.go) | $(GENERATED_DIR)
	python3 scripts/generate_xyplot.py $(GENERATED_DIR) $(PLOT_SNIPPETS)
	touch $(PLOT_STAMP)

$(PLOT_SNIPPETS): $(PLOT_STAMP)

$(LANGUAGE_SNIPPETS): $(wildcard *.go) | $(GENERATED_DIR)
	$(GO_CMD) run . doc-language-sections $(LANGUAGE_SNIPPETS)

$(EXAMPLE_SNIPPETS): $(wildcard *.go) | $(GENERATED_DIR)
	$(GO_CMD) run . doc-example-sections $(EXAMPLE_SNIPPETS)

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

$(GENERATED_DIR):
	mkdir -p $(GENERATED_DIR)

clean:
	rm -rf $(BUILD_DIR) $(GENERATED_DIR)

serve-docs: docs
	$(GO_CMD) run . serve 127.0.0.1:8000
