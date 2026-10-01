package web

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// replyMarkdown renders a model's reply (in chat and test results): GitHub's
// Markdown, in goldmark's safe mode like the release notes, with two
// differences for text a model wrote. HTML in it is shown as text rather
// than dropped (a reply about HTML would otherwise lose its examples), and
// images are links rather than fetched, so a reply can't make the browser
// load something.
var replyMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(imagesAsLinks{}, 50), util.Prioritized(externalLinks{}, 100))),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(escapedHTML{}, 100))),
)

// renderReply renders a model's reply as HTML. Rendering Markdown can't
// fail on a string; if it somehow does, the reply is shown escaped.
func renderReply(src string) template.HTML {
	var buf bytes.Buffer
	if err := replyMarkdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML("<p>" + template.HTMLEscapeString(src) + "</p>")
	}
	return template.HTML(buf.String())
}

// imagesAsLinks turns ![alt](url) into [alt](url).
type imagesAsLinks struct{}

func (imagesAsLinks) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	var images []*ast.Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering {
			images = append(images, img)
		}
		return ast.WalkContinue, nil
	})
	for _, img := range images {
		link := ast.NewLink()
		link.Destination, link.Title = img.Destination, img.Title
		for c := img.FirstChild(); c != nil; {
			next := c.NextSibling()
			link.AppendChild(link, c)
			c = next
		}
		if !link.HasChildren() {
			link.AppendChild(link, ast.NewString([]byte("image")))
		}
		img.Parent().ReplaceChild(img.Parent(), img, link)
	}
}

// escapedHTML renders raw HTML, inline or as a block, as the text it is.
type escapedHTML struct{}

func (escapedHTML) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindRawHTML, func(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			segs := n.(*ast.RawHTML).Segments
			for i := range segs.Len() {
				seg := segs.At(i)
				w.Write(util.EscapeHTML(seg.Value(src)))
			}
		}
		return ast.WalkSkipChildren, nil
	})
	reg.Register(ast.KindHTMLBlock, func(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		b := n.(*ast.HTMLBlock)
		if entering {
			w.WriteString(`<p class="raw-html">`)
			lines := b.Lines()
			for i := range lines.Len() {
				line := lines.At(i)
				w.Write(util.EscapeHTML(line.Value(src)))
			}
			return ast.WalkContinue, nil
		}
		if b.HasClosure() {
			w.Write(util.EscapeHTML(b.ClosureLine.Value(src)))
		}
		w.WriteString("</p>\n")
		return ast.WalkContinue, nil
	})
}
