# heatshrink
GO implementation of heatshrink

Sometimes your GOLANG based server system needs to communicate with embedded systems running heatshrink. Your GO program needs to compress or decompress using heatshrink algorithm. This is what this project is created for.

This is a quick and dirty port of Heatshrink by "Atomic Object": https://github.com/atomicobject/heatshrink.
I kept most of the original C naming - golint doesn't seem to agree with it so you'll see lots of golint warnings.

As I said, quick and dirty, I haven't done enough test.

On a GO program, memory for running heatshrink actually is not a big program, so I simplifed the interface, it's just two easy  functions.

func heatshrink.Compress(window, lookahead uint8, data[] byte) []byte

func heatshrink.Decompress(window, lookahead uint8, data[] byte) []byte

