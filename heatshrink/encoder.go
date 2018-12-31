package heatshrink

import (
	"bytes"
	"log"
)

const (
	HEATSHRINK_MIN_WINDOW_BITS    = 4
	HEATSHRINK_MAX_WINDOW_BITS    = 15
	HEATSHRINK_MIN_LOOKAHEAD_BITS = 3
)

const (
	HSER_SINK_OK           = 0  /* data sunk into input buffer */
	HSER_SINK_ERROR_MISUSE = -2 /* API misuse */

	HSER_POLL_EMPTY        = 0  /* input exhausted */
	HSER_POLL_MORE         = 1  /* poll again for more output  */
	HSER_POLL_ERROR_MISUSE = -2 /* API misuse */

	HSER_FINISH_DONE = 0 /* encoding is complete */
	HSER_FINISH_MORE = 1 /* more output remaining; use poll */
)

type encoder struct {
	input_size          uint16 /* bytes in input buffer */
	match_scan_index    uint16
	match_length        uint16
	match_pos           uint16
	outgoing_bits       uint16 /* enqueued outgoing bits */
	outgoing_bits_count uint8
	finishing           bool
	state               uint8 /* current state machine node */
	current_byte        uint8 /* current byte of output */
	bit_index           uint8 /* current bit index */
	window_sz2          uint8 /* 2^n size of window */
	lookahead_sz2       uint8 /* 2^n size of lookahead */
	search_index        []int16
	buffer              []byte
	outbuf              bytes.Buffer
}

// Internal state machine states
const (
	HSES_NOT_FULL        = iota /* input buffer not full enough */
	HSES_FILLED                 /* buffer is full */
	HSES_SEARCH                 /* searching for patterns */
	HSES_YIELD_TAG_BIT          /* yield tag bit */
	HSES_YIELD_LITERAL          /* emit literal byte */
	HSES_YIELD_BR_INDEX         /* yielding backref index */
	HSES_YIELD_BR_LENGTH        /* yielding backref length */
	HSES_SAVE_BACKLOG           /* copying buffer to backlog */
	HSES_FLUSH_BITS             /* flush bit buffer */
	HSES_DONE                   /* done */
)

const (
	MATCH_NOT_FOUND           = uint16(0xffff)
	HEATSHRINK_LITERAL_MARKER = 0x01
	HEATSHRINK_BACKREF_MARKER = 0x00
)

func Compress(window, lookahead uint8, data []byte) []byte {
	hse := encoder_alloc(window, lookahead)
	size := len(data)
	inlen := 0
	for {
		_, tmp := encoder_sink(hse, data[inlen:])
		inlen += int(tmp)
		encoder_poll(hse)
		if inlen == size {
			if encoder_finish(hse) == HSER_FINISH_DONE {
				break
			}
		}
	}
	return hse.outbuf.Bytes()
}

func encoder_alloc(window_sz2, lookahead_sz2 uint8) *encoder {
	if (window_sz2 < HEATSHRINK_MIN_WINDOW_BITS) ||
		(window_sz2 > HEATSHRINK_MAX_WINDOW_BITS) ||
		(lookahead_sz2 < HEATSHRINK_MIN_LOOKAHEAD_BITS) ||
		(lookahead_sz2 >= window_sz2) {
		return nil
	}

	/* Note: 2 * the window size is used because the buffer needs to fit
	* (1 << window_sz2) bytes for the current input, and an additional
	* (1 << window_sz2) bytes for the previous buffer of input, which
	* will be scanned for useful backreferences. */
	buf_sz := (2 << window_sz2)

	hse := &encoder{}
	hse.window_sz2 = window_sz2
	hse.lookahead_sz2 = lookahead_sz2
	encoder_reset(hse)
	hse.buffer = make([]byte, buf_sz)
	hse.search_index = make([]int16, buf_sz)

	log.Printf("-- allocated encoder with buffer size of %v (%v byte input size)\n",
		buf_sz, get_input_buffer_size(hse))
	return hse
}

func encoder_reset(hse *encoder) {
	hse.input_size = 0
	hse.state = HSES_NOT_FULL
	hse.match_scan_index = 0
	hse.finishing = false
	hse.bit_index = 0x80
	hse.current_byte = 0x00
	hse.match_length = 0
	hse.outgoing_bits = 0x0000
	hse.outgoing_bits_count = 0
	hse.outbuf.Reset()
}

func encoder_sink(hse *encoder, in_buf []byte) (result int, input_size uint16) {
	/* Sinking more content after saying the content is done, tsk tsk */
	if is_finishing(hse) {
		return HSER_SINK_ERROR_MISUSE, 0
	}

	/* Sinking more content before processing is done */
	if hse.state != HSES_NOT_FULL {
		return HSER_SINK_ERROR_MISUSE, 0
	}

	write_offset := get_input_offset(hse) + hse.input_size
	ibs := get_input_buffer_size(hse)
	rem := ibs - hse.input_size
	cp_sz := rem
	if len(in_buf) < int(cp_sz) {
		cp_sz = uint16(len(in_buf))
	}

	copy(hse.buffer[write_offset:], in_buf[:cp_sz])
	hse.input_size += cp_sz

	log.Printf("-- sunk %v bytes (of %v) into encoder at %v, input buffer now has %v\n",
		cp_sz, len(in_buf), write_offset, hse.input_size)
	if cp_sz == rem {
		log.Printf("-- internal buffer is now full\n")
		hse.state = HSES_FILLED
	}

	return HSER_SINK_OK, cp_sz
}

func encoder_poll(hse *encoder) int {
	for {
		log.Printf("-- polling, state %v, finishing %v\n",
			hse.state, hse.finishing)
		switch in_state := hse.state; in_state {
		case HSES_NOT_FULL:
			return HSER_POLL_EMPTY
		case HSES_FILLED:
			do_indexing(hse)
			hse.state = HSES_SEARCH
		case HSES_SEARCH:
			hse.state = est_step_search(hse)
		case HSES_YIELD_TAG_BIT:
			hse.state = est_yield_tag_bit(hse)
		case HSES_YIELD_LITERAL:
			hse.state = est_yield_literal(hse)
		case HSES_YIELD_BR_INDEX:
			hse.state = est_yield_br_index(hse)
		case HSES_YIELD_BR_LENGTH:
			hse.state = est_yield_br_length(hse)
		case HSES_SAVE_BACKLOG:
			hse.state = est_save_backlog(hse)
		case HSES_FLUSH_BITS:
			hse.state = est_flush_bit_buffer(hse)
		case HSES_DONE:
			return HSER_POLL_EMPTY
		default:
			log.Printf("-- bad state %v\n", hse.state)
			return HSER_POLL_ERROR_MISUSE
		}
	}
}

func encoder_finish(hse *encoder) int {
	log.Printf("-- setting is_finishing flag\n")
	hse.finishing = true
	if hse.state == HSES_NOT_FULL {
		hse.state = HSES_FILLED
	}
	if hse.state == HSES_DONE {
		return HSER_FINISH_DONE
	} else {
		return HSER_FINISH_MORE
	}
}

func est_step_search(hse *encoder) uint8 {
	window_length := get_input_buffer_size(hse)
	lookahead_sz := get_lookahead_size(hse)
	msi := hse.match_scan_index
	log.Printf("## step_search, scan @ +%v (%v/%v), input size %v\n",
		msi, hse.input_size+msi, 2*window_length, hse.input_size)

	bias := lookahead_sz
	if is_finishing(hse) {
		bias = 1
	}
	if msi > hse.input_size-bias {
		/* Current search buffer is exhausted, copy it into the
		* backlog and await more input. */
		log.Printf("-- end of search @ %v\n", msi)
		if is_finishing(hse) {
			return HSES_FLUSH_BITS
		} else {
			return HSES_SAVE_BACKLOG
		}
	}
	input_offset := get_input_offset(hse)
	end := input_offset + msi
	start := end - window_length

	max_possible := lookahead_sz
	if hse.input_size-msi < lookahead_sz {
		max_possible = hse.input_size - msi
	}

	match_pos, match_length := find_longest_match(hse, start, end, max_possible)

	if match_pos == MATCH_NOT_FOUND {
		log.Printf("ss Match not found\n")
		hse.match_scan_index++
		hse.match_length = 0
		return HSES_YIELD_TAG_BIT
	} else {
		log.Printf("ss Found match of %v bytes at %v\n", match_length, match_pos)
		hse.match_pos = match_pos
		hse.match_length = match_length
		if match_pos > 1<<hse.window_sz2 /*window_length*/ {
			log.Fatal("Match assert failed.")
		}
		return HSES_YIELD_TAG_BIT
	}
}

func est_yield_tag_bit(hse *encoder) uint8 {
	if hse.match_length == 0 {
		add_tag_bit(hse, HEATSHRINK_LITERAL_MARKER)
		return HSES_YIELD_LITERAL
	} else {
		add_tag_bit(hse, HEATSHRINK_BACKREF_MARKER)
		hse.outgoing_bits = hse.match_pos - 1
		hse.outgoing_bits_count = hse.window_sz2
		return HSES_YIELD_BR_INDEX
	}
}

func est_yield_literal(hse *encoder) uint8 {
	push_literal_byte(hse)
	return HSES_SEARCH
}

func est_yield_br_index(hse *encoder) uint8 {
	log.Printf("-- yielding backref index %v\n", hse.match_pos)
	if push_outgoing_bits(hse) > 0 {
		return HSES_YIELD_BR_INDEX /* continue */
	} else {
		hse.outgoing_bits = hse.match_length - 1
		hse.outgoing_bits_count = hse.lookahead_sz2
		return HSES_YIELD_BR_LENGTH /* done */
	}
}

func est_yield_br_length(hse *encoder) uint8 {
	log.Printf("-- yielding backref length %v\n", hse.match_length)
	if push_outgoing_bits(hse) > 0 {
		return HSES_YIELD_BR_LENGTH
	} else {
		hse.match_scan_index += hse.match_length
		hse.match_length = 0
		return HSES_SEARCH
	}
}

func est_save_backlog(hse *encoder) uint8 {
	log.Printf("-- saving backlog\n")
	save_backlog(hse)
	return HSES_NOT_FULL
}

func est_flush_bit_buffer(hse *encoder) uint8 {
	if hse.bit_index == 0x80 {
		log.Printf("-- done!\n")
		return HSES_DONE
	} else {
		log.Printf("-- flushing remaining byte (bit_index == 0x%02x)\n", hse.bit_index)
		hse.outbuf.WriteByte(hse.current_byte)
		log.Printf("-- done!\n")
		return HSES_DONE
	}
}

func add_tag_bit(hse *encoder, tag uint8) {
	log.Printf("-- adding tag bit: %v\n", tag)
	push_bits(hse, 1, tag)
}

func get_input_offset(hse *encoder) uint16 {
	return get_input_buffer_size(hse)
}

func get_input_buffer_size(hse *encoder) uint16 {
	return 1 << hse.window_sz2
}

func get_lookahead_size(hse *encoder) uint16 {
	return 1 << hse.lookahead_sz2
}

func do_indexing(hse *encoder) {
	/* Build an index array I that contains flattened linked lists
	* for the previous instances of every byte in the buffer.
	*
	* For example, if buf[200] == 'x', then index[200] will either
	* be an offset i such that buf[i] == 'x', or a negative offset
	* to indicate end-of-list. This significantly speeds up matching,
	* while only using sizeof(uint16_t)*sizeof(buffer) bytes of RAM.
	*
	* Future optimization options:
	* 1. Since any negative value represents end-of-list, the other
	*    15 bits could be used to improve the index dynamically.
	*
	* 2. Likewise, the last lookahead_sz bytes of the index will
	*    not be usable, so temporary data could be stored there to
	*    dynamically improve the index.
	* */
	last := [256]int16{}
	for i := range last {
		last[i] = -1
	}
	data := hse.buffer
	index := hse.search_index

	input_offset := get_input_offset(hse)
	end := input_offset + hse.input_size

	for i := 0; i < int(end); i++ {
		v := data[i]
		lv := last[v]
		index[i] = lv
		last[v] = int16(i)
	}
}

func is_finishing(hse *encoder) bool {
	return hse.finishing
}

/* Return the longest match for the bytes at buf[end:end+maxlen] between
* buf[start] and buf[end-1]. If no match is found, return -1. */
func find_longest_match(hse *encoder, start, end, maxlen uint16) (match_pos, match_length uint16) {
	log.Printf("-- scanning for match of buf[%v:%v] between buf[%v:%v] (max %v bytes)\n",
		end, end+maxlen, start, end+maxlen-1, maxlen)

	match_maxlen := uint16(0)
	match_index := MATCH_NOT_FOUND

	len := uint16(0)
	needlepoint := hse.buffer[end:]
	pos := hse.search_index[end]

	for int16(pos)-int16(start) >= 0 {
		pospoint := hse.buffer[pos:]
		len = 0

		/* Only check matches that will potentially beat the current maxlen.
		* This is redundant with the index if match_maxlen is 0, but the
		* added branch overhead to check if it == 0 seems to be worse. */
		if pospoint[match_maxlen] != needlepoint[match_maxlen] {
			pos = hse.search_index[pos]
			continue
		}

		for len = 1; len < maxlen; len++ {
			if pospoint[len] != needlepoint[len] {
				break
			}
		}

		if len > match_maxlen {
			match_maxlen = len
			match_index = uint16(pos)
			if len == maxlen {
				break
			} /* won't find better */
		}
		pos = hse.search_index[pos]
	}

	break_even_point := 1 + uint16(hse.window_sz2) + uint16(hse.lookahead_sz2)

	/* Instead of comparing break_even_point against 8*match_maxlen,
	* compare match_maxlen against break_even_point/8 to avoid
	* overflow. Since MIN_WINDOW_BITS and MIN_LOOKAHEAD_BITS are 4 and
	* 3, respectively, break_even_point/8 will always be at least 1. */
	if match_maxlen > (break_even_point / 8) {
		log.Printf("-- best match: %v bytes at -%v\n",
			match_maxlen, end-match_index)
		return end - match_index, match_maxlen
	}
	log.Printf("-- none found\n")
	return MATCH_NOT_FOUND, 0
}

func push_outgoing_bits(hse *encoder) uint8 {
	var count, bits uint8
	if hse.outgoing_bits_count > 8 {
		count = 8
		bits = uint8(hse.outgoing_bits >> (hse.outgoing_bits_count - 8))
	} else {
		count = hse.outgoing_bits_count
		bits = uint8(hse.outgoing_bits)
	}
	if count > 0 {
		log.Printf("-- pushing %v outgoing bits: 0x%02x\n", count, bits)
		push_bits(hse, count, bits)
		hse.outgoing_bits_count -= count
	}
	return count
}

/* Push COUNT (max 8) bits to the output buffer, which has room.
* Bytes are set from the lowest bits, up. */
func push_bits(hse *encoder, count, bits uint8) {
	if count > 8 {
		log.Fatal("Bit count assert failed.")
	}
	log.Printf("++ push_bits: %v bits, input of 0x%02x\n", count, bits)

	/* If adding a whole byte and at the start of a new output byte,
	* just push it through whole and skip the bit IO loop. */
	if count == 8 && hse.bit_index == 0x80 {
		hse.outbuf.WriteByte(bits)
	} else {
		for i := int(count) - 1; i >= 0; i-- {
			if bits&(1<<uint(i)) != 0 {
				hse.current_byte |= hse.bit_index
			}
			hse.bit_index >>= 1
			if hse.bit_index == 0x00 {
				hse.bit_index = 0x80
				log.Printf(" > pushing byte 0x%02x\n", hse.current_byte)
				hse.outbuf.WriteByte(hse.current_byte)
				hse.current_byte = 0x00
			}
		}
	}
}

func push_literal_byte(hse *encoder) {
	processed_offset := hse.match_scan_index - 1
	input_offset := get_input_offset(hse) + processed_offset
	c := hse.buffer[input_offset]
	log.Printf("-- yielded literal byte 0x%02x from +%v\n", c, input_offset)
	push_bits(hse, 8, c)
}

func save_backlog(hse *encoder) {
	input_buf_sz := get_input_buffer_size(hse)
	msi := hse.match_scan_index

	/* Copy processed data to beginning of buffer, so it can be
	* used for future matches. Don't bother checking whether the
	* input is less than the maximum size, because if it isn't,
	* we're done anyway. */
	rem := input_buf_sz - msi // unprocessed bytes

	copy(hse.buffer, hse.buffer[input_buf_sz-rem:])

	hse.match_scan_index = 0
	hse.input_size -= input_buf_sz - rem
}
