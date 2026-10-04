package sam

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

const testHeader = "@HD\tVN:1.6\tSO:unknown\n@SQ\tSN:ref\tLN:1000\n@SQ\tSN:chr2\tLN:500\n"

func parseHeader(t *testing.T, text string) *Header {
	t.Helper()
	h, err := ParseHeader(bufio.NewReader(strings.NewReader(text)))
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	return h
}

func TestParseValidRecord(t *testing.T) {
	h := parseHeader(t, testHeader)
	line := []byte("r1\t99\tref\t10\t60\t5M2D3M\t=\t20\t18\tACGTACGT\tIIIIIIII\n")
	line = bytes.TrimSuffix(line, []byte("\n"))
	r, err := ParseRecord(line, 4)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if err := r.Validate(h); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := r.Cigar.ReferenceLength(); got != 10 {
		t.Fatalf("ReferenceLength = %d, want 10", got)
	}
	if got := r.Cigar.ReadLength(); got != 8 {
		t.Fatalf("ReadLength = %d, want 8", got)
	}
	if !r.Flag.MateReverse() || r.Flag.Reverse() || !r.Flag.Read1() || !r.Flag.Paired() {
		t.Fatalf("flag bits wrong: %v", r.Flag.Bits())
	}
	if got := string(r.Bytes()); got != string(line) {
		t.Fatalf("round trip mismatch:\n got %q\nwant %q", got, line)
	}
}

func TestParseErrors(t *testing.T) {
	h := parseHeader(t, testHeader)
	bad := []struct {
		name string
		line string
	}{
		{"too few fields", "r1\t0\n"},
		{"bad flag", "r1\tXX\tref\t10\t60\t5M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"bad pos", "r1\t0\tref\t-1\t60\t5M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"bad mapq", "r1\t0\tref\t10\t999\t5M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"bad cigar op", "r1\t0\tref\t10\t60\t5Q\t*\t0\t0\tACGTA\tIIIII\n"},
		{"seq qual mismatch", "r1\t0\tref\t10\t60\t5M\t*\t0\t0\tACGTA\tIIII\n"},
		{"undeclared ref", "r1\t0\tnope\t10\t60\t5M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"unmapped rname", "r1\t4\tref\t10\t60\t5M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"unmapped cigar", "r1\t4\t*\t0\t60\t5M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"proper pair unpaired", "r1\t2\tref\t10\t60\t5M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"read1 read2", "r1\t192\tref\t10\t60\t5M\t=\t20\t0\tACGTA\tIIIII\n"},
		{"cigar seq mismatch", "r1\t0\tref\t10\t60\t6M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"beyond reference", "r1\t0\tref\t998\t60\t5M\t*\t0\t0\tACGTA\tIIIII\n"},
		{"undeclared mate ref", "r1\t1\tref\t10\t60\t5M\tghost\t20\t0\tACGTA\tIIIII\n"},
		{"bad tag", "r1\t0\tref\t10\t60\t5M\t*\t0\t0\tACGTA\tIIIII\tXX:nope\n"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			r, perr := ParseRecord([]byte(tc.line), 7)
			if perr == nil {
				if verr := r.Validate(h); verr == nil {
					t.Fatalf("expected error for %q, got none", tc.line)
				}
			}
		})
	}
}

func TestParseHeaderPushback(t *testing.T) {
	text := testHeader + "r1\t4\t*\t0\t0\t*\t*\t0\t0\t*\t*\n"
	r := bufio.NewReader(strings.NewReader(text))
	if _, err := ParseHeader(r); err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	line, err := r.ReadSlice('\n')
	if err != nil {
		t.Fatalf("ReadSlice: %v", err)
	}
	if !bytes.HasPrefix(line, []byte("r1")) {
		t.Fatalf("pushback lost first record: %q", line)
	}
}

func TestUnmappedRecord(t *testing.T) {
	h := parseHeader(t, testHeader)
	line := "r1\t77\t*\t0\t0\t*\t*\t0\t0\tACGTA\tIIIII"
	r, err := ParseRecord([]byte(line), 1)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if err := r.Validate(h); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if string(r.Bytes()) != line {
		t.Fatalf("round trip mismatch: %q", r.Bytes())
	}
}

func TestUnmappedWithCoordinate(t *testing.T) {
	// SAMv1 permits an unmapped read to carry RNAME/POS for placement;
	// it is still UNMAP and CIGAR '*'.
	h := parseHeader(t, testHeader)
	line := "r1\t4\tref\t10\t0\t*\t*\t0\t0\t*\t*"
	r, err := ParseRecord([]byte(line), 1)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if err := r.Validate(h); err != nil {
		t.Fatalf("unmapped record with coordinate should validate: %v", err)
	}
}

func TestUnpairedMateFieldsRejected(t *testing.T) {
	h := parseHeader(t, testHeader)
	line := "r1\t0\tref\t10\t60\t5M\tref\t20\t0\tACGTA\tIIIII"
	r, err := ParseRecord([]byte(line), 1)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if err := r.Validate(h); err == nil {
		t.Fatal("unpaired record with RNEXT/PNEXT must be rejected")
	}
}

func TestDuplicateFlagRoundTrip(t *testing.T) {
	r, err := ParseRecord([]byte("r1\t1024\tref\t10\t60\t5M\t*\t0\t0\tACGTA\tIIIII"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Flag.Duplicate() {
		t.Fatal("duplicate bit not parsed")
	}
	r.Flag |= FlagReverse
	if string(r.Bytes()) != "r1\t1040\tref\t10\t60\t5M\t*\t0\t0\tACGTA\tIIIII" {
		t.Fatalf("flag rewrite wrong: %s", r.Bytes())
	}
}
