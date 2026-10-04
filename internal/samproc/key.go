package samproc

import (
	"bytes"

	"github.com/guoyyit/sam-record-processor-go/internal/sam"
)

// usablePosition reports whether the record carries a usable coordinate
// per SAM: RNAME != '*' and POS > 0 (unmapped reads may legitimately
// carry a coordinate).
func usablePosition(r *sam.Record) bool {
	return r.RName != "*" && r.Pos > 0
}

// Compare is the total deterministic ordering for alignment records.
//
// Primary key: records with a usable coordinate come first, ordered by
// RNAME (lexicographic) then POS; records without a usable coordinate
// sort last. Ties fall back to a lexicographic comparison of the
// serialized line, so equal inputs always produce byte-identical output.
func Compare(a, b *sam.Record) int {
	au, bu := usablePosition(a), usablePosition(b)
	switch {
	case au && !bu:
		return -1
	case !au && bu:
		return 1
	case au && bu:
		if c := compareString(a.RName, b.RName); c != 0 {
			return c
		}
		if c := compareInt(a.Pos, b.Pos); c != 0 {
			return c
		}
	}
	return bytes.Compare(a.Raw, b.Raw)
}

func compareString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
