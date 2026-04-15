package channel

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// parseMarkdown parses standard markdown into a goldmark AST with GFM
// strikethrough support enabled.
func parseMarkdown(source []byte) ast.Node {
	md := goldmark.New(goldmark.WithExtensions(extension.Strikethrough))
	return md.Parser().Parse(text.NewReader(source))
}

// MarkdownToSlackMrkdwn converts standard markdown to Slack's mrkdwn format.
func MarkdownToSlackMrkdwn(md string) string {
	if md == "" {
		return ""
	}
	src := []byte(md)
	r := &slackRenderer{source: src}
	r.push()
	_ = ast.Walk(parseMarkdown(src), r.walk)
	return strings.TrimRight(r.result(), "\n")
}

// MarkdownToTelegramV2 converts standard markdown to Telegram MarkdownV2
// format, escaping non-formatting special characters outside code blocks.
func MarkdownToTelegramV2(md string) string {
	if md == "" {
		return ""
	}
	src := []byte(md)
	r := &telegramRenderer{source: src}
	r.push()
	_ = ast.Walk(parseMarkdown(src), r.walk)
	return strings.TrimRight(r.result(), "\n")
}

// --- buffer stack (used by both renderers for blockquote nesting) ---

type bufStack []*strings.Builder

func (s *bufStack) push()               { *s = append(*s, &strings.Builder{}) }
func (s *bufStack) w() *strings.Builder { return (*s)[len(*s)-1] }
func (s *bufStack) result() string      { return (*s)[0].String() }
func (s *bufStack) pop() string {
	last := (*s)[len(*s)-1]
	*s = (*s)[:len(*s)-1]
	return last.String()
}

// --- helpers ---

// isInTightList returns true if n is inside a ListItem whose parent List is tight.
func isInTightList(n ast.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if _, ok := p.(*ast.ListItem); ok {
			if list, ok := p.Parent().(*ast.List); ok {
				return list.IsTight
			}
		}
	}
	return false
}

// listItemPrefix returns "- " for unordered items or "N. " for ordered items.
func listItemPrefix(n *ast.ListItem) string {
	list, ok := n.Parent().(*ast.List)
	if !ok || !list.IsOrdered() {
		return "- "
	}
	num := list.Start
	for c := list.FirstChild(); c != nil && c != n; c = c.NextSibling() {
		num++
	}
	return fmt.Sprintf("%d. ", num)
}

// prefixLines prepends prefix to every line in s.
func prefixLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(prefix)
		b.WriteString(line)
	}
	return b.String()
}

// writeLines writes all segments from a Segments pointer to the builder.
func writeLines(b *strings.Builder, segs *text.Segments, source []byte) {
	for i := range segs.Len() {
		seg := segs.At(i)
		b.Write(seg.Value(source))
	}
}

// writeLinesEscaped writes segments with Telegram escaping applied.
func writeLinesEscaped(b *strings.Builder, segs *text.Segments, source []byte) {
	for i := range segs.Len() {
		seg := segs.At(i)
		b.WriteString(escapeTelegramText(string(seg.Value(source))))
	}
}

// telegramSpecial lists characters that must be escaped in Telegram MarkdownV2
// text (outside code blocks and formatting markers).
const telegramSpecial = `_*[]()~` + "`" + `>#+-=|{}.!\`

// escapeTelegramText escapes special characters for Telegram MarkdownV2 text.
func escapeTelegramText(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	for _, r := range s {
		if strings.ContainsRune(telegramSpecial, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeTelegramURL escapes only ) and \ inside link URLs for MarkdownV2.
func escapeTelegramURL(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == ')' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ========================= Slack renderer =========================

type slackRenderer struct {
	bufStack
	source []byte
}

func (r *slackRenderer) walk(n ast.Node, entering bool) (ast.WalkStatus, error) {
	switch node := n.(type) {
	case *ast.Document:
		// root -- no output

	case *ast.Paragraph:
		if !entering {
			if isInTightList(node) {
				r.w().WriteByte('\n')
			} else {
				r.w().WriteString("\n\n")
			}
		}

	case *ast.TextBlock:
		if !entering {
			r.w().WriteByte('\n')
		}

	case *ast.Heading:
		if entering {
			r.w().WriteByte('*')
		} else {
			r.w().WriteString("*\n\n")
		}

	case *ast.ThematicBreak:
		if entering {
			r.w().WriteString("---\n\n")
		}

	case *ast.Text:
		if entering {
			r.w().Write(node.Value(r.source))
			if node.SoftLineBreak() {
				r.w().WriteByte('\n')
			}
			if node.HardLineBreak() {
				r.w().WriteByte('\n')
			}
		}

	case *ast.String:
		if entering {
			r.w().Write(node.Value)
		}

	case *ast.Emphasis:
		if node.Level == 2 {
			r.w().WriteByte('*')
		} else {
			r.w().WriteByte('_')
		}

	case *east.Strikethrough:
		r.w().WriteByte('~')

	case *ast.CodeSpan:
		if entering {
			r.w().WriteByte('`')
			for c := node.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					r.w().Write(t.Value(r.source))
				}
			}
			r.w().WriteByte('`')
			return ast.WalkSkipChildren, nil
		}

	case *ast.FencedCodeBlock:
		if entering {
			r.w().WriteString("```\n")
			writeLines(r.w(), node.Lines(), r.source)
			r.w().WriteString("```\n\n")
			return ast.WalkSkipChildren, nil
		}

	case *ast.CodeBlock:
		if entering {
			r.w().WriteString("```\n")
			writeLines(r.w(), node.Lines(), r.source)
			r.w().WriteString("```\n\n")
			return ast.WalkSkipChildren, nil
		}

	case *ast.Link:
		if entering {
			r.w().WriteByte('<')
			r.w().Write(node.Destination)
			r.w().WriteByte('|')
		} else {
			r.w().WriteByte('>')
		}

	case *ast.AutoLink:
		if entering {
			r.w().Write(node.URL(r.source))
		}
		return ast.WalkSkipChildren, nil

	case *ast.Image:
		if entering {
			r.w().WriteByte('<')
			r.w().Write(node.Destination)
			r.w().WriteByte('|')
		} else {
			r.w().WriteByte('>')
		}

	case *ast.Blockquote:
		if entering {
			r.push()
		} else {
			content := strings.TrimRight(r.pop(), "\n")
			r.w().WriteString(prefixLines(content, "> "))
			r.w().WriteString("\n\n")
		}

	case *ast.List:
		if !entering && node.IsTight {
			r.w().WriteByte('\n')
		}

	case *ast.ListItem:
		if entering {
			r.w().WriteString(listItemPrefix(node))
		}

	case *ast.RawHTML:
		if entering {
			writeLines(r.w(), node.Segments, r.source)
			return ast.WalkSkipChildren, nil
		}

	case *ast.HTMLBlock:
		if entering {
			writeLines(r.w(), node.Lines(), r.source)
			r.w().WriteByte('\n')
			return ast.WalkSkipChildren, nil
		}
	}

	return ast.WalkContinue, nil
}

// ========================= Telegram renderer =========================

type telegramRenderer struct {
	bufStack
	source []byte
}

func (r *telegramRenderer) walk(n ast.Node, entering bool) (ast.WalkStatus, error) {
	switch node := n.(type) {
	case *ast.Document:
		// root -- no output

	case *ast.Paragraph:
		if !entering {
			if isInTightList(node) {
				r.w().WriteByte('\n')
			} else {
				r.w().WriteString("\n\n")
			}
		}

	case *ast.TextBlock:
		if !entering {
			r.w().WriteByte('\n')
		}

	case *ast.Heading:
		if entering {
			r.w().WriteByte('*')
		} else {
			r.w().WriteString("*\n\n")
		}

	case *ast.ThematicBreak:
		if entering {
			r.w().WriteString(escapeTelegramText("---"))
			r.w().WriteString("\n\n")
		}

	case *ast.Text:
		if entering {
			r.w().WriteString(escapeTelegramText(string(node.Value(r.source))))
			if node.SoftLineBreak() {
				r.w().WriteByte('\n')
			}
			if node.HardLineBreak() {
				r.w().WriteByte('\n')
			}
		}

	case *ast.String:
		if entering {
			r.w().WriteString(escapeTelegramText(string(node.Value)))
		}

	case *ast.Emphasis:
		if node.Level == 2 {
			r.w().WriteByte('*')
		} else {
			r.w().WriteByte('_')
		}

	case *east.Strikethrough:
		r.w().WriteByte('~')

	case *ast.CodeSpan:
		if entering {
			r.w().WriteByte('`')
			for c := node.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					r.w().Write(t.Value(r.source)) // no escaping inside code
				}
			}
			r.w().WriteByte('`')
			return ast.WalkSkipChildren, nil
		}

	case *ast.FencedCodeBlock:
		if entering {
			r.w().WriteString("```")
			if lang := node.Language(r.source); len(lang) > 0 {
				r.w().Write(lang)
			}
			r.w().WriteByte('\n')
			writeLines(r.w(), node.Lines(), r.source) // no escaping inside code
			r.w().WriteString("```\n\n")
			return ast.WalkSkipChildren, nil
		}

	case *ast.CodeBlock:
		if entering {
			r.w().WriteString("```\n")
			writeLines(r.w(), node.Lines(), r.source)
			r.w().WriteString("```\n\n")
			return ast.WalkSkipChildren, nil
		}

	case *ast.Link:
		if entering {
			r.w().WriteByte('[')
		} else {
			r.w().WriteString("](")
			r.w().WriteString(escapeTelegramURL(string(node.Destination)))
			r.w().WriteByte(')')
		}

	case *ast.AutoLink:
		if entering {
			r.w().WriteString(escapeTelegramText(string(node.URL(r.source))))
		}
		return ast.WalkSkipChildren, nil

	case *ast.Image:
		if entering {
			r.w().WriteByte('[')
		} else {
			r.w().WriteString("](")
			r.w().WriteString(escapeTelegramURL(string(node.Destination)))
			r.w().WriteByte(')')
		}

	case *ast.Blockquote:
		if entering {
			r.push()
		} else {
			content := strings.TrimRight(r.pop(), "\n")
			r.w().WriteString(prefixLines(content, ">"))
			r.w().WriteString("\n\n")
		}

	case *ast.List:
		if !entering && node.IsTight {
			r.w().WriteByte('\n')
		}

	case *ast.ListItem:
		if entering {
			r.w().WriteString(escapeTelegramText(listItemPrefix(node)))
		}

	case *ast.RawHTML:
		if entering {
			writeLinesEscaped(r.w(), node.Segments, r.source)
			return ast.WalkSkipChildren, nil
		}

	case *ast.HTMLBlock:
		if entering {
			writeLinesEscaped(r.w(), node.Lines(), r.source)
			r.w().WriteByte('\n')
			return ast.WalkSkipChildren, nil
		}
	}

	return ast.WalkContinue, nil
}
