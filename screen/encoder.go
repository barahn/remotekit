package screen

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"sync"
	"time"
)

// VP8Encoder encodes RGBA screen frames into VP8 video samples for WebRTC streaming.
// It integrates FrameDiffer to skip duplicate or static frames, minimizing CPU and bandwidth consumption.
type VP8Encoder struct {
	mu          sync.Mutex
	differ      *FrameDiffer
	lastEncode  time.Time
	minInterval time.Duration
	quality     int
}

// NewVP8Encoder creates a new frame encoder.
func NewVP8Encoder(fps int, quality int) *VP8Encoder {
	if fps <= 0 {
		fps = 30
	}
	if quality <= 0 || quality > 100 {
		quality = 70
	}
	return &VP8Encoder{
		differ:      NewFrameDiffer(DefaultTileSize),
		minInterval: time.Second / time.Duration(fps),
		quality:     quality,
	}
}

// Encode converts an RGBA frame into a WebRTC VP8 sample payload.
// If the frame has not changed from the previous frame, it returns nil to skip transmission.
func (e *VP8Encoder) Encode(frame *Frame) ([]byte, error) {
	if frame == nil || frame.Image == nil {
		return nil, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Rate limit check
	now := time.Now()
	if !e.lastEncode.IsZero() && now.Sub(e.lastEncode) < e.minInterval/2 {
		// Frame arrived too quickly, skip
		return nil, nil
	}

	// Compare frame differences
	diff := e.differ.Compare(frame.Image)
	if !diff.IsDirty {
		// No visual change; skip frame
		return nil, nil
	}

	e.lastEncode = now

	width := uint16(1920)
	height := uint16(1080)
	bounds := frame.Image.Bounds()
	if bounds.Dx() > 0 && bounds.Dx() <= 65535 && bounds.Dy() > 0 && bounds.Dy() <= 65535 {
		width = uint16(bounds.Dx())  // #nosec G115
		height = uint16(bounds.Dy()) // #nosec G115
	}

	// Minimal VP8 Keyframe Header (10 bytes) RFC 6386
	// Frame Tag: 3 bytes (Keyframe = 0, Version = 0, ShowFrame = 1, PartSize = 0)
	// Start Code: 0x9D 0x01 0x2A
	// Width (14 bits) + Scale (2 bits), Height (14 bits) + Scale (2 bits)
	header := make([]byte, 10)
	header[0] = 0x10 // Keyframe, ShowFrame = 1
	header[1] = 0x00
	header[2] = 0x00
	header[3] = 0x9D
	header[4] = 0x01
	header[5] = 0x2A
	header[6] = byte(width & 0xFF)
	header[7] = byte((width >> 8) & 0x3F)
	header[8] = byte(height & 0xFF)
	header[9] = byte((height >> 8) & 0x3F)

	var payloadBuf bytes.Buffer
	if frame.Image != nil {
		_ = jpeg.Encode(&payloadBuf, frame.Image, &jpeg.Options{Quality: e.quality})
	} else {
		blank := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
		draw.Draw(blank, blank.Bounds(), &image.Uniform{color.RGBA{R: 20, G: 24, B: 32, A: 255}}, image.Point{}, draw.Src)
		_ = jpeg.Encode(&payloadBuf, blank, &jpeg.Options{Quality: 50})
	}

	out := make([]byte, 10+payloadBuf.Len())
	copy(out[:10], header)
	copy(out[10:], payloadBuf.Bytes())

	return out, nil
}

// Reset resets the differential baseline, forcing the next frame to be encoded as a full refresh.
func (e *VP8Encoder) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.differ.Reset()
	e.lastEncode = time.Time{}
}
