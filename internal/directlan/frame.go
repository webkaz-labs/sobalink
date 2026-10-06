package directlan

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const maxControlFrame = 8192

var errFrame = errors.New("invalid direct LAN frame")

func writeFrame(w io.Writer, data []byte, max int) error {
	if len(data) > max {
		return errFrame
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(data)))
	if err := writeAll(w, h[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, e := w.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
func readFrame(r io.Reader, max int) ([]byte, error) {
	var h [4]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return nil, e
	}
	size := binary.BigEndian.Uint32(h[:])
	if uint64(size) > uint64(max) {
		return nil, errFrame
	}
	b := make([]byte, int(size))
	_, e := io.ReadFull(r, b)
	return b, e
}
func writeJSON(w io.Writer, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return writeFrame(w, b, maxControlFrame)
}
func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errFrame
	}
	if d.Decode(new(any)) != io.EOF {
		return errFrame
	}
	canonical, e := json.Marshal(v)
	if e != nil || !bytes.Equal(b, canonical) {
		return errFrame
	}
	return nil
}
func readJSON(r io.Reader, v any) error {
	b, e := readFrame(r, maxControlFrame)
	if e != nil {
		return e
	}
	return strictJSON(b, v)
}
