package samproc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/guoyyit/sam-record-processor-go/internal/sam"
)

// MarkDuplicates reads a coordinate-sorted SAM input, marks duplicate
// records and writes the result to output.
//
// Rules:
//   - Unmapped, secondary and supplementary records are never candidates
//     and are passed through unchanged.
//   - Paired records with a mapped mate are grouped by the two fragment
//     ends: (RNAME,POS,orientation) of each mate.
//   - Other records are grouped by their single (RNAME,POS,orientation).
//   - Within each candidate group the record with the greatest sum of
//     base qualities is the representative and keeps the flag clear;
//     every other record gets FLAG 0x400. Equal quality sums are broken
//     by the raw line bytes, so the choice is deterministic.
func MarkDuplicates(ctx context.Context, cfg Config, input, output string) error {
	if err := cfg.normalize(); err != nil {
		return err
	}
	cr, err := openChunk(input)
	if err != nil {
		return fmt.Errorf("markdup: open %s: %w", input, err)
	}
	defer cr.Close()

	tmp := output + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(out, 1<<16)
	if _, err := bw.Write(cr.Header().Raw()); err != nil {
		return err
	}

	d := newDeduper(bw)
	for {
		if err := ctx.Err(); err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return err
		}
		rec, err := cr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("markdup: %s: %w", input, err)
		}
		if err := d.add(rec); err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := d.finish(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, output)
}

// queued is one record in arrival order; nodes form a linked list so
// group membership stays valid as earlier records are emitted.
type queued struct {
	rec  *sam.Record
	key  string
	done bool
	next *queued
}

type dupGroup struct {
	members []*queued
	maxRef  string
	maxPos  int

	seenEnds map[[2]string]int
}

type deduper struct {
	bw      *bufio.Writer
	head    *queued
	tail    *queued
	groups  map[string]*dupGroup
	lineBuf []byte
}

func newDeduper(bw *bufio.Writer) *deduper {
	return &deduper{bw: bw, groups: map[string]*dupGroup{}}
}

func participates(r *sam.Record) bool {
	return !r.Flag.Unmapped() && !r.Flag.Secondary() && !r.Flag.Supplementary()
}

func (d *deduper) append(q *queued) {
	if d.tail == nil {
		d.head, d.tail = q, q
		return
	}
	d.tail.next = q
	d.tail = q
}

func (d *deduper) add(r *sam.Record) error {
	if !participates(r) {
		if usablePosition(r) {
			if err := d.advanceBefore(r.RName, r.Pos); err != nil {
				return err
			}
		}
		d.append(&queued{rec: r, done: true})
		return d.drain()
	}

	key, maxRef, maxPos := dupKey(r)
	q := &queued{rec: r, key: key}
	d.append(q)
	g := d.groups[key]
	if g == nil {
		g = &dupGroup{maxRef: maxRef, maxPos: maxPos}
		d.groups[key] = g
	}
	g.members = append(g.members, q)
	if maxRef > g.maxRef || (maxRef == g.maxRef && maxPos > g.maxPos) {
		g.maxRef, g.maxPos = maxRef, maxPos
	}
	if g.seenEnds == nil {
		g.seenEnds = map[[2]string]int{}
	}
	// Count member records by their own coordinate; paired members
	// additionally promise a mate record at the other fragment end.
	g.seenEnds[endKey(r.RName, r.Pos)]++

	if usablePosition(r) {
		if err := d.advanceBefore(r.RName, r.Pos); err != nil {
			return err
		}
	}
	return nil
}

// advanceBefore finalizes groups whose furthest end is strictly before
// the sweep coordinate, then emits the released prefix of the list.
func (d *deduper) advanceBefore(ref string, pos int) error {
	for key, g := range d.groups {
		swept := g.maxRef < ref || (g.maxRef == ref && g.maxPos < pos)
		if swept && g.allEndsSeen() {
			d.finalizeGroup(g)
			delete(d.groups, key)
		}
	}
	return d.drain()
}

// allEndsSeen reports whether every promised mate end is backed by a
// member record already in the group. Each member always covers its own
// end; the only not-yet-seen end can be the mate of an earlier record.
func (g *dupGroup) allEndsSeen() bool {
	for mRef, positions := range g.pendingMateEnds() {
		for _, mPos := range positions {
			if g.seenEnds[endKey(mRef, mPos)] == 0 {
				return false
			}
		}
	}
	return true
}

// pendingMateEnds returns the set of distinct promised mate ends.
func (g *dupGroup) pendingMateEnds() map[string][]int {
	out := map[string][]int{}
	seen := map[[2]string]bool{}
	for _, mq := range g.members {
		mRef, mPos, ok := effectiveMate(mq.rec)
		if !ok {
			continue
		}
		k := endKey(mRef, mPos)
		if seen[k] {
			continue
		}
		seen[k] = true
		out[mRef] = append(out[mRef], mPos)
	}
	return out
}

func endKey(ref string, pos int) [2]string {
	return [2]string{ref, fmt.Sprintf("%d", pos)}
}

// effectiveMate returns the mate coordinate when a mapped mate exists.
func effectiveMate(r *sam.Record) (string, int, bool) {
	if !r.Flag.Paired() || r.Flag.MateUnmapped() || r.RNext == "*" || r.PNext <= 0 {
		return "", 0, false
	}
	mRef := r.RNext
	if mRef == "=" {
		mRef = r.RName
	}
	return mRef, r.PNext, true
}

func (d *deduper) finalizeGroup(g *dupGroup) {
	// Group members into fragments. A paired candidate fragment is the
	// two records sharing one QNAME; a single-end fragment is one
	// record. Selection compares the fragment's total base quality.
	type fragment struct {
		qname string
		recs  []*queued
		score int64
		tie   []byte // deterministic tie-break bytes
	}
	byQ := map[string]*fragment{}
	var order []*fragment
	for _, q := range g.members {
		f := byQ[q.rec.QName]
		if f == nil {
			f = &fragment{qname: q.rec.QName}
			byQ[q.rec.QName] = f
			order = append(order, f)
		}
		f.recs = append(f.recs, q)
		f.score += qualitySum(q.rec)
		f.tie = append(f.tie, q.rec.Raw...)
	}

	if len(order) < 2 {
		for _, q := range g.members {
			q.done = true
		}
		return
	}
	best := order[0]
	for _, f := range order[1:] {
		if f.score > best.score ||
			(f.score == best.score && bytesCompare(f.tie, best.tie) < 0) {
			best = f
		}
	}
	for _, f := range order {
		isBest := f == best
		for _, q := range f.recs {
			q.done = true
			if isBest {
				q.rec.Flag &^= sam.FlagDuplicate
			} else {
				q.rec.Flag |= sam.FlagDuplicate
			}
		}
	}
}

func (d *deduper) drain() error {
	for d.head != nil && d.head.done {
		q := d.head
		d.head = q.next
		if d.head == nil {
			d.tail = nil
		}
		q.next = nil
		if err := d.emit(q.rec); err != nil {
			return err
		}
	}
	return nil
}

func (d *deduper) finish() error {
	for key, g := range d.groups {
		d.finalizeGroup(g)
		delete(d.groups, key)
	}
	return d.drain()
}

func (d *deduper) emit(r *sam.Record) error {
	d.lineBuf = r.Append(d.lineBuf[:0])
	d.lineBuf = append(d.lineBuf, '\n')
	_, err := d.bw.Write(d.lineBuf)
	return err
}

// qualitySum sums Phred33 base qualities; '*' means unavailable and
// contributes zero.
func qualitySum(r *sam.Record) int64 {
	if r.Qual == "*" {
		return 0
	}
	var sum int64
	for _, b := range []byte(r.Qual) {
		sum += int64(b - 33)
	}
	return sum
}

// dupKey returns the duplicate candidate key and the furthest fragment
// coordinate used to know when the group is complete.
func dupKey(r *sam.Record) (key, maxRef string, maxPos int) {
	end := func(ref string, pos int, rev bool) string {
		o := byte('F')
		if rev {
			o = 'R'
		}
		return fmt.Sprintf("%s\x00%d\x00%c", ref, pos, o)
	}
	own := end(r.RName, r.Pos, r.Flag.Reverse())
	if r.Flag.Paired() && !r.Flag.MateUnmapped() && r.RNext != "*" && r.PNext > 0 {
		mateRef := r.RNext
		if mateRef == "=" {
			mateRef = r.RName
		}
		mate := end(mateRef, r.PNext, r.Flag.MateReverse())
		a, b := own, mate
		if b < a {
			a, b = b, a
		}
		key = "2\x00" + a + "\x00|\x00" + b
		maxRef, maxPos = mateRef, r.PNext
		if maxRef < r.RName || (maxRef == r.RName && maxPos < r.Pos) {
			maxRef, maxPos = r.RName, r.Pos
		}
		return key, maxRef, maxPos
	}
	return "1\x00" + own, r.RName, r.Pos
}

func bytesCompare(a, b []byte) int {
	la, lb := len(a), len(b)
	n := la
	if lb < n {
		n = lb
	}
	for i := 0; i < n; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	switch {
	case la < lb:
		return -1
	case la > lb:
		return 1
	default:
		return 0
	}
}
