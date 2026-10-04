package samproc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/guoyyit/sam-record-processor-go/internal/sam"
)

// A chunk file is a plain, self-contained SAM fragment: the (possibly
// empty) verbatim header followed by coordinate-sorted alignment lines.
// Complete chunk files can therefore be reused directly as merge inputs.

// writeChunk writes records as a complete chunk file at path. The file is
// created as path+".tmp" and atomically renamed, so an interrupted write
// never leaves a partial file under the final name.
func writeChunk(path string, h *sam.Header, records []*sam.Record) (retErr error) {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
		if retErr != nil {
			_ = os.Remove(tmp)
		}
	}()
	bw := bufio.NewWriterSize(f, 1<<16)
	if h != nil {
		if _, err := bw.Write(h.Raw()); err != nil {
			return err
		}
	}
	lineBuf := make([]byte, 0, 256)
	for _, r := range records {
		lineBuf = r.Append(lineBuf[:0])
		lineBuf = append(lineBuf, '\n')
		if _, err := bw.Write(lineBuf); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// chunkReader streams records from one chunk or SAM input file.
type chunkReader struct {
	f      *os.File
	br     *bufio.Reader
	header *sam.Header
	lineNo int
}

// openChunk opens path, parses and validates its header, and positions
// the reader at the first alignment line.
func openChunk(path string) (*chunkReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	cr := &chunkReader{f: f, br: bufio.NewReaderSize(f, 1<<16)}
	h, err := sam.ParseHeader(cr.br)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	cr.header = h
	return cr, nil
}

// Header returns the parsed header.
func (c *chunkReader) Header() *sam.Header { return c.header }

// Next reads the next record, returning io.EOF when exhausted.
func (c *chunkReader) Next() (*sam.Record, error) {
	for {
		var line []byte
		finalErr := error(nil)
		for {
			part, err := c.br.ReadSlice('\n')
			// Copy each fragment: records must not retain the large
			// reusable buffer backing the bufio.Reader, otherwise a
			// single big line inflates the lifetime memory of every
			// later record.
			cp := make([]byte, len(part))
			copy(cp, part)
			line = append(line, cp...)
			if err == nil || errors.Is(err, io.EOF) {
				finalErr = err
				if len(line) == 0 && errors.Is(err, io.EOF) {
					return nil, io.EOF
				}
				if len(line) > 0 {
					break
				}
				return nil, io.EOF
			}
			if err != bufio.ErrBufferFull {
				return nil, err
			}
		}
		c.lineNo++
		trim := trimLF(line)
		if len(trim) == 0 {
			if finalErr != nil {
				return nil, finalErr
			}
			continue
		}
		r, perr := sam.ParseRecord(trim, c.lineNo)
		if perr != nil {
			return nil, perr
		}
		if verr := r.Validate(c.header); verr != nil {
			return nil, verr
		}
		if finalErr != nil && !errors.Is(finalErr, io.EOF) {
			return nil, finalErr
		}
		return r, nil
	}
}

func (c *chunkReader) Close() error { return c.f.Close() }

func trimLF(line []byte) []byte {
	line = trimSuffixByte(line, '\n')
	line = trimSuffixByte(line, '\r')
	return line
}

func trimSuffixByte(b []byte, s byte) []byte {
	if len(b) > 0 && b[len(b)-1] == s {
		return b[:len(b)-1]
	}
	return b
}

// tempChunkPath builds a chunk file name inside dir.
func tempChunkPath(dir string, id uint64) string {
	return filepath.Join(dir, chunkPrefix+itoa(id)+chunkSuffix)
}

// tempMergePath builds an intermediate cascade merge file name.
func tempMergePath(dir string, level, group int) string {
	return filepath.Join(dir,
		fmt.Sprintf(".samproc-merge-L%d-G%d.sam", level, group))
}

const (
	chunkPrefix = ".samproc-chunk-"
	chunkSuffix = ".sam"
)

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
