package heatshrink

import (
	"bytes"
	"log"
)

/* States for the polling state machine. */
const (
	HSDS_TAG_BIT           = iota /* tag bit */
	HSDS_YIELD_LITERAL            /* ready to yield literal byte */
	HSDS_BACKREF_INDEX_MSB        /* most significant byte of index */
	HSDS_BACKREF_INDEX_LSB        /* least significant byte of index */
	HSDS_BACKREF_COUNT_MSB        /* most significant byte of count */
	HSDS_BACKREF_COUNT_LSB        /* least significant byte of count */
	HSDS_YIELD_BACKREF            /* ready to yield back-reference */
)

const (
	HSDR_SINK_OK   = 0 /* data sunk, ready to poll */
	HSDR_SINK_FULL = 1 /* out of space in internal buffer */

	HSDR_POLL_EMPTY         = 0 /* input exhausted */
	HSDR_POLL_MORE          = 1 /* more data remaining, call again w/ fresh output buffer */
	HSDR_POLL_ERROR_UNKNOWN = -2

	HSDR_FINISH_DONE = 0 /* output is done */
	HSDR_FINISH_MORE = 1 /* more output remains */
)

const (
	NO_BITS = uint16(0xffff)
)

type decoder struct {
	input_size   uint16 /* bytes in input buffer */
	input_index  uint16 /* offset to next unprocessed input byte */
	output_count uint16 /* how many bytes to output */
	output_index uint16 /* index for bytes to output */
	head_index   uint16 /* head of window buffer */
	state        uint8  /* current state machine node */
	current_byte uint8  /* current byte of input */
	bit_index    uint8  /* current bit index */

	/* Fields that are only used if dynamically allocated. */
	window_sz2    uint8 /* window buffer bits */
	lookahead_sz2 uint8 /* lookahead bits */

	/* Input buffer, then expansion window buffer */
	decbuf []byte
	inbuf  []byte
	outbuf bytes.Buffer
}

func Decompress(window, lookahead uint8, data []byte) []byte {
	hsd := decoder_alloc(window, lookahead)
	size := len(data)
	inlen := 0
	for {
		_, tmp := decoder_sink(hsd, data[inlen:])
		inlen += int(tmp)
		decoder_poll(hsd)
		if inlen == size {
			if decoder_finish(hsd) == HSDR_FINISH_DONE {
				break
			}
		}
	}
	return hsd.outbuf.Bytes()
}

func decoder_alloc(window_sz2, lookahead_sz2 uint8) *decoder {
	if (window_sz2 < HEATSHRINK_MIN_WINDOW_BITS) ||
		(window_sz2 > HEATSHRINK_MAX_WINDOW_BITS) ||
		(lookahead_sz2 < HEATSHRINK_MIN_LOOKAHEAD_BITS) ||
		(lookahead_sz2 >= window_sz2) {
		return nil
	}
	hsd := &decoder{}
	hsd.window_sz2 = window_sz2
	hsd.lookahead_sz2 = lookahead_sz2
	hsd.decbuf = make([]byte, 1<<hsd.window_sz2)
	hsd.inbuf = make([]byte, 65535)
	decoder_reset(hsd)
	log.Printf("-- allocated decoder with buffer size of %v + %v\n",
		len(hsd.decbuf), len(hsd.inbuf))
	return hsd
}

func decoder_reset(hsd *decoder) {
	hsd.state = HSDS_TAG_BIT
	hsd.input_size = 0
	hsd.input_index = 0
	hsd.bit_index = 0x00
	hsd.current_byte = 0x00
	hsd.output_count = 0
	hsd.output_index = 0
	hsd.head_index = 0
	hsd.outbuf.Reset()
}

/* Copy SIZE bytes into the decoder's input buffer, if it will fit. */
func decoder_sink(hsd *decoder, data []byte) (result int, input_size uint16) {
	rem := uint16(len(hsd.inbuf)) - hsd.input_size
	if rem == 0 {
		return HSDR_SINK_FULL, 0
	}

	size := rem
	if len(data) < int(size) {
		size = uint16(len(data))
	}
	log.Printf("-- sinking %v bytes\n", size)
	/* copy into input buffer (at head of buffers) */
	copy(hsd.inbuf[hsd.input_size:], data[:size])
	hsd.input_size += size
	return HSDR_SINK_OK, size
}

func decoder_poll(hsd *decoder) int {
	for {
		log.Printf("-- poll, state is %v, input_size %v\n",
			hsd.state, hsd.input_size)
		in_state := hsd.state
		switch in_state {
		case HSDS_TAG_BIT:
			hsd.state = dst_tag_bit(hsd)
		case HSDS_YIELD_LITERAL:
			hsd.state = dst_yield_literal(hsd)
		case HSDS_BACKREF_INDEX_MSB:
			hsd.state = dst_backref_index_msb(hsd)
		case HSDS_BACKREF_INDEX_LSB:
			hsd.state = dst_backref_index_lsb(hsd)
		case HSDS_BACKREF_COUNT_MSB:
			hsd.state = dst_backref_count_msb(hsd)
		case HSDS_BACKREF_COUNT_LSB:
			hsd.state = dst_backref_count_lsb(hsd)
		case HSDS_YIELD_BACKREF:
			hsd.state = dst_yield_backref(hsd)
		default:
			return HSDR_POLL_ERROR_UNKNOWN
		}

		/* If the current state cannot advance, check if input or output
		* buffer are exhausted. */
		if hsd.state == in_state {
			return HSDR_POLL_EMPTY
		}
	}
}

func decoder_finish(hsd *decoder) int {
	switch hsd.state {
	case HSDS_TAG_BIT,
		/* If we want to finish with no input, but are in these states, it's
		* because the 0-bit padding to the last byte looks like a backref
		* marker bit followed by all 0s for index and count bits. */
		HSDS_BACKREF_INDEX_LSB,
		HSDS_BACKREF_INDEX_MSB,
		HSDS_BACKREF_COUNT_LSB,
		HSDS_BACKREF_COUNT_MSB,

		/* If the output stream is padded with 0xFFs (possibly due to being in
		* flash memory), also explicitly check the input size rather than
		* uselessly returning MORE but yielding 0 bytes when polling. */
		HSDS_YIELD_LITERAL:
		if hsd.input_size == 0 {
			return HSDR_FINISH_DONE
		} else {
			return HSDR_FINISH_MORE
		}
	}
	return HSDR_FINISH_MORE
}

func dst_tag_bit(hsd *decoder) uint8 {
	bits := get_bits(hsd, 1) // get tag bit
	if bits == NO_BITS {
		return HSDS_TAG_BIT
	} else if bits > 0 {
		return HSDS_YIELD_LITERAL
	} else if hsd.window_sz2 > 8 {
		return HSDS_BACKREF_INDEX_MSB
	} else {
		hsd.output_index = 0
		return HSDS_BACKREF_INDEX_LSB
	}
}

func dst_yield_literal(hsd *decoder) uint8 {
	/* Emit a repeated section from the window buffer, and add it (again)
	* to the window buffer. (Note that the repetition can include
	* itself.)*/
	bits := get_bits(hsd, 8)
	if bits == NO_BITS {
		return HSDS_YIELD_LITERAL
	} /* out of input */
	mask := uint16(1<<hsd.window_sz2) - 1
	c := uint8(bits & 0xFF)
	log.Printf("-- emitting literal byte 0x%02x\n", c)
	hsd.decbuf[hsd.head_index&mask] = c
	hsd.head_index++
	push_byte(hsd, c)
	return HSDS_TAG_BIT
}

func dst_backref_index_msb(hsd *decoder) uint8 {
	bit_ct := hsd.window_sz2
	if bit_ct <= 8 {
		log.Fatal("Bit count assert failed.")
	}
	bits := get_bits(hsd, bit_ct-8)
	log.Printf("-- backref index (msb), got 0x%04x (+1)\n", bits)
	if bits == NO_BITS {
		return HSDS_BACKREF_INDEX_MSB
	}
	hsd.output_index = bits << 8
	return HSDS_BACKREF_INDEX_LSB
}

func dst_backref_index_lsb(hsd *decoder) uint8 {
	bit_ct := hsd.window_sz2
	if bit_ct > 8 {
		bit_ct = 8
	}
	bits := get_bits(hsd, bit_ct)
	log.Printf("-- backref index (lsb), got 0x%04x (+1)\n", bits)
	if bits == NO_BITS {
		return HSDS_BACKREF_INDEX_LSB
	}
	hsd.output_index |= bits
	hsd.output_index++
	br_bit_ct := hsd.lookahead_sz2
	hsd.output_count = 0
	if br_bit_ct > 8 {
		return HSDS_BACKREF_COUNT_MSB
	} else {
		return HSDS_BACKREF_COUNT_LSB
	}
}

func dst_backref_count_msb(hsd *decoder) uint8 {
	br_bit_ct := hsd.lookahead_sz2
	if br_bit_ct <= 8 {
		log.Fatal("Bit count asser failed.")
	}
	bits := get_bits(hsd, br_bit_ct-8)
	log.Printf("-- backref count (msb), got 0x%04x (+1)\n", bits)
	if bits == NO_BITS {
		return HSDS_BACKREF_COUNT_MSB
	}
	hsd.output_count = bits << 8
	return HSDS_BACKREF_COUNT_LSB
}

func dst_backref_count_lsb(hsd *decoder) uint8 {
	br_bit_ct := hsd.lookahead_sz2
	if br_bit_ct > 8 {
		br_bit_ct = 8
	}
	bits := get_bits(hsd, br_bit_ct)
	log.Printf("-- backref count (lsb), got 0x%04x (+1)\n", bits)
	if bits == NO_BITS {
		return HSDS_BACKREF_COUNT_LSB
	}
	hsd.output_count |= bits
	hsd.output_count++
	return HSDS_YIELD_BACKREF
}

func dst_yield_backref(hsd *decoder) uint8 {
	count := hsd.output_count
	mask := uint16(1<<hsd.window_sz2) - 1
	neg_offset := hsd.output_index
	log.Printf("-- emitting %v bytes from -%v bytes back\n", count, neg_offset)
	if neg_offset > mask+1 {
		log.Fatal("neg_offset assert failed.")
	}
	if count > (1 << hsd.lookahead_sz2) {
		log.Fatal("count assert failed.")
	}

	for i := uint16(0); i < count; i++ {
		c := hsd.decbuf[(hsd.head_index-neg_offset)&mask]
		push_byte(hsd, c)
		hsd.decbuf[hsd.head_index&mask] = c
		hsd.head_index++
		log.Printf("  -- ++ 0x%02x\n", c)
	}
	hsd.output_count -= count
	if hsd.output_count == 0 {
		return HSDS_TAG_BIT
	}
	return HSDS_YIELD_BACKREF
}

/* Get the next COUNT bits from the input buffer, saving incremental progress.
* Returns NO_BITS on end of input, or if more than 15 bits are requested. */
func get_bits(hsd *decoder, count uint8) uint16 {
	accumulator := uint16(0)
	if count > 15 {
		return NO_BITS
	}
	log.Printf("-- popping %v bit(s)\n", count)

	/* If we aren't able to get COUNT bits, suspend immediately, because we
	* don't track how many bits of COUNT we've accumulated before suspend. */
	if hsd.input_size == 0 {
		if hsd.bit_index < (1 << (count - 1)) {
			return NO_BITS
		}
	}

	for i := uint8(0); i < count; i++ {
		if hsd.bit_index == 0x00 {
			if hsd.input_size == 0 {
				log.Printf("  -- out of bits, suspending w/ accumulator of %v (0x%02x)\n",
					accumulator, accumulator)
				return NO_BITS
			}
			hsd.current_byte = hsd.inbuf[hsd.input_index]
			hsd.input_index++
			log.Printf("  -- pulled byte 0x%02x\n", hsd.current_byte)
			if hsd.input_index == hsd.input_size {
				hsd.input_index = 0 /* input is exhausted */
				hsd.input_size = 0
			}
			hsd.bit_index = 0x80
		}
		accumulator <<= 1
		if hsd.current_byte&hsd.bit_index > 0 {
			accumulator |= 0x01
		}
		hsd.bit_index >>= 1
	}

	if count > 1 {
		log.Printf("  -- accumulated %08x\n", accumulator)
	}
	return accumulator
}

func push_byte(hsd *decoder, byte uint8) {
	log.Printf(" -- pushing byte: 0x%02x\n", byte)
	hsd.outbuf.WriteByte(byte)
}
