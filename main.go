package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type Kind uint8

const (
	KindInvalid Kind = iota
	KindSymbol
	KindNumber
	KindString
	KindBool
	KindList
)

type Value struct {
	Kind  Kind
	Text  string
	Items []Value
}

func Symbol(text string) Value {
	return Value{Kind: KindSymbol, Text: text}
}

func Number(text string) Value {
	return Value{Kind: KindNumber, Text: text}
}

func String(text string) Value {
	return Value{Kind: KindString, Text: text}
}

func Bool(text string) Value {
	return Value{Kind: KindBool, Text: text}
}

func List(items ...Value) Value {
	return Value{Kind: KindList, Items: items}
}

func Read(input string) (Value, error) {
	tokens, err := tokenize(input)
	if err != nil {
		return Value{}, err
	}

	p := parser{tokens: tokens}
	v, err := p.parseValue()
	if err != nil {
		return Value{}, err
	}
	if p.hasNext() {
		return Value{}, fmt.Errorf("unexpected token %q", p.peek().text)
	}
	return v, nil
}

func MustRead(input string) Value {
	v, err := Read(input)
	if err != nil {
		panic(err)
	}
	return v
}

func (v Value) String() string {
	switch v.Kind {
	case KindSymbol:
		return v.Text
	case KindNumber:
		return v.Text
	case KindString:
		return strconv.Quote(v.Text)
	case KindBool:
		return v.Text
	case KindList:
		parts := make([]string, 0, len(v.Items))
		for _, item := range v.Items {
			parts = append(parts, item.String())
		}
		return "(" + strings.Join(parts, " ") + ")"
	default:
		return "<invalid>"
	}
}

type token struct {
	kind      string
	text      string
	tightLeft bool
}

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) parseValue() (Value, error) {
	if !p.hasNext() {
		return Value{}, fmt.Errorf("unexpected end of input")
	}

	switch p.peek().kind {
	case "lparen":
		return p.parseSExpr()
	case "symbol", "number", "string", "bool":
		if p.canStartMExpr() {
			return p.parseMExpr()
		}
		return p.parseAtom()
	default:
		return Value{}, fmt.Errorf("unexpected token %q", p.peek().text)
	}
}

func (p *parser) parseSExpr() (Value, error) {
	if !p.match("lparen") {
		return Value{}, fmt.Errorf("expected '('")
	}

	var items []Value
	for {
		if !p.hasNext() {
			return Value{}, fmt.Errorf("unterminated list")
		}
		if p.match("rparen") {
			return List(items...), nil
		}

		var (
			item Value
			err  error
		)
		if len(items) == 0 {
			item, err = p.parseHeadValue()
		} else {
			item, err = p.parseValue()
		}
		if err != nil {
			return Value{}, err
		}
		items = append(items, item)
	}
}

func (p *parser) parseHeadValue() (Value, error) {
	if !p.hasNext() {
		return Value{}, fmt.Errorf("unexpected end of input")
	}

	switch p.peek().kind {
	case "symbol", "number", "string", "bool":
		return p.parseAtom()
	default:
		return p.parseValue()
	}
}

func (p *parser) parseMExpr() (Value, error) {
	head, err := p.parseAtom()
	if err != nil {
		return Value{}, err
	}
	if !p.match("lparen") {
		return Value{}, fmt.Errorf("expected '(' after %q", head.Text)
	}

	items := []Value{head}
	for {
		if !p.hasNext() {
			return Value{}, fmt.Errorf("unterminated m-expression")
		}
		if p.match("rparen") {
			return List(items...), nil
		}

		item, err := p.parseValue()
		if err != nil {
			return Value{}, err
		}
		items = append(items, item)

		if p.match("comma") {
			continue
		}
		if p.peek().kind == "rparen" {
			continue
		}
	}
}

func (p *parser) parseAtom() (Value, error) {
	if !p.hasNext() {
		return Value{}, fmt.Errorf("unexpected end of input")
	}

	tok := p.peek()
	p.pos++

	switch tok.kind {
	case "symbol":
		return Symbol(tok.text), nil
	case "number":
		return Number(tok.text), nil
	case "string":
		return String(tok.text), nil
	case "bool":
		return Bool(tok.text), nil
	default:
		return Value{}, fmt.Errorf("unexpected atom %q", tok.text)
	}
}

func (p *parser) canStartMExpr() bool {
	return p.pos+1 < len(p.tokens) &&
		p.tokens[p.pos+1].kind == "lparen" &&
		p.tokens[p.pos+1].tightLeft
}

func (p *parser) hasNext() bool {
	return p.pos < len(p.tokens)
}

func (p *parser) peek() token {
	return p.tokens[p.pos]
}

func (p *parser) match(kind string) bool {
	if !p.hasNext() || p.tokens[p.pos].kind != kind {
		return false
	}
	p.pos++
	return true
}

func tokenize(input string) ([]token, error) {
	var out []token
	lastEnd := 0

	for i := 0; i < len(input); {
		ch := rune(input[i])

		if unicode.IsSpace(ch) {
			i++
			continue
		}

		switch ch {
		case '(':
			out = append(out, token{
				kind:      "lparen",
				text:      "(",
				tightLeft: i == lastEnd,
			})
			i++
			lastEnd = i
			continue
		case ')':
			out = append(out, token{
				kind:      "rparen",
				text:      ")",
				tightLeft: i == lastEnd,
			})
			i++
			lastEnd = i
			continue
		case ',':
			out = append(out, token{
				kind:      "comma",
				text:      ",",
				tightLeft: i == lastEnd,
			})
			i++
			lastEnd = i
			continue
		case '"':
			start := i + 1
			i++
			for i < len(input) && input[i] != '"' {
				i++
			}
			if i >= len(input) {
				return nil, fmt.Errorf("unterminated string literal")
			}
			out = append(out, token{
				kind:      "string",
				text:      input[start:i],
				tightLeft: start-1 == lastEnd,
			})
			i++
			lastEnd = i
			continue
		}

		if unicode.IsDigit(ch) || (ch == '-' && i+1 < len(input) && unicode.IsDigit(rune(input[i+1]))) {
			start := i
			i++
			for i < len(input) && unicode.IsDigit(rune(input[i])) {
				i++
			}
			out = append(out, token{
				kind:      "number",
				text:      input[start:i],
				tightLeft: start == lastEnd,
			})
			lastEnd = i
			continue
		}

		if isSymbolStart(ch) {
			start := i
			i++
			for i < len(input) && isSymbolPart(rune(input[i])) {
				i++
			}
			text := input[start:i]
			switch text {
			case "true", "false":
				out = append(out, token{
					kind:      "bool",
					text:      text,
					tightLeft: start == lastEnd,
				})
			default:
				out = append(out, token{
					kind:      "symbol",
					text:      text,
					tightLeft: start == lastEnd,
				})
			}
			lastEnd = i
			continue
		}

		return nil, fmt.Errorf("unexpected character %q", string(ch))
	}

	return out, nil
}

func isSymbolStart(ch rune) bool {
	return unicode.IsLetter(ch) || strings.ContainsRune("+-*/<>=!?_", ch)
}

func isSymbolPart(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || strings.ContainsRune("+-*/<>=!?_", ch)
}

func main() {}
