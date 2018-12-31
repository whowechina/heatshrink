package main

import (
	"fmt"
	"io/ioutil"
	"log"

	"github.com/whowechina/heatshrink/heatshrink"
)

func main() {
	log.SetOutput(ioutil.Discard)
	in := []byte("HELLO WORLD THINKS WORLD GREAT")

	out := heatshrink.Compress(8, 3, in)
	fmt.Printf("Compress: %v -> %v\n", len(in), len(out))

	out2 := heatshrink.Decompress(8, 3, out)
	fmt.Printf("Decompress: %v -> %v\n", len(out), len(out2))
}
