package sam

import (
	"io"
	"strings"
)

// Line returns the SAM text for the record. Unmodified records return the
// original input line byte-for-byte; modified records are re-serialized.
func (r *Record) Line() string {
	if !r.dirty {
		return r.raw
	}
	fields := make([]string, 0, 11+len(r.Aux))
	seq, qual := r.Seq, r.Qual
	if seq == "" {
		seq = "*"
	}
	if qual == "" {
		qual = "*"
	}
	fields = append(fields,
		r.Name,
		itoa(int(r.Flag)),
		r.RName,
		itoa(r.Pos),
		itoa(r.MapQ),
		r.CigarString(),
		r.RNext,
		itoa(r.PNext),
		itoa(r.TLen),
		seq,
		qual,
	)
	fields = append(fields, r.Aux...)
	return strings.Join(fields, "\t")
}

// CigarString returns the CIGAR string ("*" when absent).
func (r *Record) CigarString() string {
	if r.Cigar == nil {
		return "*"
	}
	var b strings.Builder
	for _, op := range r.Cigar {
		b.WriteString(itoa(op.Len))
		b.WriteByte(op.Op)
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// WriteHeader writes all header lines to w.
func WriteHeader(w io.Writer, h *Header) error {
	for _, ln := range h.Lines {
		if _, err := io.WriteString(w, ln+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// MergeHeaders combines several headers into one: a fresh @HD line with
// coordinate sort order, followed by the union of all remaining lines in
// first-seen order (exact duplicates removed).
func MergeHeaders(hs ...*Header) *Header {
	out := &Header{RefSet: map[string]bool{}}
	out.Lines = append(out.Lines, "@HD\tVN:1.6\tSO:coordinate")
	seen := map[string]bool{out.Lines[0]: true}
	for _, h := range hs {
		if h == nil {
			continue
		}
		for _, ln := range h.Lines {
			if strings.HasPrefix(ln, "@HD\t") || ln == "@HD" {
				continue
			}
			if seen[ln] {
				continue
			}
			seen[ln] = true
			out.Lines = append(out.Lines, ln)
		}
		for _, ref := range h.Refs {
			if !out.RefSet[ref] {
				out.RefSet[ref] = true
				out.Refs = append(out.Refs, ref)
			}
		}
	}
	return out
}
