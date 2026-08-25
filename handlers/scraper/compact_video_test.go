package handlers

import "testing"

func TestCompactVideoFromDASHManifestChoosesBestRepresentationUnderLimit(t *testing.T) {
	manifest := `<?xml version="1.0"?><MPD><Period><AdaptationSet contentType="video"><Representation bandwidth="1973414" mimeType="video/mp4" width="720" height="1280" FBContentLength="30459654"><BaseURL>https://scontent.cdninstagram.com/high.mp4?oe=FFFFFFFF</BaseURL></Representation><Representation bandwidth="382336" mimeType="video/mp4" width="360" height="640" FBContentLength="5901371"><BaseURL>https://scontent.cdninstagram.com/small.mp4?oe=FFFFFFFF</BaseURL></Representation></AdaptationSet><AdaptationSet contentType="audio"><Representation bandwidth="61048" mimeType="audio/mp4" FBContentLength="943883"><BaseURL>https://scontent.cdninstagram.com/audio.mp4?oe=FFFFFFFF</BaseURL></Representation></AdaptationSet></Period></MPD>`

	sources, err := compactAVFromDASHManifest(manifest, 30_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if sources.Video.URL != "https://scontent.cdninstagram.com/small.mp4?oe=FFFFFFFF" {
		t.Fatalf("video URL = %q", sources.Video.URL)
	}
	if sources.Video.Width != 360 || sources.Video.Height != 640 {
		t.Fatalf("dimensions = %dx%d", sources.Video.Width, sources.Video.Height)
	}
	if sources.AudioURL != "https://scontent.cdninstagram.com/audio.mp4?oe=FFFFFFFF" {
		t.Fatalf("audio URL = %q", sources.AudioURL)
	}
	if sources.VideoBytes != 5901371 || sources.AudioBytes != 943883 {
		t.Fatalf("source bytes = video %d audio %d", sources.VideoBytes, sources.AudioBytes)
	}
}

func TestCompactVideoFromDASHManifestRejectsOnlyOversizedVideo(t *testing.T) {
	manifest := `<MPD><Period><AdaptationSet contentType="video"><Representation bandwidth="1973414" mimeType="video/mp4" width="720" height="1280" FBContentLength="30459654"><BaseURL>https://scontent.cdninstagram.com/high.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`
	if _, err := compactVideoFromDASHManifest(manifest, 30_000_000); err == nil {
		t.Fatal("expected no compact representation")
	}
}
