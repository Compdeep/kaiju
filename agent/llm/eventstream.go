package llm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// AWS's event-stream encoding, which Bedrock's ConverseStream answers in.
//
// Each message: total length, headers length and a checksum of those two (four
// bytes each, big-endian), then the headers, the payload, and a checksum of
// everything before it. A header is a one-byte name length, the name, a
// one-byte type and a value whose size the type gives. Only string headers
// carry anything Bedrock's events need; the rest are read past.

// streamEvent is one message: its string headers and its payload.
type streamEvent struct {
	headers map[string]string
	payload []byte
}

// maxEventSize bounds one message. Bedrock's events are a few kilobytes; a
// length past this is a damaged stream, not a message to allocate for.
const maxEventSize = 16 << 20

// headerValueSize is the fixed size of each non-string header type's value;
// -1 marks the types that carry a two-byte length first.
var headerValueSize = [...]int{0: 0, 1: 0, 2: 1, 3: 2, 4: 4, 5: 8, 6: -1, 7: -1, 8: 8, 9: 16}

/*
 * readEvent reads one message.
 * param: r - the stream.
 * return: the message; io.EOF at a clean end between messages; an error for a
 *         cut or damaged one.
 */
func readEvent(r io.Reader) (streamEvent, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return streamEvent{}, err
	}
	total := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:12]) {
		return streamEvent{}, errors.New("event stream: prelude checksum mismatch")
	}
	if total < 16 || total > maxEventSize || headersLen > total-16 {
		return streamEvent{}, fmt.Errorf("event stream: bad lengths %d/%d", total, headersLen)
	}
	rest := make([]byte, total-12)
	if _, err := io.ReadFull(r, rest); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return streamEvent{}, err
	}
	body := rest[:len(rest)-4]
	sum := crc32.Update(crc32.ChecksumIEEE(prelude[:]), crc32.IEEETable, body)
	if sum != binary.BigEndian.Uint32(rest[len(rest)-4:]) {
		return streamEvent{}, errors.New("event stream: message checksum mismatch")
	}
	headers, err := readHeaders(body[:headersLen])
	if err != nil {
		return streamEvent{}, err
	}
	return streamEvent{headers: headers, payload: body[headersLen:]}, nil
}

// readHeaders keeps the string headers and reads past the rest.
func readHeaders(b []byte) (map[string]string, error) {
	out := map[string]string{}
	bad := errors.New("event stream: malformed header")
	for len(b) > 0 {
		n := int(b[0])
		if len(b) < 2+n {
			return nil, bad
		}
		name, typ := string(b[1:1+n]), int(b[1+n])
		b = b[2+n:]
		if typ >= len(headerValueSize) {
			return nil, bad
		}
		size := headerValueSize[typ]
		if size < 0 {
			if len(b) < 2 {
				return nil, bad
			}
			size = 2 + int(binary.BigEndian.Uint16(b))
		}
		if len(b) < size {
			return nil, bad
		}
		if typ == 7 {
			out[name] = string(b[2:size])
		}
		b = b[size:]
	}
	return out, nil
}
