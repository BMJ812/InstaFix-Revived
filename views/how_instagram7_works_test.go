package views

import (
	"bytes"
	"strings"
	"testing"
)

func TestHowInstagram7WorksPageUsesLiveExample(t *testing.T) {
	var out bytes.Buffer
	if !HowInstagram7Works(&out) {
		t.Fatal("HowInstagram7Works returned false")
	}
	html := out.String()
	for _, check := range []string{
		`rel="canonical" href="https://www.instagram7.com/how-instagram7-works"`,
		`Fix Instagram Reel previews by adding one number.`,
		`poster="/assets/demo/instagram7-test-reel-poster.webp"`,
		`src="/assets/demo/instagram7-test-reel.mp4"`,
		`Add 7 after instagram`,
		`Frequently asked questions`,
		`href="/#converter"`,
		`data-video-ready="false"`,
	} {
		if !strings.Contains(html, check) {
			t.Fatalf("page output missing %q", check)
		}
	}
	for _, forbidden := range []string{`"@type":"VideoObject"`, `contentUrl`, `<video controls preload="metadata" poster="">`} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("page unexpectedly contains incomplete future video data %q", forbidden)
		}
	}
}

func TestHowInstagram7WorksRendersCompleteFutureVideo(t *testing.T) {
	video := demoVideo{
		Name:          "Instagram7 walkthrough",
		Description:   "See the complete Instagram7 sharing workflow.",
		PosterURL:     "https://www.instagram7.com/assets/video/walkthrough-poster.webp",
		MP4URL:        "https://www.instagram7.com/assets/video/walkthrough.mp4",
		WebMURL:       "https://www.instagram7.com/assets/video/walkthrough.webm",
		CaptionsURL:   "https://www.instagram7.com/assets/video/walkthrough.en.vtt",
		TranscriptURL: "https://www.instagram7.com/how-instagram7-works#transcript",
		Transcript:    "Copy the Reel link, add 7, and send it.",
		UploadDate:    "2026-08-02",
		Duration:      "PT45S",
	}
	var out bytes.Buffer
	if err := renderHowInstagram7Works(&out, video); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, check := range []string{
		`data-video-ready="true"`,
		`type="video/webm"`,
		`type="video/mp4"`,
		`kind="captions"`,
		`Read the video transcript`,
		`"@type":"VideoObject"`,
		`"contentUrl":"https://www.instagram7.com/assets/video/walkthrough.mp4"`,
		`"thumbnailUrl":["https://www.instagram7.com/assets/video/walkthrough-poster.webp"]`,
	} {
		if !strings.Contains(html, check) {
			t.Fatalf("video page output missing %q", check)
		}
	}
}
