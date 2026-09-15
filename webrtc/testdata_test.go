package webrtc

import (
	"os"
	"path/filepath"
	"testing"
)

// vp8KeyFrameForTest returns a real VP8 key frame.
//
// It is read from testdata rather than generated here, because generating it
// would make pkg/webrtc depend on pkg/screen and invert the dependency: the
// screen package is what drives WebRTC, not the other way round.
func vp8KeyFrameForTest(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "keyframe.vp8"))
	if err != nil {
		t.Fatalf("reading the VP8 test frame: %v", err)
	}
	if len(data) == 0 || data[0]&1 != 0 {
		t.Fatal("testdata/keyframe.vp8 is not a VP8 key frame")
	}
	return data
}
