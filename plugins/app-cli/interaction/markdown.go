package interactioncomponent

import (
	"bytes"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

var terminalMarkdownParser = goldmark.New(goldmark.WithExtensions(extension.GFM))

type terminalMarkdownRenderer struct {
	model  *tuiModel
	source []byte
	width  int
}

func renderTerminalMarkdown(model *tuiModel, source string, width int) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	renderer := terminalMarkdownRenderer{model: model, source: []byte(source), width: max(8, width)}
	document := terminalMarkdownParser.Parser().Parse(text.NewReader(renderer.source))
	return strings.TrimSpace(renderer.renderBlocks(document, renderer.width))
}

func (r terminalMarkdownRenderer) renderBlocks(parent ast.Node, width int) string {
	parts := make([]string, 0, parent.ChildCount())
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if rendered := strings.TrimSpace(r.renderBlockNode(child, width)); rendered != "" {
			parts = append(parts, rendered)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (r terminalMarkdownRenderer) renderBlockNode(node ast.Node, width int) string {
	switch value := node.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return lipgloss.Wrap(r.renderInlineChildren(node), width, "")
	case *ast.Heading:
		prefix := strings.Repeat("#", value.Level) + " "
		text := prefix + r.renderInlineChildren(node)
		return lipgloss.Wrap(r.headingStyle(value.Level).Render(text), width, "")
	case *ast.Blockquote:
		body := r.renderBlocks(node, max(4, width-2))
		return prefixLines(body, r.quoteStyle().Render("│ "))
	case *ast.List:
		return r.renderList(value, width)
	case *ast.ListItem:
		return r.renderBlocks(node, width)
	case *ast.FencedCodeBlock:
		return r.renderCodeBlock(value, width)
	case *ast.CodeBlock:
		return r.renderCodeLines(value.Lines(), "", width)
	case *ast.ThematicBreak:
		return r.model.mutedStyle().Render(strings.Repeat("─", max(3, min(width, 24))))
	case *ast.HTMLBlock:
		return lipgloss.Wrap(sanitizeText(string(value.Text(r.source))), width, "")
	default:
		if node.HasChildren() {
			return r.renderBlocks(node, width)
		}
		return ""
	}
}

func (r terminalMarkdownRenderer) renderInlineChildren(parent ast.Node) string {
	var result strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		result.WriteString(r.renderInline(child))
	}
	return result.String()
}

func (r terminalMarkdownRenderer) renderInline(node ast.Node) string {
	switch value := node.(type) {
	case *ast.Text:
		content := sanitizeText(string(value.Segment.Value(r.source)))
		if value.HardLineBreak() {
			content += "\n"
		} else if value.SoftLineBreak() {
			content += " "
		}
		return content
	case *ast.String:
		return sanitizeText(string(value.Value))
	case *ast.CodeSpan:
		return r.codeStyle().Render(sanitizeText(string(value.Text(r.source))))
	case *ast.Emphasis:
		content := r.renderInlineChildren(node)
		style := lipgloss.NewStyle().Italic(true)
		if value.Level == 2 {
			style = lipgloss.NewStyle().Bold(true)
		}
		return style.Render(content)
	case *ast.Link:
		label := r.renderInlineChildren(node)
		destination := sanitizeText(string(value.Destination))
		if destination == "" || destination == label {
			return r.linkStyle().Render(label)
		}
		return r.linkStyle().Render(label) + r.model.mutedStyle().Render(" ("+destination+")")
	case *ast.AutoLink:
		return r.linkStyle().Render(sanitizeText(string(value.URL(r.source))))
	case *ast.Image:
		label := r.renderInlineChildren(node)
		if label == "" {
			label = "image"
		}
		return r.model.mutedStyle().Render("[" + label + "]")
	case *ast.RawHTML:
		return ""
	default:
		if node.HasChildren() {
			return r.renderInlineChildren(node)
		}
		return ""
	}
}

func (r terminalMarkdownRenderer) renderList(list *ast.List, width int) string {
	lines := make([]string, 0, list.ChildCount())
	index := list.Start
	for child := list.FirstChild(); child != nil; child = child.NextSibling() {
		item, ok := child.(*ast.ListItem)
		if !ok {
			continue
		}
		prefix := "• "
		if list.IsOrdered() {
			prefix = fmt.Sprintf("%d. ", index)
			index++
		}
		body := r.renderBlocks(item, max(4, width-lipgloss.Width(prefix)))
		lines = append(lines, hangingIndent(body, prefix))
	}
	return strings.Join(lines, "\n")
}

func (r terminalMarkdownRenderer) renderCodeBlock(block *ast.FencedCodeBlock, width int) string {
	language := sanitizeText(string(block.Language(r.source)))
	return r.renderCodeLines(block.Lines(), language, width)
}

func (r terminalMarkdownRenderer) renderCodeLines(lines *text.Segments, language string, width int) string {
	var content bytes.Buffer
	for index := 0; index < lines.Len(); index++ {
		segment := lines.At(index)
		content.Write(segment.Value(r.source))
	}
	body := strings.TrimSuffix(sanitizeText(content.String()), "\n")
	if language != "" {
		body = r.model.mutedStyle().Render(language) + "\n" + body
	}
	return r.codeBlockStyle().Width(max(4, width-2)).Render(lipgloss.Wrap(body, max(4, width-4), ""))
}

func (r terminalMarkdownRenderer) headingStyle(level int) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	if r.model.color {
		color := r.model.accentColor()
		if level > 2 {
			color = lipgloss.Color("75")
		}
		style = style.Foreground(color)
	}
	return style
}

func (r terminalMarkdownRenderer) quoteStyle() lipgloss.Style {
	style := lipgloss.NewStyle()
	if r.model.color {
		style = style.Foreground(lipgloss.Color("244"))
	}
	return style
}

func (r terminalMarkdownRenderer) codeStyle() lipgloss.Style {
	style := lipgloss.NewStyle()
	if r.model.color {
		style = style.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("236"))
	}
	return style
}

func (r terminalMarkdownRenderer) codeBlockStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).PaddingLeft(1)
	if r.model.color {
		style = style.BorderForeground(lipgloss.Color("240")).Foreground(lipgloss.Color("252"))
	}
	return style
}

func (r terminalMarkdownRenderer) linkStyle() lipgloss.Style {
	style := lipgloss.NewStyle().Underline(true)
	if r.model.color {
		style = style.Foreground(lipgloss.Color("75"))
	}
	return style
}

func prefixLines(value, prefix string) string {
	if value == "" {
		return prefix
	}
	return prefix + strings.ReplaceAll(value, "\n", "\n"+prefix)
}

func hangingIndent(value, prefix string) string {
	indent := strings.Repeat(" ", lipgloss.Width(prefix))
	return prefix + strings.ReplaceAll(value, "\n", "\n"+indent)
}
