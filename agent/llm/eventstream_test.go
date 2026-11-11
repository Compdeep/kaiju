package llm

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"testing"
	"testing/iotest"
)

// encodeEvent is one AWS event-stream message with string headers, for tests.
func encodeEvent(headers map[string]string, payload []byte) []byte {
	var h bytes.Buffer
	for name, value := range headers {
		h.WriteByte(byte(len(name)))
		h.WriteString(name)
		h.WriteByte(7)
		_ = binary.Write(&h, binary.BigEndian, uint16(len(value)))
		h.WriteString(value)
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(16+h.Len()+len(payload)))
	_ = binary.Write(&b, binary.BigEndian, uint32(h.Len()))
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(b.Bytes()))
	b.Write(h.Bytes())
	b.Write(payload)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(b.Bytes()))
	return b.Bytes()
}

// AWS's own test vectors: an empty message, and a payload with no headers.
func TestReadEventAWSVectors(t *testing.T) {
	for want, raw := range map[string]string{
		"":              "000000100000000005c248eb7d98c8ff",
		"{'foo':'bar'}": "0000001d00000000fd528c5a7b27666f6f273a27626172277dc3653936",
	} {
		b, _ := hex.DecodeString(raw)
		ev, err := readEvent(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if string(ev.payload) != want || len(ev.headers) != 0 {
			t.Errorf("%s: payload %q headers %v", raw, ev.payload, ev.headers)
		}
	}
}

// Messages read one after another, whatever sizes the reads come in, and the
// end of the stream between messages is io.EOF.
func TestReadEventStream(t *testing.T) {
	var s bytes.Buffer
	s.Write(encodeEvent(map[string]string{":event-type": "a", ":message-type": "event"}, []byte(`{"x":1}`)))
	s.Write(encodeEvent(map[string]string{":event-type": "b"}, nil))
	r := iotest.OneByteReader(&s)
	first, err := readEvent(r)
	if err != nil || first.headers[":event-type"] != "a" || first.headers[":message-type"] != "event" || string(first.payload) != `{"x":1}` {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if second, err := readEvent(r); err != nil || second.headers[":event-type"] != "b" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if _, err := readEvent(r); err != io.EOF {
		t.Errorf("after the last message: %v, want io.EOF", err)
	}
}

// A damaged message is refused, not read; a cut one is unexpected EOF.
func TestReadEventRefusesDamage(t *testing.T) {
	good := encodeEvent(map[string]string{":event-type": "a"}, []byte("payload"))
	for i := range []int{0, 1} {
		bad := append([]byte(nil), good...)
		bad[[]int{9, len(bad) - 6}[i]] ^= 0xff // prelude checksum, then payload
		if _, err := readEvent(bytes.NewReader(bad)); err == nil {
			t.Errorf("damage %d read without error", i)
		}
	}
	if _, err := readEvent(bytes.NewReader(good[:len(good)-3])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("cut message: %v", err)
	}
}
