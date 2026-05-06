package pipeline

// EncodeProfile bundles the encoder + source-selection settings derived from
// the user's chosen output_quality. Centralizing this avoids the renderer and
// the visual fetcher drifting out of sync (e.g. asking Pexels for 1920+ source
// while encoding at draft CRF, which would just throw away the extra detail).
//
// Quality presets (v1):
//
//   draft    — fastest render, lower bitrate. Useful for previews / iteration.
//   standard — balanced quality+speed (existing default behavior).
//   high     — YouTube-ready master. Visually transparent encode + min FHD source.
type EncodeProfile struct {
	Quality      string // "draft" | "standard" | "high"
	Preset       string // x264 -preset value
	CRF          string // x264 -crf value (string for direct use as ffmpeg arg)
	FPS          int    // output framerate
	PexelsSize   string // Pexels &size= filter ("" / "medium" / "large")
	MinClipWidth int    // minimum acceptable Pexels file width (pixels)
}

// ProfileFor returns the EncodeProfile for the given output_quality string.
// Unknown / empty quality names fall back to "standard" so the pipeline keeps
// working even if older clients submit jobs without the field.
func ProfileFor(quality string) EncodeProfile {
	switch quality {
	case "draft":
		return EncodeProfile{
			Quality:      "draft",
			Preset:       "ultrafast",
			CRF:          "28",
			FPS:          30,
			PexelsSize:   "medium",
			MinClipWidth: 1280,
		}
	case "high":
		return EncodeProfile{
			Quality:      "high",
			Preset:       "medium",
			CRF:          "18",
			FPS:          30,
			PexelsSize:   "large",
			MinClipWidth: 1920,
		}
	case "standard", "":
		fallthrough
	default:
		return EncodeProfile{
			Quality:      "standard",
			Preset:       "fast",
			CRF:          "23",
			FPS:          30,
			PexelsSize:   "large",
			MinClipWidth: 1280,
		}
	}
}
