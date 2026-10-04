package sam

import "strconv"

// Append renders the record as one SAM line (without trailing newline)
// into b. Field values are reconstructed from the parsed fields.
func (r *Record) Append(b []byte) []byte {
	b = append(b, r.QName...)
	b = append(b, '\t')
	b = strconv.AppendInt(b, int64(r.Flag), 10)
	b = append(b, '\t')
	b = append(b, r.RName...)
	b = append(b, '\t')
	b = strconv.AppendInt(b, int64(r.Pos), 10)
	b = append(b, '\t')
	b = strconv.AppendInt(b, int64(r.MapQ), 10)
	b = append(b, '\t')
	b = append(b, r.Cigar.String()...)
	b = append(b, '\t')
	b = append(b, r.RNext...)
	b = append(b, '\t')
	b = strconv.AppendInt(b, int64(r.PNext), 10)
	b = append(b, '\t')
	b = strconv.AppendInt(b, int64(r.TLEN), 10)
	b = append(b, '\t')
	b = append(b, r.Seq...)
	b = append(b, '\t')
	b = append(b, r.Qual...)
	if r.Tags != "" {
		b = append(b, '\t')
		b = append(b, r.Tags...)
	}
	return b
}

// Bytes returns the rendered SAM line without a trailing newline.
func (r *Record) Bytes() []byte {
	return r.Append(nil)
}
