package stratum

import (
	"bufio"
	"errors"
	"io"
)

var ErrLineTooLong = errors.New("stratum: line exceeds the size limit")

// LineReader reads '\n'-terminated lines with an upper size limit. The
// buffer starts small and grows only for long lines.
type LineReader struct {
	r   *bufio.Reader
	max int
	acc []byte
	err error // sticky: after an error the stream position is undefined
}

func NewLineReader(r io.Reader, max int) *LineReader {
	return &LineReader{r: bufio.NewReaderSize(r, 4096), max: max}
}

// ReadLine returns the next line including its terminator. The slice is
// valid until the next call. Empty lines are skipped. Errors are sticky: an
// oversized line is not skipped, so reading on would start mid-line.
func (l *LineReader) ReadLine() ([]byte, error) {
	if l.err != nil {
		return nil, l.err
	}
	for {
		line, err := l.readRaw()
		if err != nil {
			l.err = err
			return nil, err
		}
		if !blank(line) {
			return line, nil
		}
	}
}

func (l *LineReader) readRaw() ([]byte, error) {
	l.acc = l.acc[:0]
	for {
		chunk, err := l.r.ReadSlice('\n')
		if len(l.acc)+len(chunk) > l.max {
			return nil, ErrLineTooLong
		}
		switch {
		case err == nil:
			if len(l.acc) == 0 {
				return chunk, nil
			}
			l.acc = append(l.acc, chunk...)
			return l.acc, nil
		case errors.Is(err, bufio.ErrBufferFull):
			l.acc = append(l.acc, chunk...)
		default:
			if err == io.EOF && len(l.acc)+len(chunk) > 0 {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
}

func blank(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return false
		}
	}
	return true
}
