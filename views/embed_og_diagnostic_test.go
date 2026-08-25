package views

import (
	"bytes"
	"strings"
	"testing"

	"instafix/views/model"
)

func TestEmbedOGDiagnosticOrderAndVideoMetadata(t *testing.T) {
	v := &model.ViewsData{
		Card:          "summary_large_image",
		Title:         "@tester",
		Description:   "caption & stats",
		URL:           "https://www.instagram.com/reel/CODE/",
		CanonicalURL:  "https://www.instagram.com/reel/CODE/",
		Site:          "Instagram7",
		Creator:       "@tester",
		OGType:        "article",
		ArticleAuthor: "https://www.instagram.com/tester/",
		ImageURL:      "https://www.instagram7.com/offload/CODE/1?thumbnail=1",
		ImageWidth:    720,
		ImageHeight:   1280,
		ImageAlt:      "caption & stats",
		VideoURL:      "https://www.instagram7.com/offload/CODE/1?v=1",
		Width:         720,
		Height:        1280,
	}

	var buf bytes.Buffer
	EmbedOGDiagnostic(v, &buf)
	body := buf.String()

	ordered := []string{`property="og:url"`, `property="og:site_name"`, `property="og:title"`, `property="og:description"`, `property="og:type"`, `property="og:image"`, `property="og:video"`}
	last := -1
	for _, marker := range ordered {
		idx := strings.Index(body, marker)
		if idx < 0 {
			t.Fatalf("missing %s in %s", marker, body)
		}
		if idx <= last {
			t.Fatalf("metadata order mismatch around %s", marker)
		}
		last = idx
	}
	for _, required := range []string{
		`property="og:video:type" content="video/mp4"`,
		`property="og:video:width" content="720"`,
		`property="og:video:height" content="1280"`,
		`caption &amp; stats`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("missing %q in %s", required, body)
		}
	}
}
