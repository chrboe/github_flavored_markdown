/*
Package github_flavored_markdown provides a GitHub Flavored Markdown renderer
with fenced code block highlighting, clickable heading anchor links.

The functionality should be equivalent to the GitHub Markdown API endpoint specified at
https://developer.github.com/v3/markdown/#render-a-markdown-document-in-raw-mode, except
the rendering is performed locally.

See examples for how to generate a complete HTML page, including CSS styles.
*/
package github_flavored_markdown

import (
	"bytes"
	"fmt"
	"github.com/microcosm-cc/bluemonday"
	"github.com/shurcooL/highlight_diff"
	"github.com/shurcooL/highlight_go"
	"github.com/shurcooL/octiconssvg"
	"github.com/shurcooL/sanitized_anchor_name"
	"github.com/sourcegraph/annotate"
	"github.com/sourcegraph/syntaxhighlight"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	bf "gopkg.in/russross/blackfriday.v2"
	"io"
	"regexp"
	"sort"
	"text/template"
)

// Markdown renders GitHub Flavored Markdown text.
func Markdown(text []byte) []byte {
	const htmlFlags = 0

	params := bf.HTMLRendererParameters{
		Flags: htmlFlags,
	}

	renderer := &renderer{
		HTMLRenderer: bf.NewHTMLRenderer(params),
	}

	unsanitized := bf.Run(text, bf.WithRenderer(renderer), bf.WithExtensions(extensions))
	sanitized := policy.SanitizeBytes(unsanitized)
	return sanitized
}

// Heading returns a heading HTML node with title text.
// The heading comes with an anchor based on the title.
//
// heading can be one of atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6.
func Heading(heading atom.Atom, title string) *html.Node {
	aName := sanitized_anchor_name.Create(title)
	a := &html.Node{
		Type: html.ElementNode, Data: atom.A.String(),
		Attr: []html.Attribute{
			{Key: atom.Name.String(), Val: aName},
			{Key: atom.Class.String(), Val: "anchor"},
			{Key: atom.Href.String(), Val: "#" + aName},
			{Key: atom.Rel.String(), Val: "nofollow"},
			{Key: "aria-hidden", Val: "true"},
		},
	}
	span := &html.Node{
		Type: html.ElementNode, Data: atom.Span.String(),
		Attr:       []html.Attribute{{Key: atom.Class.String(), Val: "octicon-link"}}, // TODO: Factor out the CSS for just headings.
		FirstChild: octiconssvg.Link(),
	}
	a.AppendChild(span)
	h := &html.Node{Type: html.ElementNode, Data: heading.String()}
	h.AppendChild(a)
	h.AppendChild(&html.Node{Type: html.TextNode, Data: title})
	return h
}

// extensions for GitHub Flavored Markdown-like parsing.
const extensions = bf.NoIntraEmphasis |
bf.Tables |
bf.FencedCode |
bf.Autolink |
bf.Strikethrough |
bf.SpaceHeadings |
bf.NoEmptyLineBeforeBlock

// policy for GitHub Flavored Markdown-like sanitization.
var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").Matching(bluemonday.SpaceSeparatedTokens).OnElements("div", "span")
	p.AllowAttrs("class", "name").Matching(bluemonday.SpaceSeparatedTokens).OnElements("a")
	p.AllowAttrs("rel").Matching(regexp.MustCompile(`^nofollow$`)).OnElements("a")
	p.AllowAttrs("aria-hidden").Matching(regexp.MustCompile(`^true$`)).OnElements("a")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").Matching(regexp.MustCompile(`^$`)).OnElements("input")
	p.AllowDataURIImages()
	return p
}()

type renderer struct {
	*bf.HTMLRenderer
}

func appendLanguageAttr(attrs []string, info []byte) []string {
	// first, add the "highlight" class
	attrs = append(attrs, `class="highlight`)
	if len(info) == 0 {
		// if there's no more classes, leave it at that
		attrs[0] += `"`
		return attrs
	}

	// try to get the end of the language
	endOfLang := bytes.IndexAny(info, "\t ")
	if endOfLang < 0 {
		// if it's not found, just use the whole thing
		endOfLang = len(info)
	}

	// append the class
	return append(attrs, fmt.Sprintf(`highlight-%s"`, info[:endOfLang]))
}

func findLang(info []byte) []byte {
	endOfLang := bytes.IndexAny(info, "\t ")
	if endOfLang < 0 {
		return []byte("")
	}

	return info[:endOfLang]
}

func heading(w io.Writer, node *bf.Node, entering bool) bf.WalkStatus {
	if node.Prev != nil {
		w.Write([]byte("\n"))
	}

	// Extract text content of the heading.
	var textContent string
	if htmlNode, err := html.Parse(bytes.NewReader(node.Literal)); err == nil {
		textContent = extractText(htmlNode)
	} else {
		// Failed to parse HTML (probably can never happen), so just use the whole thing.
		textContent = html.UnescapeString(string(node.Literal))
	}
	anchorName := sanitized_anchor_name.Create(textContent)

	w.Write([]byte(fmt.Sprintf(`<h%d><a name="%s" class="anchor" href="#%s" rel="nofollow" aria-hidden="true"><span class="octicon octicon-link"></span></a>`, node.HeadingData.Level, anchorName, anchorName)))
	//w.Write([]textHTML)
	w.Write([]byte(fmt.Sprintf("</h%d>\n", node.HeadingData.Level)))

	return bf.GoToNext
}

func codeblock(w io.Writer, node *bf.Node, entering bool) bf.WalkStatus {
	//r.cr(w)

	// parse out language
	lang := findLang(node.Info)

	if len(lang) == 0 {
		w.Write([]byte(`<pre><code>`))
	} else {
		// <div class="highlight highlight-...">
		w.Write([]byte(fmt.Sprintf(`<div class="highlight highlight-%s">`, lang)))
	}

	if highlightedCode, ok := highlightCode(node.Literal, string(lang)); ok {
		w.Write(highlightedCode)
	} else {
		attrEscape(w, node.Literal)
	}

	if len(lang) == 0 {
		w.Write([]byte(`</code></pre>`))
	} else {
		w.Write([]byte(`</pre></div>`))
	}

	// TODO evaluate if this is needed
	if node.Parent.Type != bf.Item {
		//r.cr(w)
	}

	return bf.GoToNext
}

func (r *renderer) RenderNode(w io.Writer, node *bf.Node, entering bool) bf.WalkStatus {
	switch node.Type {
	case bf.Heading:
		return heading(w, node, entering)

	case bf.CodeBlock:
		return codeblock(w, node, entering)
	}

	return bf.GoToNext
}

// extractText returns the recursive concatenation of the text content of an html node.
func extractText(n *html.Node) string {
	var out string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			out += c.Data
		} else {
			out += extractText(c)
		}
	}
	return out
}

// Task List support.
func (r *renderer) ListItem(out *bytes.Buffer, text []byte, flags int) {
	switch {
	case bytes.HasPrefix(text, []byte("[ ] ")):
		text = append([]byte(`<input type="checkbox" disabled="">`), text[3:]...)
	case bytes.HasPrefix(text, []byte("[x] ")) || bytes.HasPrefix(text, []byte("[X] ")):
		text = append([]byte(`<input type="checkbox" checked="" disabled="">`), text[3:]...)
	}
	r.HTMLRenderer.ListItem(out, text, flags)
}

var gfmHTMLConfig = syntaxhighlight.HTMLConfig{
	String:        "s",
	Keyword:       "k",
	Comment:       "c",
	Type:          "n",
	Literal:       "o",
	Punctuation:   "p",
	Plaintext:     "n",
	Tag:           "tag",
	HTMLTag:       "htm",
	HTMLAttrName:  "atn",
	HTMLAttrValue: "atv",
	Decimal:       "m",
}

func highlightCode(src []byte, lang string) (highlightedCode []byte, ok bool) {
	switch lang {
	case "Go", "Go-unformatted":
		var buf bytes.Buffer
		err := highlight_go.Print(src, &buf, syntaxhighlight.HTMLPrinter(gfmHTMLConfig))
		if err != nil {
			return nil, false
		}
		return buf.Bytes(), true
	case "diff":
		anns, err := highlight_diff.Annotate(src)
		if err != nil {
			return nil, false
		}

		lines := bytes.Split(src, []byte("\n"))
		lineStarts := make([]int, len(lines))
		var offset int
		for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
			lineStarts[lineIndex] = offset
			offset += len(lines[lineIndex]) + 1
		}

		lastDel, lastIns := -1, -1
		for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
			var lineFirstChar byte
			if len(lines[lineIndex]) > 0 {
				lineFirstChar = lines[lineIndex][0]
			}
			switch lineFirstChar {
			case '+':
				if lastIns == -1 {
					lastIns = lineIndex
				}
			case '-':
				if lastDel == -1 {
					lastDel = lineIndex
				}
			default:
				if lastDel != -1 || lastIns != -1 {
					if lastDel == -1 {
						lastDel = lastIns
					} else if lastIns == -1 {
						lastIns = lineIndex
					}

					beginOffsetLeft := lineStarts[lastDel]
					endOffsetLeft := lineStarts[lastIns]
					beginOffsetRight := lineStarts[lastIns]
					endOffsetRight := lineStarts[lineIndex]

					anns = append(anns, &annotate.Annotation{Start: beginOffsetLeft, End: endOffsetLeft, Left: []byte(`<span class="gd input-block">`), Right: []byte(`</span>`), WantInner: 0})
					anns = append(anns, &annotate.Annotation{Start: beginOffsetRight, End: endOffsetRight, Left: []byte(`<span class="gi input-block">`), Right: []byte(`</span>`), WantInner: 0})

					if '@' != lineFirstChar {
						//leftContent := string(src[beginOffsetLeft:endOffsetLeft])
						//rightContent := string(src[beginOffsetRight:endOffsetRight])
						// This is needed to filter out the "-" and "+" at the beginning of each line from being highlighted.
						// TODO: Still not completely filtered out.
						leftContent := ""
						for line := lastDel; line < lastIns; line++ {
							leftContent += "\x00" + string(lines[line][1:]) + "\n"
						}
						rightContent := ""
						for line := lastIns; line < lineIndex; line++ {
							rightContent += "\x00" + string(lines[line][1:]) + "\n"
						}

						var sectionSegments [2][]*annotate.Annotation
						highlight_diff.HighlightedDiffFunc(leftContent, rightContent, &sectionSegments, [2]int{beginOffsetLeft, beginOffsetRight})

						anns = append(anns, sectionSegments[0]...)
						anns = append(anns, sectionSegments[1]...)
					}
				}
				lastDel, lastIns = -1, -1
			}
		}

		sort.Sort(anns)

		out, err := annotate.Annotate(src, anns, template.HTMLEscape)
		if err != nil {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}

// Unexported blackfriday helpers.

func doubleSpace(out *bytes.Buffer) {
	if out.Len() > 0 {
		out.WriteByte('\n')
	}
}

func escapeSingleChar(char byte) (string, bool) {
	if char == '"' {
		return "&quot;", true
	}
	if char == '&' {
		return "&amp;", true
	}
	if char == '<' {
		return "&lt;", true
	}
	if char == '>' {
		return "&gt;", true
	}
	return "", false
}

func attrEscape(w io.Writer, src []byte) {
	org := 0
	for i, ch := range src {
		if entity, ok := escapeSingleChar(ch); ok {
			if i > org {
				// copy all the normal characters since the last escape
				w.Write(src[org:i])
			}
			org = i + 1
			w.Write(entity)
		}
	}
	if org < len(src) {
		w.Write(src[org:])
	}
}
