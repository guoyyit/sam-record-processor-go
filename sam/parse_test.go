package sam

import (
	"io"
	"strings"
	"testing"
)

const testHeader = "@HD\tVN:1.6\tSO:unsorted\n@SQ\tSN:chr1\tLN:1000\n@SQ\tSN:chr2\tLN:2000\n"

func parseAll(t *testing.T, input string) (*Header, []*Record) {
	t.Helper()
	rd := NewReader(strings.NewReader(input))
	var recs []*Record
	for {
		rec, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		recs = append(recs, rec)
	}
	return rd.Header(), recs
}

func TestParseValidRecords(t *testing.T) {
	in := testHeader +
		"r1\t99\tchr1\t10\t60\t8M\t=\t50\t100\tACGTACGT\tIIIIIIII\tNM:i:1\n" +
		"r2\t4\t*\t0\t0\t*\t*\t0\t0\tNNNN\t####\n" +
		"r3\t0\tchr2\t7\t20\t2S6M1I3M2D4M\t*\t0\t0\tACGTACGTACGTACGT\tIIIIIIIIIIIIIIII\n"
	h, recs := parseAll(t, in)
	if len(h.Refs) != 2 || !h.HasRef("chr1") || !h.HasRef("chr2") {
		t.Fatalf("header refs: %v", h.Refs)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.Name != "r1" || r.Flag != 99 || r.RName != "chr1" || r.Pos != 10 ||
		r.MapQ != 60 || r.RNext != "=" || r.PNext != 50 || r.TLen != 100 {
		t.Fatalf("record fields: %+v", r)
	}
	if r.Seq != "ACGTACGT" || r.Qual != "IIIIIIII" {
		t.Fatalf("seq/qual: %q %q", r.Seq, r.Qual)
	}
	if len(r.Aux) != 1 || r.Aux[0] != "NM:i:1" {
		t.Fatalf("aux: %v", r.Aux)
	}
	if r.Line() != "r1\t99\tchr1\t10\t60\t8M\t=\t50\t100\tACGTACGT\tIIIIIIII\tNM:i:1" {
		t.Fatalf("round trip: %q", r.Line())
	}
	r3 := recs[2]
	if got := r3.CigarString(); got != "2S6M1I3M2D4M" {
		t.Fatalf("cigar: %q", got)
	}
	if r3.QueryLen() != 16 || r3.RefLen() != 15 {
		t.Fatalf("lengths: query %d ref %d", r3.QueryLen(), r3.RefLen())
	}
	if r3.End() != 21 {
		t.Fatalf("end: %d", r3.End())
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"few fields", testHeader + "r1\t99\tchr1\n", "11"},
		{"undeclared ref", testHeader + "r1\t0\tchrX\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n", "not declared"},
		{"undeclared mate ref", testHeader + "r1\t1\tchr1\t10\t60\t4M\tchrY\t5\t0\tACGT\tIIII\n", "not declared"},
		{"bad flag", testHeader + "r1\t99999\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n", "FLAG"},
		{"proper without paired", testHeader + "r1\t2\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n", "0x2"},
		{"mate unmapped without paired", testHeader + "r1\t8\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n", "0x8"},
		{"mate reverse without paired", testHeader + "r1\t32\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n", "0x20"},
		{"read1 without paired", testHeader + "r1\t64\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n", "0x1"},
		{"read1 and read2", testHeader + "r1\t193\tchr1\t10\t60\t4M\t=\t20\t0\tACGT\tIIII\n", "0x40"},
		{"mapped without ref", testHeader + "r1\t0\t*\t0\t0\t*\t*\t0\t0\tACGT\tIIII\n", "0x4"},
		{"mapped pos zero", testHeader + "r1\t0\tchr1\t0\t60\t4M\t*\t0\t0\tACGT\tIIII\n", "POS"},
		{"mapped no cigar", testHeader + "r1\t0\tchr1\t10\t60\t*\t*\t0\t0\tACGT\tIIII\n", "CIGAR"},
		{"pos without ref", testHeader + "r1\t4\t*\t5\t0\t*\t*\t0\t0\tACGT\tIIII\n", "POS"},
		{"qual length mismatch", testHeader + "r1\t0\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIII\n", "QUAL"},
		{"cigar seq mismatch", testHeader + "r1\t0\tchr1\t10\t60\t5M\t*\t0\t0\tACGT\tIIII\n", "CIGAR"},
		{"bad cigar char", testHeader + "r1\t0\tchr1\t10\t60\t4Z\t*\t0\t0\tACGT\tIIII\n", "CIGAR"},
		{"zero length cigar op", testHeader + "r1\t0\tchr1\t10\t60\t0M4M\t*\t0\t0\tACGT\tIIII\n", "CIGAR"},
		{"bad mapq", testHeader + "r1\t0\tchr1\t10\t300\t4M\t*\t0\t0\tACGT\tIIII\n", "MAPQ"},
		{"bad seq char", testHeader + "r1\t0\tchr1\t10\t60\t4M\t*\t0\t0\tACG1\tIIII\n", "SEQ"},
		{"bad aux", testHeader + "r1\t0\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\tNMx:i:1\n", "aux"},
		{"empty qname", testHeader + "\t0\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n", "QNAME"},
		{"duplicate SQ", "@SQ\tSN:chr1\tLN:10\n@SQ\tSN:chr1\tLN:20\n", "duplicate"},
		{"SQ missing SN", "@SQ\tLN:10\n", "SN"},
		{"unknown header type", "@XX\tVN:1\n", "unknown"},
		{"header after records", testHeader + "r1\t4\t*\t0\t0\t*\t*\t0\t0\t*\t*\n@SQ\tSN:chr9\tLN:1\n", "header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rd := NewReader(strings.NewReader(tc.input))
			var err error
			for {
				_, err = rd.Next()
				if err != nil {
					break
				}
			}
			if err == io.EOF {
				t.Fatalf("expected error containing %q, got EOF", tc.want)
			}
			pe, ok := err.(*ParseError)
			if !ok {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}
			if pe.Line <= 0 {
				t.Fatalf("error missing line number: %v", pe)
			}
			if !strings.Contains(pe.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", pe.Error(), tc.want)
			}
		})
	}
}

func TestParseErrorLineNumbers(t *testing.T) {
	in := testHeader +
		"ok\t0\tchr1\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n" +
		"bad\t0\tchr9\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n"
	rd := NewReader(strings.NewReader(in))
	if _, err := rd.Next(); err != nil {
		t.Fatalf("first record: %v", err)
	}
	_, err := rd.Next()
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %v", err)
	}
	if pe.Line != 5 {
		t.Fatalf("want line 5, got %d (%v)", pe.Line, pe)
	}
}

func TestParseNoTrailingNewline(t *testing.T) {
	in := testHeader + "r1\t4\t*\t0\t0\t*\t*\t0\t0\t*\t*"
	_, recs := parseAll(t, in)
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
}
