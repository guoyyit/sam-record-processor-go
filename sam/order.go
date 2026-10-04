package sam

// Compare orders records by (reference name lexicographic, 1-based start
// coordinate). Records without a usable coordinate sort after all placed
// records. Ties are broken by the full record line so the order is total
// and deterministic.
func Compare(a, b *Record) int {
	ap, bp := a.Placed(), b.Placed()
	if ap != bp {
		if ap {
			return -1
		}
		return 1
	}
	if ap {
		if a.RName != b.RName {
			if a.RName < b.RName {
				return -1
			}
			return 1
		}
		if a.Pos != b.Pos {
			if a.Pos < b.Pos {
				return -1
			}
			return 1
		}
	}
	return compareString(a.Line(), b.Line())
}

// Less reports whether a sorts before b under Compare.
func Less(a, b *Record) bool { return Compare(a, b) < 0 }

func compareString(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
