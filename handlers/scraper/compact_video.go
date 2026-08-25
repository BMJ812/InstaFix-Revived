package handlers

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

const compactMuxOverheadReserve int64 = 512 * 1024

// CompactPreviewSources describes separate Instagram DASH audio/video tracks
// that can be remuxed into a Telegram-friendly MP4 without re-encoding.
type CompactPreviewSources struct {
	Video          Media
	AudioURL       string
	VideoBytes     int64
	AudioBytes     int64
	EstimatedBytes int64
}

// ResolveCompactPreviewAV returns the best public Instagram DASH video+audio
// pair whose combined encoded size leaves enough room below maxBytes for MP4
// container overhead. The source URLs stay on Instagram CDN.
func ResolveCompactPreviewAV(postID string, maxBytes int64) (CompactPreviewSources, error) {
	if !validPostID(postID) {
		return CompactPreviewSources{}, errors.New("postID is not a valid Instagram post ID")
	}
	if maxBytes <= 0 {
		return CompactPreviewSources{}, errors.New("compact preview byte limit is disabled")
	}
	key := postID + ":compact-preview-av:" + strconv.FormatInt(maxBytes, 10)
	ret, err, _ := sflightScraper.Do(key, func() (interface{}, error) {
		body, err := scrapeFromGQLMobile(postID)
		if err != nil || !graphQLBodyHasMedia(body) {
			body, err = scrapeFromGQL(postID)
			if err != nil {
				return nil, err
			}
		}
		item, _ := graphQLMediaRoot(gjson.ParseBytes(body).Get("data"))
		if !presentResult(item) {
			return nil, ErrNotFound
		}
		manifest := strings.TrimSpace(item.Get("video_dash_manifest").String())
		if manifest == "" {
			return nil, errors.New("Instagram response has no DASH manifest")
		}
		sources, err := compactAVFromDASHManifest(manifest, maxBytes)
		if err != nil {
			return nil, err
		}
		sources.Video.TypeName = "GraphVideo"
		sources.Video.ThumbnailURL = bestV1ImageURL(item)
		if sources.Video.ThumbnailURL == "" {
			sources.Video.ThumbnailURL = bestDisplayURL(item)
		}
		wrapped := &InstaData{PostID: postID, Medias: []Media{sources.Video}}
		if err := normalizeMediaURLs(wrapped); err != nil {
			return nil, err
		}
		sources.Video = wrapped.Medias[0]

		audio, err := url.Parse(strings.TrimSpace(sources.AudioURL))
		if err != nil || audio.Host == "" || audio.Scheme != "https" {
			return nil, errors.New("compact audio URL is invalid")
		}
		normalizeInstagramCDNHost(audio)
		sources.AudioURL = audio.String()
		return sources, nil
	})
	if err != nil {
		return CompactPreviewSources{}, err
	}
	sources, ok := ret.(CompactPreviewSources)
	if !ok {
		return CompactPreviewSources{}, errors.New("compact preview resolver returned invalid sources")
	}
	return sources, nil
}

// ResolveCompactPreviewVideo is retained for callers that only need the chosen
// video dimensions/URL. Selection still reserves room for the matching audio.
func ResolveCompactPreviewVideo(postID string, maxBytes int64) (Media, error) {
	sources, err := ResolveCompactPreviewAV(postID, maxBytes)
	if err != nil {
		return Media{}, err
	}
	return sources.Video, nil
}

type dashMPD struct {
	Periods []dashPeriod `xml:"Period"`
}

type dashPeriod struct {
	AdaptationSets []dashAdaptationSet `xml:"AdaptationSet"`
}

type dashAdaptationSet struct {
	ContentType     string               `xml:"contentType,attr"`
	Representations []dashRepresentation `xml:"Representation"`
}

type dashRepresentation struct {
	Bandwidth     int64  `xml:"bandwidth,attr"`
	MimeType      string `xml:"mimeType,attr"`
	Width         int    `xml:"width,attr"`
	Height        int    `xml:"height,attr"`
	ContentLength int64  `xml:"FBContentLength,attr"`
	BaseURL       string `xml:"BaseURL"`
}

func compactAVFromDASHManifest(raw string, maxBytes int64) (CompactPreviewSources, error) {
	if maxBytes <= compactMuxOverheadReserve {
		return CompactPreviewSources{}, errors.New("compact preview byte limit is too small")
	}
	var manifest dashMPD
	if err := xml.Unmarshal([]byte(raw), &manifest); err != nil {
		return CompactPreviewSources{}, fmt.Errorf("parse DASH manifest: %w", err)
	}

	videos := make([]dashRepresentation, 0, 4)
	audios := make([]dashRepresentation, 0, 2)
	for _, period := range manifest.Periods {
		for _, set := range period.AdaptationSets {
			kind := strings.ToLower(strings.TrimSpace(set.ContentType))
			for _, rep := range set.Representations {
				if rep.ContentLength <= 0 || strings.TrimSpace(rep.BaseURL) == "" || !validMediaURL(rep.BaseURL) || !IsAllowedMediaURL(rep.BaseURL) {
					continue
				}
				switch kind {
				case "video":
					if rep.MimeType == "" || strings.EqualFold(rep.MimeType, "video/mp4") {
						videos = append(videos, rep)
					}
				case "audio":
					if rep.MimeType == "" || strings.EqualFold(rep.MimeType, "audio/mp4") {
						audios = append(audios, rep)
					}
				}
			}
		}
	}
	if len(videos) == 0 || len(audios) == 0 {
		return CompactPreviewSources{}, errors.New("DASH manifest does not contain usable video and audio tracks")
	}

	var bestVideo, bestAudio dashRepresentation
	found := false
	for _, video := range videos {
		for _, audio := range audios {
			estimated := video.ContentLength + audio.ContentLength + compactMuxOverheadReserve
			if estimated > maxBytes {
				continue
			}
			area := video.Width * video.Height
			bestArea := bestVideo.Width * bestVideo.Height
			if !found || area > bestArea ||
				(area == bestArea && video.Bandwidth > bestVideo.Bandwidth) ||
				(area == bestArea && video.Bandwidth == bestVideo.Bandwidth && audio.Bandwidth > bestAudio.Bandwidth) {
				bestVideo, bestAudio = video, audio
				found = true
			}
		}
	}
	if !found {
		return CompactPreviewSources{}, fmt.Errorf("DASH manifest has no audio/video pair within %d bytes", maxBytes)
	}

	return CompactPreviewSources{
		Video: Media{
			TypeName: "GraphVideo",
			URL:      strings.TrimSpace(bestVideo.BaseURL),
			Width:    bestVideo.Width,
			Height:   bestVideo.Height,
		},
		AudioURL:       strings.TrimSpace(bestAudio.BaseURL),
		VideoBytes:     bestVideo.ContentLength,
		AudioBytes:     bestAudio.ContentLength,
		EstimatedBytes: bestVideo.ContentLength + bestAudio.ContentLength + compactMuxOverheadReserve,
	}, nil
}

func compactVideoFromDASHManifest(raw string, maxBytes int64) (Media, error) {
	sources, err := compactAVFromDASHManifest(raw, maxBytes)
	if err != nil {
		return Media{}, err
	}
	return sources.Video, nil
}
