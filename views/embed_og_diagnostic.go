package views

import (
	"html"
	"instafix/views/model"
	"io"
	"strconv"
)

// EmbedOGDiagnostic renders a deliberately small OpenGraph document whose
// metadata order mirrors OGInstagram closely enough to isolate Telegram parser
// behavior from InstaFix's normal template. It is only selected by an explicit
// Telegram diagnostic flag in the handler; production rendering stays on Embed.
func EmbedOGDiagnostic(v *model.ViewsData, wr io.Writer) {
	write := func(s string) { _, _ = io.WriteString(wr, s) }
	attr := func(s string) { write(html.EscapeString(s)) }
	meta := func(kind, name, value string) {
		if value == "" {
			return
		}
		write(`<meta ` + kind + `="`)
		attr(name)
		write(`" content="`)
		attr(value)
		write(`"/>`)
	}

	write(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"/>`)
	if v.CanonicalURL != "" {
		write(`<link rel="canonical" href="`)
		attr(v.CanonicalURL)
		write(`"/>`)
	}
	meta("property", "og:url", v.URL)
	meta("property", "og:locale", "en_US")
	meta("property", "og:site_name", v.Site)
	meta("property", "og:title", v.Title)
	meta("name", "twitter:title", v.Title)
	meta("name", "theme-color", "#ff0069")
	meta("name", "twitter:card", v.Card)
	meta("name", "description", v.Description)
	meta("property", "og:description", v.Description)
	meta("name", "twitter:description", v.Description)
	if v.FaviconURL != "" {
		write(`<link rel="icon" href="`)
		attr(v.FaviconURL)
		write(`"/>`)
	}
	meta("name", "twitter:creator", v.Creator)
	meta("property", "og:type", v.OGType)
	if v.AppleIconURL != "" {
		write(`<link rel="apple-touch-icon" href="`)
		attr(v.AppleIconURL)
		write(`"/>`)
	}
	meta("property", "article:author", v.ArticleAuthor)

	if v.ImageURL != "" {
		meta("property", "og:image", v.ImageURL)
		meta("property", "og:image:secure_url", v.ImageURL)
		meta("name", "twitter:image", v.ImageURL)
		if v.ImageWidth > 0 && v.ImageHeight > 0 {
			meta("property", "og:image:width", strconv.Itoa(v.ImageWidth))
			meta("property", "og:image:height", strconv.Itoa(v.ImageHeight))
		}
		meta("name", "twitter:image:alt", v.ImageAlt)
		meta("property", "og:image:alt", v.ImageAlt)
	}

	if v.VideoURL != "" {
		meta("property", "og:video", v.VideoURL)
		meta("property", "og:video:secure_url", v.VideoURL)
		meta("property", "og:video:type", "video/mp4")
		if v.Width > 0 && v.Height > 0 {
			meta("property", "og:video:width", strconv.Itoa(v.Width))
			meta("property", "og:video:height", strconv.Itoa(v.Height))
		}
	}
	write(`</head><body></body></html>`)
}
