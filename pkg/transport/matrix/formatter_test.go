package matrix

import (
	"strings"
	"testing"
)

func TestMarkdownHTMLFormatterFormatsMarkdown(t *testing.T) {
	formatter := newMarkdownHTMLFormatter()
	formatted, ok := formatter.Format("# title\n\n- one\n- two\n\n```go\nfmt.Println(\"x\")\n```\n\n> quote")
	if !ok {
		t.Fatalf("Format() ok = false, want true")
	}
	if !strings.Contains(formatted, "<h1>title</h1>") {
		t.Fatalf("formatted output missing heading: %q", formatted)
	}
	if !strings.Contains(formatted, "<ul>") {
		t.Fatalf("formatted output missing list: %q", formatted)
	}
	if !strings.Contains(formatted, "<blockquote>") {
		t.Fatalf("formatted output missing blockquote: %q", formatted)
	}
	if !strings.Contains(formatted, "<pre><code") {
		t.Fatalf("formatted output missing code block: %q", formatted)
	}
}

func TestMarkdownHTMLFormatterStripsUnsafeHTML(t *testing.T) {
	formatter := newMarkdownHTMLFormatter()
	formatted, ok := formatter.Format("safe text\n\n<script>alert(1)</script><img src=\"x\" onerror=\"alert(1)\">")
	if !ok {
		t.Fatalf("Format() ok = false, want true")
	}
	if strings.Contains(formatted, "<script") {
		t.Fatalf("formatted output contains script tag: %q", formatted)
	}
	if strings.Contains(formatted, "<img") {
		t.Fatalf("formatted output contains img tag: %q", formatted)
	}
	if strings.Contains(formatted, "onerror") {
		t.Fatalf("formatted output contains event handler: %q", formatted)
	}
}

func TestMarkdownHTMLFormatterStripsUnsafeLinks(t *testing.T) {
	formatter := newMarkdownHTMLFormatter()
	formatted, ok := formatter.Format(`[bad](javascript:alert(1)) [ok](https://example.com)`)
	if !ok {
		t.Fatalf("Format() ok = false, want true")
	}
	if strings.Contains(strings.ToLower(formatted), "javascript:") {
		t.Fatalf("formatted output contains javascript URL: %q", formatted)
	}
	if !strings.Contains(formatted, `href="https://example.com"`) {
		t.Fatalf("formatted output missing safe link: %q", formatted)
	}
}

func TestMarkdownHTMLFormatterRejectsEmptyInput(t *testing.T) {
	formatter := newMarkdownHTMLFormatter()
	if _, ok := formatter.Format(" \n\t "); ok {
		t.Fatalf("Format() ok = true, want false")
	}
}
