//go:build !race

package screen

// raceDetectorEnabled reports whether the binary was built with -race.
// See requireNoUpstreamRace in capture_test.go.
const raceDetectorEnabled = false
