package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A tier is one resolution of stored points: a directory of JSON-lines
// files, one per UTC day or month, one point per line.
type tier struct {
	dir     string
	step    int64 // seconds per point
	monthly bool  // one file per month, else per day
}

var (
	minuteTier       = tier{dir: "minutes", step: 60}
	hourTier         = tier{dir: "hours", step: 3600, monthly: true}
	workerDetailTier = tier{dir: "workers-10m", step: 600}
	workerHourTier   = tier{dir: "workers-hours", step: 3600, monthly: true}
)

const ext = ".jsonl"

func (t tier) layout() string {
	if t.monthly {
		return "2006-01"
	}
	return "2006-01-02"
}

// fileStart is the start of the file holding unix time ts.
func (t tier) fileStart(ts int64) time.Time {
	u := time.Unix(ts, 0).UTC()
	if t.monthly {
		return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func (t tier) nextFile(start time.Time) time.Time {
	if t.monthly {
		return start.AddDate(0, 1, 0)
	}
	return start.AddDate(0, 0, 1)
}

func (r *Recorder) file(t tier, ts int64) string {
	return filepath.Join(r.dir, t.dir, t.fileStart(ts).Format(t.layout())+ext)
}

// appendPoint writes one line with a single write, so a crash leaves at most
// one unreadable line, which readers skip. If the file does not end with a
// newline (a write cut short), the new line starts on a line of its own.
func appendPoint(path string, p Point) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	line := append(b, '\n')
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readFile returns the points of one file with from <= T < to. A missing
// file is no data; damaged lines are skipped.
func readFile(path string, from, to int64) ([]Point, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Point
	sc := bufio.NewScanner(f)
	// A worker line grows with the farm: allow up to 16 MiB.
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var p Point
		if json.Unmarshal(sc.Bytes(), &p) != nil || p.T < from || p.T >= to {
			continue
		}
		out = append(out, p)
	}
	return out, sc.Err()
}

// read returns the points of a tier with from <= T < to.
func (r *Recorder) read(t tier, from, to int64) ([]Point, error) {
	var out []Point
	for start := t.fileStart(from); start.Unix() < to; start = t.nextFile(start) {
		pts, err := readFile(r.file(t, start.Unix()), from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, pts...)
	}
	return out, nil
}

// files lists the files of a tier with the time each one starts, oldest
// first. Files with other names are ignored.
func (r *Recorder) files(t tier) ([]string, []time.Time) {
	dir := filepath.Join(r.dir, t.dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ext) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var paths []string
	var starts []time.Time
	for _, n := range names {
		start, err := time.Parse(t.layout(), strings.TrimSuffix(n, ext))
		if err != nil {
			continue
		}
		paths = append(paths, filepath.Join(dir, n))
		starts = append(starts, start)
	}
	return paths, starts
}

// newest is the start of the newest point of a tier, or 0.
func (r *Recorder) newest(t tier) int64 {
	paths, _ := r.files(t)
	for i := len(paths) - 1; i >= 0; i-- {
		pts, err := readFile(paths[i], 0, 1<<62)
		if err != nil || len(pts) == 0 {
			continue
		}
		last := int64(0)
		for _, p := range pts {
			last = max(last, p.T)
		}
		return last
	}
	return 0
}

// prune deletes the files of a tier that end before cut.
func (r *Recorder) prune(t tier, cut time.Time) error {
	paths, starts := r.files(t)
	var errs []error
	for i, path := range paths {
		if !t.nextFile(starts[i]).After(cut) {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// DiskUsage is the size of the stored history in bytes.
func (r *Recorder) DiskUsage() int64 {
	var total int64
	filepath.WalkDir(r.dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
