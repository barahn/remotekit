package screen

import "image"

// RGBAToI420ForTest exposes the colour conversion to the package's external
// tests, so a conformance test can compare the decoder's output against the
// same pixels the encoder was given rather than against a second, subtly
// different conversion written for the test.
func RGBAToI420ForTest(dst []byte, img *image.RGBA, width, height int) {
	rgbaToI420(dst, img, width, height)
}
