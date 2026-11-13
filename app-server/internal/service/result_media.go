package service

import (
	"strconv"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func resultMediaMeta(raw map[string]any) *domain.MediaMeta {
	if len(raw) == 0 {
		return nil
	}
	meta := &domain.MediaMeta{}
	if role, _ := raw["role"].(string); role == "vocals" || role == "background" {
		meta.Role = role
	}
	meta.Container, _ = raw["format_name"].(string)
	if duration, ok := raw["duration"].(string); ok {
		if seconds, err := strconv.ParseFloat(duration, 64); err == nil && seconds >= 0 {
			meta.DurationMs = domain.Millis(seconds * 1000)
		}
	}
	streams, _ := raw["streams"].([]any)
	for _, value := range streams {
		stream, ok := value.(map[string]any)
		if !ok {
			continue
		}
		codecType, _ := stream["codec_type"].(string)
		if codecType == "audio" {
			meta.AudioTracks++
			if rate, ok := stream["sample_rate"].(string); ok {
				meta.SampleRate, _ = strconv.Atoi(rate)
			}
		}
		if codecType == "video" {
			meta.Codec, _ = stream["codec_name"].(string)
			if width, ok := intValue(stream["width"]); ok {
				meta.Width = width
			}
			if height, ok := intValue(stream["height"]); ok {
				meta.Height = height
			}
		}
	}
	return meta
}
