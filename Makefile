PANDOC ?= pandoc
MMDC ?= mmdc

DOCS_DIR := docs
BUILD_DIR := $(DOCS_DIR)/build
GENERATED_DIR := $(DOCS_DIR)/generated
MERMAID_DIR := $(DOCS_DIR)/mermaid

DOC_SRC := $(DOCS_DIR)/ir.md
HTML_OUT := $(BUILD_DIR)/ir.html

MERMAID_SRC := $(wildcard $(MERMAID_DIR)/*.mmd)
MERMAID_SVG := $(patsubst $(MERMAID_DIR)/%.mmd,$(GENERATED_DIR)/%.svg,$(MERMAID_SRC))

.PHONY: all docs diagrams test clean

all: test docs

test:
	go test ./...

docs: $(HTML_OUT)

diagrams: $(MERMAID_SVG)

$(HTML_OUT): $(DOC_SRC) | $(BUILD_DIR)
	$(PANDOC) --standalone --toc --from markdown --to html5 -o $@ $<

$(GENERATED_DIR)/%.svg: $(MERMAID_DIR)/%.mmd | $(GENERATED_DIR)
	$(MMDC) -i $< -o $@

$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

$(GENERATED_DIR):
	mkdir -p $(GENERATED_DIR)

clean:
	rm -rf $(BUILD_DIR) $(GENERATED_DIR)
