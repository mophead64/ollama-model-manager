package web

import (
	"strings"
	"testing"
)

func TestRenderReply(t *testing.T) {
	got := string(renderReply("## Steps\n\n1. **Install** it\n2. Run `go test`\n\n```go\nfmt.Println(\"<hi>\")\n```\n\n" +
		"Use a <div> here.\n\n<script>alert(1)</script>\n\n![cat](https://example.com/cat.png) [bad](javascript:alert(1)) [ok](https://example.com)\n\n| a | b |\n|---|---|\n| 1 | 2 |"))
	for _, want := range []string{
		"<h2>Steps</h2>", "<ol>", "<strong>Install</strong>", "<code>go test</code>",
		`<pre><code class="language-go">fmt.Println(&quot;&lt;hi&gt;&quot;)`,
		"Use a &lt;div&gt; here.",                                   // inline HTML shown, not dropped
		`<p class="raw-html">&lt;script&gt;alert(1)&lt;/script&gt;`, // a block too
		`<a href="https://example.com/cat.png" target="_blank" rel="noopener noreferrer">cat</a>`,
		`<a href="https://example.com" target="_blank" rel="noopener noreferrer">ok</a>`,
		"<table>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"<script", "<div>", "<img", `href="javascript:`} {
		if strings.Contains(got, bad) {
			t.Errorf("contains %q:\n%s", bad, got)
		}
	}
}
