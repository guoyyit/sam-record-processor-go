package sam

import (
	"bufio"
	"fmt"
	"io"
	"os"
)

// DedupFile marks duplicate records in a coordinate-sorted SAM file and
// writes the result to outPath. Unmapped, secondary and supplementary
// records are neither considered nor marked. Among a duplicate set the
// record with the highest base-quality sum is kept as representative;
// ties are broken deterministically. All other members get FlagDuplicate.
//
// Duplicate keys: for paired records with a mapped mate, both ends'
// start coordinates and orientations; otherwise the record's own start
// coordinate and orientation.
func DedupFile(inPath, outPath string) error {
	if inPath == outPath {
		return fmt.Errorf("dedup: input and output paths must differ")
	}
	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()
	rd := NewReader(f)
	header, err := rd.ReadHeader()
	if err != nil {
		return err
	}

	tmp := outPath + ".tmp"
	os.Remove(tmp)
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(out, 1<<20)
	if err := WriteHeader(bw, header); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}

	var group []*Record
	flush := func() error {
		if len(group) == 0 {
			return nil
		}
		markGroup(group)
		for _, rec := range group {
			if _, err := bw.WriteString(rec.Line() + "\n"); err != nil {
				return err
			}
		}
		group = group[:0]
		return nil
	}

	for {
		rec, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		if len(group) > 0 && sameCoord(group[0], rec) {
			group = append(group, rec)
			continue
		}
		if err := flush(); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		group = append(group, rec)
	}
	if err := flush(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := bw.Flush(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, outPath)
}

// sameCoord groups records sharing a sort coordinate. Unplaced records
// each form their own group (they are never candidates anyway).
func sameCoord(a, b *Record) bool {
	if !a.Placed() || !b.Placed() {
		return false
	}
	return a.RName == b.RName && a.Pos == b.Pos
}

// dupKey identifies a duplicate set within one coordinate group.
type dupKey struct {
	rev     bool
	paired  bool // mate mapped: both ends participate
	mateRef string
	matePos int
	mateRev bool
}

// markGroup flags duplicates within one coordinate group.
func markGroup(group []*Record) {
	buckets := map[dupKey][]*Record{}
	var keys []dupKey
	for _, rec := range group {
		if !dupCandidate(rec) {
			continue
		}
		k := keyFor(rec)
		if _, ok := buckets[k]; !ok {
			keys = append(keys, k)
		}
		buckets[k] = append(buckets[k], rec)
	}
	for _, k := range keys {
		b := buckets[k]
		if len(b) < 2 {
			continue
		}
		rep := b[0]
		for _, r := range b[1:] {
			if better(r, rep) {
				rep = r
			}
		}
		for _, r := range b {
			if r != rep {
				r.SetFlag(FlagDuplicate)
			}
		}
	}
}

// dupCandidate reports whether a record participates in duplicate marking.
func dupCandidate(rec *Record) bool {
	const excluded = FlagUnmapped | FlagSecondary | FlagSupplementary
	return rec.Flag&excluded == 0 && rec.Placed()
}

// keyFor derives the duplicate key of a candidate record.
func keyFor(rec *Record) dupKey {
	k := dupKey{rev: rec.Flag&FlagReverse != 0}
	if rec.Flag&FlagPaired != 0 && rec.Flag&FlagMateUnmapped == 0 {
		k.paired = true
		k.mateRef = rec.RNext
		if k.mateRef == "=" {
			k.mateRef = rec.RName
		}
		k.matePos = rec.PNext
		k.mateRev = rec.Flag&FlagMateReverse != 0
	}
	return k
}

// better reports whether a is a better representative than b: higher
// base-quality sum, then smaller name, then smaller line, so the choice
// is deterministic for identical inputs in any order.
func better(a, b *Record) bool {
	qa, qb := a.QualSum(), b.QualSum()
	if qa != qb {
		return qa > qb
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.Line() < b.Line()
}
