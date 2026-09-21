// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package screen

import (
	"hash/crc32"
	"image"
	"sync"
)

const (
	// DefaultTileSize is the default width and height (in pixels) of tile blocks used for diffing.
	DefaultTileSize = 64
)

// FrameDiffResult contains information about what changed between two consecutive frames.
type FrameDiffResult struct {
	// IsDirty is true if any region of the frame changed.
	IsDirty bool

	// ChangedTiles contains the bounding rectangles of tiles that were modified.
	ChangedTiles []image.Rectangle

	// BoundingBox is the minimum bounding rectangle enclosing all changed tiles.
	// Is empty if IsDirty is false.
	BoundingBox image.Rectangle

	// TotalTiles is the total number of tiles evaluated.
	TotalTiles int

	// ChangedCount is the number of tiles that changed.
	ChangedCount int
}

// FrameDiffer performs fast differential comparison between consecutive RGBA frames
// to avoid encoding and transmitting static or unchanged regions.
type FrameDiffer struct {
	tileSize   int
	prevPix    []byte
	prevHashes []uint32
	cols       int
	rows       int
	bounds     image.Rectangle
	mu         sync.Mutex
}

// NewFrameDiffer creates a FrameDiffer with the specified tile size.
// If tileSize <= 0, DefaultTileSize (64) is used.
func NewFrameDiffer(tileSize int) *FrameDiffer {
	if tileSize <= 0 {
		tileSize = DefaultTileSize
	}
	return &FrameDiffer{
		tileSize: tileSize,
	}
}

// Compare compares the current RGBA frame against the previous frame.
// Returns a FrameDiffResult indicating changed regions.
// On the first call, all tiles are marked as changed (full refresh).
func (d *FrameDiffer) Compare(img *image.RGBA) *FrameDiffResult {
	if img == nil || img.Bounds().Empty() {
		return &FrameDiffResult{}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	cols := (w + d.tileSize - 1) / d.tileSize
	rows := (h + d.tileSize - 1) / d.tileSize
	totalTiles := cols * rows

	// Re-initialize buffers if dimensions changed
	if d.bounds != bounds || len(d.prevHashes) != totalTiles {
		d.bounds = bounds
		d.cols = cols
		d.rows = rows
		d.prevHashes = make([]uint32, totalTiles)
		d.prevPix = make([]byte, len(img.Pix))
		copy(d.prevPix, img.Pix)

		// First frame: mark everything as changed
		changed := make([]image.Rectangle, 0, totalTiles)
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				tileRect := d.tileBounds(c, r, w, h, bounds.Min)
				changed = append(changed, tileRect)
				d.prevHashes[r*cols+c] = d.hashTile(img.Pix, img.Stride, tileRect)
			}
		}

		return &FrameDiffResult{
			IsDirty:      true,
			ChangedTiles: changed,
			BoundingBox:  bounds,
			TotalTiles:   totalTiles,
			ChangedCount: totalTiles,
		}
	}

	// Compare tiles against cached hashes
	changed := make([]image.Rectangle, 0, 16)
	var minX, minY, maxX, maxY int
	firstChange := true

	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			tileIndex := r*cols + c
			tileRect := d.tileBounds(c, r, w, h, bounds.Min)

			currentHash := d.hashTile(img.Pix, img.Stride, tileRect)
			if currentHash != d.prevHashes[tileIndex] {
				d.prevHashes[tileIndex] = currentHash
				changed = append(changed, tileRect)

				if firstChange {
					minX, minY = tileRect.Min.X, tileRect.Min.Y
					maxX, maxY = tileRect.Max.X, tileRect.Max.Y
					firstChange = false
				} else {
					if tileRect.Min.X < minX {
						minX = tileRect.Min.X
					}
					if tileRect.Min.Y < minY {
						minY = tileRect.Min.Y
					}
					if tileRect.Max.X > maxX {
						maxX = tileRect.Max.X
					}
					if tileRect.Max.Y > maxY {
						maxY = tileRect.Max.Y
					}
				}
			}
		}
	}

	// Update cached raw pixel buffer
	copy(d.prevPix, img.Pix)

	if len(changed) == 0 {
		return &FrameDiffResult{
			IsDirty:    false,
			TotalTiles: totalTiles,
		}
	}

	return &FrameDiffResult{
		IsDirty:      true,
		ChangedTiles: changed,
		BoundingBox:  image.Rect(minX, minY, maxX, maxY),
		TotalTiles:   totalTiles,
		ChangedCount: len(changed),
	}
}

// Reset clears the cached previous frame state, forcing the next Compare to report a full refresh.
func (d *FrameDiffer) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.prevPix = nil
	d.prevHashes = nil
	d.bounds = image.Rectangle{}
}

// tileBounds calculates the image rectangle for a given tile coordinate (col, row).
func (d *FrameDiffer) tileBounds(col, row, totalW, totalH int, origin image.Point) image.Rectangle {
	x0 := origin.X + col*d.tileSize
	y0 := origin.Y + row*d.tileSize
	x1 := x0 + d.tileSize
	y1 := y0 + d.tileSize

	if x1 > origin.X+totalW {
		x1 = origin.X + totalW
	}
	if y1 > origin.Y+totalH {
		y1 = origin.Y + totalH
	}

	return image.Rect(x0, y0, x1, y1)
}

// hashTile calculates IEEE CRC32 checksum for the pixels in a tile region.
func (d *FrameDiffer) hashTile(pix []byte, stride int, rect image.Rectangle) uint32 {
	h := crc32.NewIEEE()
	bytesPerPixel := 4
	rowBytes := rect.Dx() * bytesPerPixel

	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		offset := y*stride + rect.Min.X*bytesPerPixel
		_, _ = h.Write(pix[offset : offset+rowBytes])
	}

	return h.Sum32()
}
