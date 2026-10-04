package sam

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// RefSeq is one @SQ reference sequence entry.
type RefSeq struct {
	Name   string
	Length int
}

// Header holds the raw header text and the reference dictionary.
type Header struct {
	lines [][]byte // verbatim header lines, without trailing newline
	raw   []byte   // verbatim header bytes including newlines
	refs  []RefSeq
	index map[string]int
}

const maxHeaderLine = 1 << 20

// ParseHeader consumes all leading '@' header lines from r. Header syntax
// is validated and @SQ entries populate the reference dictionary. Bytes
// belonging to the first alignment line are left buffered in r.
func ParseHeader(r *bufio.Reader) (*Header, error) {
	h := &Header{index: map[string]int{}}
	for {
		first, err := r.Peek(1)
		if err != nil || len(first) == 0 {
			return h, nil
		}
		if first[0] != '@' {
			return h, nil
		}
		line, readErr := r.ReadSlice('\n')
		if readErr == bufio.ErrBufferFull {
			return nil, fmt.Errorf("header line longer than reader buffer")
		}
		if readErr != nil && len(line) == 0 {
			return h, nil
		}
		trimmed := bytes.TrimSuffix(line, []byte("\n"))
		trimmed = bytes.TrimSuffix(trimmed, []byte("\r"))
		if len(trimmed) == 0 {
			return nil, fmt.Errorf("malformed header: empty header line")
		}
		if len(trimmed) > maxHeaderLine {
			return nil, fmt.Errorf("header line too long (%d bytes)", len(trimmed))
		}
		if err := h.parseLine(trimmed); err != nil {
			return nil, err
		}
		h.lines = append(h.lines, append([]byte(nil), trimmed...))
		h.raw = append(h.raw, line...)
	}
}

func (h *Header) parseLine(line []byte) error {
	fields := strings.Split(string(line), "\t")
	if len(fields) < 2 {
		return fmt.Errorf("malformed header line %q: expected tab-separated fields", line)
	}
	record := fields[0]
	if len(record) < 3 || record[0] != '@' {
		return fmt.Errorf("malformed header line %q: record type must start with @", line)
	}
	for _, f := range fields[1:] {
		if len(f) < 3 || f[2] != ':' {
			return fmt.Errorf("malformed header line %q: invalid field %q", line, f)
		}
	}
	if record != "@SQ" {
		return nil
	}
	var ref RefSeq
	haveName, haveLen := false, false
	for _, f := range fields[1:] {
		tag, value := f[:2], f[3:]
		switch string(tag) {
		case "SN":
			if value == "" {
				return fmt.Errorf("@SQ entry has empty SN: %q", line)
			}
			ref.Name = value
			haveName = true
		case "LN":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return fmt.Errorf("@SQ entry %q has invalid LN %q", line, value)
			}
			ref.Length = n
			haveLen = true
		}
	}
	if !haveName || !haveLen {
		return fmt.Errorf("@SQ entry %q must contain SN and LN", line)
	}
	if _, dup := h.index[ref.Name]; dup {
		return fmt.Errorf("duplicate @SQ SN %q", ref.Name)
	}
	h.index[ref.Name] = len(h.refs)
	h.refs = append(h.refs, ref)
	return nil
}

// Refs returns the declared references in declaration order.
func (h *Header) Refs() []RefSeq {
	out := make([]RefSeq, len(h.refs))
	copy(out, h.refs)
	return out
}

// Lookup finds a declared reference by name.
func (h *Header) Lookup(name string) (RefSeq, bool) {
	if i, ok := h.index[name]; ok {
		return h.refs[i], true
	}
	return RefSeq{}, false
}

// Raw returns the verbatim header bytes.
func (h *Header) Raw() []byte {
	out := make([]byte, len(h.raw))
	copy(out, h.raw)
	return out
}

// Lines returns verbatim header lines without newlines.
func (h *Header) Lines() [][]byte {
	out := make([][]byte, len(h.lines))
	for i, l := range h.lines {
		out[i] = append([]byte(nil), l...)
	}
	return out
}
