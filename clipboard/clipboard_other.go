//go:build !windows && !linux && !darwin

package clipboard

// NewManager returns a memory fallback clipboard manager on non-supported platforms.
func NewManager() Manager {
	return NewMemoryClipboard()
}
