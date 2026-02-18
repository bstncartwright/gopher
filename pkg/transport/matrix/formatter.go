package matrix

import (
	"bytes"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

type richTextFormatter interface {
	Format(markdown string) (string, bool)
}

type markdownHTMLFormatter struct {
	renderer goldmark.Markdown
	policy   *bluemonday.Policy
}

func newMarkdownHTMLFormatter() richTextFormatter {
	policy := bluemonday.NewPolicy()
	policy.AllowElements(
		"p", "br", "strong", "em", "del", "code", "pre", "blockquote",
		"ul", "ol", "li", "a",
		"h1", "h2", "h3", "h4", "h5", "h6",
	)
	policy.AllowAttrs("href").OnElements("a")
	policy.RequireParseableURLs(true)
	policy.AllowURLSchemes("http", "https", "mailto", "matrix")
	policy.AddSpaceWhenStrippingTag(true)

	return &markdownHTMLFormatter{
		renderer: goldmark.New(
			goldmark.WithExtensions(extension.GFM),
		),
		policy: policy,
	}
}

func (f *markdownHTMLFormatter) Format(markdown string) (string, bool) {
	if f == nil || f.renderer == nil || f.policy == nil {
		return "", false
	}
	source := strings.TrimSpace(markdown)
	if source == "" {
		return "", false
	}

	var rendered bytes.Buffer
	if err := f.renderer.Convert([]byte(source), &rendered); err != nil {
		return "", false
	}
	sanitized := strings.TrimSpace(f.policy.Sanitize(rendered.String()))
	if sanitized == "" {
		return "", false
	}
	return sanitized, true
}
