package handlers

import (
	"errors"
	"testing"
)

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

func TestIsLikelyVideoOnlyDASHURLDetectsInstagramEncodedMetadata(t *testing.T) {
	dashURL := "https://scontent.cdninstagram.com/video.mp4?efg=eyJ2ZW5jb2RlX3RhZyI6Inhwdl9wcm9ncmVzc2l2ZS5JTlNUQUdSQU0uQ0xJUFMuQzMuNzIwLmRhc2hfYmFzZWxpbmVfMV92MSJ9"
	if !IsLikelyVideoOnlyDASHURL(dashURL) {
		t.Fatal("expected DASH baseline rendition to be detected as video-only")
	}
}

func TestIsLikelyVideoOnlyDASHURLKeepsProgressiveMP4Direct(t *testing.T) {
	progressiveURL := "https://scontent.cdninstagram.com/video.mp4?_nc_cat=110&oe=FFFFFFFF"
	if IsLikelyVideoOnlyDASHURL(progressiveURL) {
		t.Fatal("ordinary progressive MP4 must not be forced through AV remux")
	}
}

func TestCompactAVFromDASHManifestInfersTrackKindFromMimeType(t *testing.T) {
	manifest := `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation bandwidth="382336" mimeType="video/mp4" width="360" height="640" FBContentLength="5901371"><BaseURL>https://scontent.cdninstagram.com/small.mp4</BaseURL></Representation></AdaptationSet><AdaptationSet mimeType="audio/mp4"><Representation bandwidth="61048" mimeType="audio/mp4" FBContentLength="943883"><BaseURL>https://scontent.cdninstagram.com/audio.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`
	sources, err := compactAVFromDASHManifest(manifest, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if sources.Video.URL == "" || sources.AudioURL == "" {
		t.Fatalf("expected both tracks, got %+v", sources)
	}
}

func TestCompactAVSourcesAllowsExplicitlySilentReel(t *testing.T) {
	body := []byte(`{"data":{"xdt_api__v1__media__shortcode__web_info":{"items":[{"has_audio":false,"video_dash_manifest":"<MPD></MPD>"}]}}}`)
	_, err := compactAVSourcesFromGraphQLBody("DcZmj-stF4F", body, 20_000_000)
	if !errors.Is(err, ErrNoAudioExpected) {
		t.Fatalf("expected ErrNoAudioExpected, got %v", err)
	}
}
