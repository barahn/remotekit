package screen

import (
	"image"
	"image/color"
	"testing"
)

func TestFrameDiffer_FirstFrame(t *testing.T) {
	differ := NewFrameDiffer(64)
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))

	result := differ.Compare(img)

	if !result.IsDirty {
		t.Error("expected first frame to be dirty (full refresh)")
	}
	if result.TotalTiles == 0 {
		t.Error("expected total tiles > 0")
	}
	if result.ChangedCount != result.TotalTiles {
		t.Errorf("expected all %d tiles to be marked changed on first frame, got %d", result.TotalTiles, result.ChangedCount)
	}
	if result.BoundingBox != img.Bounds() {
		t.Errorf("expected bounding box %v, got %v", img.Bounds(), result.BoundingBox)
	}
}

func TestFrameDiffer_IdenticalFrames(t *testing.T) {
	differ := NewFrameDiffer(64)
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))

	// First frame sets baseline
	differ.Compare(img)

	// Second frame with same content
	result := differ.Compare(img)

	if result.IsDirty {
		t.Error("expected identical frame to NOT be dirty")
	}
	if result.ChangedCount != 0 {
		t.Errorf("expected 0 changed tiles, got %d", result.ChangedCount)
	}
}

func TestFrameDiffer_PartialChange(t *testing.T) {
	differ := NewFrameDiffer(64)
	img1 := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	differ.Compare(img1)

	// Create second frame with a modified rectangle in top-left
	img2 := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	copy(img2.Pix, img1.Pix)

	// Modify pixels in tile (0, 0)
	img2.Set(10, 10, color.RGBA{R: 255, G: 0, B: 0, A: 255})

	result := differ.Compare(img2)

	if !result.IsDirty {
		t.Error("expected modified frame to be dirty")
	}
	if result.ChangedCount != 1 {
		t.Errorf("expected exactly 1 changed tile, got %d", result.ChangedCount)
	}
	expectedTile := image.Rect(0, 0, 64, 64)
	if len(result.ChangedTiles) > 0 && result.ChangedTiles[0] != expectedTile {
		t.Errorf("expected changed tile %v, got %v", expectedTile, result.ChangedTiles[0])
	}
}

func TestFrameDiffer_Reset(t *testing.T) {
	differ := NewFrameDiffer(64)
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))

	differ.Compare(img)
	res2 := differ.Compare(img)
	if res2.IsDirty {
		t.Error("expected clean frame before reset")
	}

	differ.Reset()
	res3 := differ.Compare(img)
	if !res3.IsDirty {
		t.Error("expected dirty frame after reset")
	}
}

func BenchmarkFrameDiffer_FullFrame(b *testing.B) {
	differ := NewFrameDiffer(64)
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	differ.Compare(img) // Warmup

	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(1920 * 1080 * 4))

	for i := 0; i < b.N; i++ {
		differ.Compare(img)
	}
}
