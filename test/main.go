package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"

	"github.com/whowechina/heatshrink"
)

func main() {
	// Turn off annoying debuging logs
	log.SetOutput(ioutil.Discard)

	str := "Hello world."
	// Simple trick to make a string longer
	for i := 0; i < 13; i++ {
		str += str
	}
	in := []byte(str)

	// Compress
	out := heatshrink.Compress(8, 3, in)
	fmt.Printf("Compress: %v -> %v\n", len(in), len(out))

	// Decompress
	out2 := heatshrink.Decompress(8, 3, out)
	fmt.Printf("Decompress: %v -> %v Equal: %v\n", len(out), len(out2), bytes.Equal(in, out2))
}
