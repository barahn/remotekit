package screen

import (
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/barahn/remotekit/screen/codec/vp8"
)

// defaultKeyFrameInterval is how often a key frame is emitted, in frames.
//
// Every key frame is a full refresh and costs several times an inter frame, but
// it is also the only frame a viewer joining mid-stream can start from, and the
// only place accumulated error is discarded. Ten seconds at 30fps is a
// compromise for a desktop stream, where a viewer usually connects once and
// stays.
const defaultKeyFrameInterval = 300

// VP8Encoder encodes RGBA screen frames into VP8 samples for WebRTC streaming.
// It integrates FrameDiffer to skip frames that carry no visual change, which
// on a desktop is most of them.
//
// The encoder underneath is pkg/screen/codec/vp8, a pure-Go VP8 encoder forked
// from github.com/opd-ai/vp8 and repaired against RFC 6386. Its output is
// checked against libvpx by TestEncoderProducesConformantStream here and, more
// strictly, by the byte-level reconstruction diff in the codec package: the
// encoder's reference and libvpx's reconstruction agree exactly, which is what
// keeps a closed-loop codec from drifting.
//
// This replaces a placeholder that prepended a VP8 key frame header to a JPEG
// payload. That was scaffolding no decoder could read.
type VP8Encoder struct {
	mu          sync.Mutex
	differ      *FrameDiffer
	enc         *vp8.Encoder
	yuv         []byte
	width       int
	height      int
	lastEncode  time.Time
	minInterval time.Duration
	fps         int
	quality     int
	forceKey    bool
}

// NewVP8Encoder creates a new frame encoder.
//
// quality is 1..100 in the conventional sense, higher being better, and is
// mapped onto the encoder's bitrate. It is named for the parameter it replaced
// rather than for what VP8 exposes, so that callers do not have to change.
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
		fps:         fps,
		quality:     quality,
		forceKey:    true,
	}
}

// Encode converts an RGBA frame into a WebRTC VP8 sample payload.
// If the frame has not changed from the previous frame, it returns nil to skip
// transmission.
func (e *VP8Encoder) Encode(frame *Frame) ([]byte, error) {
	if frame == nil || frame.Image == nil {
		return nil, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	if !e.lastEncode.IsZero() && now.Sub(e.lastEncode) < e.minInterval/2 {
		// Frame arrived too quickly, skip
		return nil, nil
	}

	// A frame that changes nothing is not sent at all. The encoder's reference
	// is untouched by the omission, so the next frame it does encode still
	// predicts from a picture the decoder holds.
	diff := e.differ.Compare(frame.Image)
	if !diff.IsDirty {
		return nil, nil
	}

	width, height := codedSize(frame.Image.Bounds())
	if width == 0 || height == 0 {
		return nil, nil
	}

	if err := e.ensureEncoder(width, height); err != nil {
		return nil, err
	}

	rgbaToI420(e.yuv, frame.Image, width, height)

	// Hand the encoder the tile map, so unchanged macroblocks cost nothing to
	// decide. Without this the encoder analyses every macroblock of every
	// frame, which at 1080p is roughly twice the time budget of a 30fps stream
	// even when almost nothing on screen moved.
	e.enc.SetDirtyMacroblocks(dirtyMacroblocks(diff, frame.Image.Bounds(), width, height))

	if e.forceKey {
		e.enc.ForceKeyFrame()
		e.forceKey = false
	}

	sample, err := e.enc.Encode(e.yuv)
	if err != nil {
		return nil, fmt.Errorf("screen: vp8 encode failed: %w", err)
	}

	e.lastEncode = now
	return sample, nil
}

// ensureEncoder creates the encoder, or replaces it when the frame size
// changes.
//
// A resolution change cannot be carried across: the reference frames are the
// old size, and VP8 has no way to say so mid-stream. The replacement starts
// with a key frame, which is also what a viewer needs in order to follow the
// new size.
func (e *VP8Encoder) ensureEncoder(width, height int) error {
	if e.enc != nil && e.width == width && e.height == height {
		return nil
	}

	enc, err := vp8.NewEncoder(width, height, e.fps)
	if err != nil {
		return fmt.Errorf("screen: cannot encode %dx%d: %w", width, height, err)
	}
	enc.SetKeyFrameInterval(defaultKeyFrameInterval)
	enc.SetBitrate(qualityToBitrate(e.quality, width, height, e.fps))

	// Screen content moves in whole blocks or not at all, so the motion search
	// buys nothing here and the profile skips it. See the codec package.
	enc.SetScreenContentProfile(true)

	e.enc = enc
	e.width = width
	e.height = height
	e.yuv = make([]byte, width*height*3/2)
	e.forceKey = true
	return nil
}

// codedSize returns the even dimensions VP8 requires, cropping by at most one
// pixel on each axis. An odd-sized display is unusual but not impossible, and
// losing its last row is better than refusing to encode it.
func codedSize(b image.Rectangle) (width, height int) {
	width = b.Dx() &^ 1
	height = b.Dy() &^ 1
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	return width, height
}

// qualityToBitrate maps the 1..100 quality parameter onto a bitrate, scaled by
// how much picture there is to carry.
//
// The reference point is 1080p30 at quality 70, which lands near 4 Mbps —
// roughly what a desktop stream of that size needs before text starts to smear.
// Everything else scales linearly with pixel rate.
func qualityToBitrate(quality, width, height, fps int) int {
	const refPixelRate = 1920 * 1080 * 30
	const refBitrate = 4_000_000

	pixelRate := width * height * fps
	scaled := float64(refBitrate) * float64(pixelRate) / float64(refPixelRate)
	// quality 70 is the reference; 100 doubles it and 1 cuts it to a third.
	scaled *= 0.3 + float64(quality)/100.0
	return int(scaled)
}

// Reset resets the differential baseline and forces the next frame to be
// encoded as a key frame, which is what a newly joined viewer needs.
func (e *VP8Encoder) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.differ.Reset()
	e.lastEncode = time.Time{}
	e.forceKey = true
}

// dirtyMacroblocks converts the differ's tile map into a per-macroblock mask in
// raster order.
//
// A macroblock is reported clean only if no changed tile touches it. The
// rounding is deliberately one-sided: marking a changed macroblock clean would
// freeze it on screen until the next key frame, while marking a clean one dirty
// only costs the analysis that would have happened anyway.
//
// Returning nil means "analyse everything", which is what the encoder does with
// no map. That is the answer whenever the tile map cannot be trusted to cover
// the frame.
func dirtyMacroblocks(diff *FrameDiffResult, bounds image.Rectangle, width, height int) []bool {
	if diff == nil || len(diff.ChangedTiles) == 0 {
		return nil
	}
	mbW := (width + 15) / 16
	mbH := (height + 15) / 16
	mask := make([]bool, mbW*mbH)

	for _, tile := range diff.ChangedTiles {
		// Tile coordinates carry the image's own origin; macroblocks are
		// counted from the top-left of the coded picture.
		t := tile.Sub(bounds.Min)
		x0 := t.Min.X / 16
		y0 := t.Min.Y / 16
		x1 := (t.Max.X + 15) / 16
		y1 := (t.Max.Y + 15) / 16
		for my := y0; my < y1 && my < mbH; my++ {
			if my < 0 {
				continue
			}
			for mx := x0; mx < x1 && mx < mbW; mx++ {
				if mx < 0 {
					continue
				}
				mask[my*mbW+mx] = true
			}
		}
	}
	return mask
}
