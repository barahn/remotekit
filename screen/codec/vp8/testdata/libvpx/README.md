# libvpx VP8 test vectors

`vp80-00-comprehensive-001.ivf` to `-018.ivf`, and the per-frame MD5s of
their decoded output, are libvpx's own VP8 conformance vectors. They come
unmodified from the WebM project's test data store:

    https://storage.googleapis.com/downloads.webmproject.org/test_data/libvpx/

They are © The WebM Project authors, under libvpx's BSD-3-Clause licence and
patent grant, which are reproduced beside them (`LICENSE`, `PATENTS`, `AUTHORS`,
from https://chromium.googlesource.com/webm/libvpx). They are test data only;
nothing here is compiled into the package.

`TestDecoderMatchesLibvpxTestVectors` decodes every vector and checks each
shown frame against its MD5. Between them the vectors exercise bitstream
versions 0 to 3 (six-tap, bilinear and full-pixel prediction), SPLITMV, the
normal and simple loop filters, loop filter deltas, segmentation, golden and
altref references and the copies between them, a hidden frame, multiple token
partitions and sizes that are not whole macroblocks. None sets a sign bias.
