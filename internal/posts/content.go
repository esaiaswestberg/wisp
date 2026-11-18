package posts

import (
	"html"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

var whitespace = regexp.MustCompile(`\s+`)

var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowElements("figure", "figcaption", "table", "thead", "tbody", "tr", "th", "td", "pre", "code")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^(language-[a-z0-9_-]+|hljs)$`)).OnElements("code", "pre")
	p.AllowAttrs("alt", "width", "height").OnElements("img")
	p.AllowURLSchemes("http", "https", "mailto")
	return p
}()

func Sanitize(body string) string { return policy.Sanitize(body) }

func PlainText(body string) string {
	value := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " ")
	value = html.UnescapeString(value)
	return strings.TrimSpace(whitespace.ReplaceAllString(value, " "))
}

func Slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
