# heatshrink
GO implementation of heatshrink

This is a quick and dirty port of Heatshrink by "Atomic Object": https://github.com/atomicobject/heatshrink.

Sometimes your GOLANG based server system needs to communicate with embedded systems running heatshrink, your GO program needs to compress or decompress using heatshrink algorithm. This is what this project is created for.

As I said, quick and dirty, I haven't done enough test.

On a GO program, memory for running heatshrink actually is not a big program, so I simplifed the interface, it's just two easy  functions.

func heatshrink.Compress(window_sz2, lookahead_sz2 uint8, data[] byte) []byte
func heatshrink.Decompress(window_sz2, lookahead_sz2 uint8, data[] byte) []byte

