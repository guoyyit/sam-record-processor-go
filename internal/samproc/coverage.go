package samproc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/guoyyit/sam-record-processor-go/internal/sam"
)

// Base is an observed query base; it JSON-marshals as a one-letter
// string rather than a numeric byte.
type Base byte

// MarshalJSON renders the base as a JSON string.
func (b Base) MarshalJSON() ([]byte, error) { return json.Marshal(string(b)) }

// Phred is a Phred33 base quality; 255 marks unavailable quality.
type Phred byte

// MarshalJSON renders the numeric Phred score.
func (q Phred) MarshalJSON() ([]byte, error) { return json.Marshal(int(q)) }

// Observation is one observed base at one reference position.
type Observation struct {
	Base    Base
	Qual    Phred // Phred33 integer; 255 means unavailable ("*")
	Reverse bool
}

// Position is the coverage view of one reference position.
type Position struct {
	Ref           string
	Pos           int
	Depth         int
	Covered       bool // true iff at least one record covers this site
	Observations  []Observation
	InsertedBases []Observation // I bases inserted immediately after Pos
}

// Coverage streams a per-position coverage view for the inclusive
// interval [start,end] on ref from a coordinate-sorted SAM input.
//
// Memory is bounded by the number of records overlapping one sweep
// position, independent of interval length and input size. Every site
// in the interval is emitted; if no record covers a site, Covered is
// false, distinguishing "zero depth here" from "no record in interval".
func Coverage(ctx context.Context, cfg Config, input, ref string, start, end int, emit func(Position) error) error {
	if err := cfg.normalize(); err != nil {
		return err
	}
	if start < 1 || end < start {
		return fmt.Errorf("coverage: invalid interval [%d,%d]", start, end)
	}
	cr, err := openChunk(input)
	if err != nil {
		return fmt.Errorf("coverage: open %s: %w", input, err)
	}
	defer cr.Close()
	refSeq, ok := cr.Header().Lookup(ref)
	if !ok {
		return fmt.Errorf("coverage: reference %q is not declared in @SQ header", ref)
	}
	if end > refSeq.Length {
		return fmt.Errorf("coverage: interval end %d exceeds LN %d of %q", end, refSeq.Length, ref)
	}

	active := &activeSet{}
	next, err := cr.Next()
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("coverage: %s: %w", input, err)
	}

	for p := start; p <= end; p++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for next != nil {
			if !usablePosition(next) {
				// trailing unplaced records
			} else if next.RName == ref && next.Pos <= p {
				active.add(next)
			} else if next.RName < ref || (next.RName == ref && next.Pos <= p) {
				// record on an earlier reference: skip
			} else {
				break
			}
			r, nerr := cr.Next()
			if nerr != nil && !errors.Is(nerr, io.EOF) {
				return fmt.Errorf("coverage: %s: %w", input, nerr)
			}
			next = r
		}
		active.dropBefore(p)

		pos := Position{Ref: ref, Pos: p}
		for _, a := range active.records {
			if a.cur.excluded(cfg) {
				continue
			}
			a.cur.advanceTo(p)
			if a.cur.state == stateAligned {
				pos.Covered = true
				if o, present := a.cur.alignedBase(); present {
					pos.Observations = append(pos.Observations, o)
				}
				pos.InsertedBases = append(pos.InsertedBases, a.cur.insertionsHere()...)
			}
		}
		pos.Depth = len(pos.Observations)
		if err := emit(pos); err != nil {
			return err
		}
	}
	return nil
}

// Cursor states for the current reference position.
const (
	stateBefore  = iota // not yet reached the queried position
	stateAligned        // parked on an M/=/X/D element at the position
	stateSkipped        // parked on an N element at the position
	stateAfter          // CIGAR exhausted
)

// cursor walks one record's CIGAR in reference-coordinate order. The
// parked element is always a reference-consuming operation; non-ref
// operations (I, S, H, P) are consumed during advances or lookahead.
type cursor struct {
	rec *sam.Record

	opIndex int // index of parked op
	opLeft  int // remaining repetitions including the parked element
	refPos  int // reference coordinate of the parked element
	readPos int // query index of the parked element
	state   int

	inserted []Observation // I bases following the parked ref element
}

func newCursor(r *sam.Record) *cursor {
	c := &cursor{rec: r, refPos: r.Pos, readPos: 0, state: stateBefore}
	c.skipPrelude(0)
	return c
}

// skipPrelude moves opIndex to the first reference-consuming op at or
// after the current index while advancing readPos over H/S/P ops.
func (c *cursor) skipPrelude(startRead int) {
	r := c.rec
	c.readPos = startRead
	for c.opIndex < len(r.Cigar) {
		t := r.Cigar[c.opIndex].Type
		if t == 'S' {
			c.readPos += r.Cigar[c.opIndex].Len
		}
		if isRefOp(t) {
			c.opLeft = r.Cigar[c.opIndex].Len
			return
		}
		c.opIndex++
	}
	c.state = stateAfter
}

func isRefOp(t byte) bool {
	switch t {
	case 'M', 'D', 'N', '=', 'X':
		return true
	}
	return false
}

// advanceTo positions the cursor on reference position p.
func (c *cursor) advanceTo(p int) {
	c.inserted = c.inserted[:0]
	if c.state == stateAfter {
		return
	}
	r := c.rec
	for c.refPos < p && c.opIndex < len(r.Cigar) {
		t := c.currOp()
		switch t {
		case 'M', '=', 'X':
			c.refPos++
			c.readPos++
			c.opLeft--
		case 'D', 'N':
			c.refPos++
			c.opLeft--
		}
		if c.opLeft == 0 {
			c.moveToNextRefOp()
			if c.state == stateAfter {
				return
			}
		}
	}
	if c.refPos == p {
		switch c.currOp() {
		case 'M', '=', 'X', 'D':
			c.state = stateAligned
		case 'N':
			c.state = stateSkipped
		}
		c.collectInsertionsAfter()
	}
}

func (c *cursor) currOp() byte {
	if c.opIndex >= len(c.rec.Cigar) {
		return 0
	}
	return c.rec.Cigar[c.opIndex].Type
}

// moveToNextRefOp advances past the exhausted parked op and any
// following non-reference ops, consuming their query bases.
func (c *cursor) moveToNextRefOp() {
	r := c.rec
	c.opIndex++
	for c.opIndex < len(r.Cigar) {
		op := r.Cigar[c.opIndex]
		switch op.Type {
		case 'I':
			c.readPos += op.Len
		case 'S':
			c.readPos += op.Len
		case 'H', 'P':
			// no stored query bases
		default:
			c.currOpSet(op.Type, op.Len)
			return
		}
		c.opIndex++
	}
	c.state = stateAfter
}

func (c *cursor) currOpSet(t byte, left int) {
	c.opLeft = left
}

// collectInsertionsAfter gathers I bases directly following the parked
// reference element (i.e. inserted after the current site). A single I
// block is the only legal placement; S/H/P after the last ref base are
// not insertions within the alignment.
func (c *cursor) collectInsertionsAfter() {
	r := c.rec
	if c.opLeft != 1 {
		return
	}
	next := c.opIndex + 1
	if next >= len(r.Cigar) || r.Cigar[next].Type != 'I' {
		return
	}
	op := r.Cigar[next]
	readBase := c.readPos + 1
	for k := 0; k < op.Len; k++ {
		o := Observation{Reverse: r.Flag.Reverse(), Qual: Phred(255)}
		if r.Seq != "*" && readBase+k < len(r.Seq) {
			o.Base = Base(r.Seq[readBase+k])
		}
		if r.Qual != "*" && readBase+k < len(r.Qual) {
			o.Qual = Phred(r.Qual[readBase+k] - 33)
		}
		c.inserted = append(c.inserted, o)
	}
}

func (c *cursor) alignedBase() (Observation, bool) {
	if c.currOp() == 'D' {
		return Observation{}, false
	}
	r := c.rec
	o := Observation{Reverse: r.Flag.Reverse(), Qual: Phred(255), Base: Base('N')}
	if r.Seq != "*" && c.readPos < len(r.Seq) {
		o.Base = Base(r.Seq[c.readPos])
	}
	if r.Qual != "*" && c.readPos < len(r.Qual) {
		o.Qual = Phred(r.Qual[c.readPos] - 33)
	}
	return o, true
}

func (c *cursor) insertionsHere() []Observation { return c.inserted }

func (c *cursor) excluded(cfg Config) bool {
	r := c.rec
	if r.Flag.Unmapped() || r.Flag.Secondary() || r.Flag.Supplementary() {
		return true
	}
	if r.Flag.Duplicate() && !cfg.CountDuplicates {
		return true
	}
	return r.Seq == "*"
}

type activeRecord struct {
	cur *cursor
	end int
}

type activeSet struct {
	records []*activeRecord
}

func (s *activeSet) add(r *sam.Record) {
	if r.Flag.Unmapped() || r.Cigar == nil {
		return
	}
	s.records = append(s.records, &activeRecord{
		cur: newCursor(r),
		end: r.Pos + r.Cigar.ReferenceLength() - 1,
	})
}

func (s *activeSet) dropBefore(p int) {
	kept := s.records[:0]
	for _, a := range s.records {
		if a.end >= p {
			kept = append(kept, a)
		}
	}
	s.records = kept
}
