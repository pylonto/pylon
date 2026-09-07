package pidebug

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}

// CheckLocation is lexical and does not inspect any forbidden tree. Start() also
// resolves existing aliases; offline Inspect never visits auth/repository data.
func CheckLocation(path string, forbidden ...string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(os.PathSeparator) {
		return ErrUnsafe
	}
	for _, f := range forbidden {
		if !filepath.IsAbs(f) || overlaps(path, filepath.Clean(f)) {
			return ErrUnsafe
		}
	}
	return nil
}

func privateInfo(info os.FileInfo, dir bool) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) {
		return false
	}
	if dir {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && s.Nlink == 1
}

func openDirectory(path string) (*os.Root, error) {
	info, err := os.Lstat(path) // #nosec G703 -- Stat validates this boundary before the inode-matched os.Root open; it reads no content.
	if err != nil || !privateInfo(info, true) {
		return nil, ErrUnsafe
	}
	// A writable non-sticky ancestor lets someone else replace our private root.
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		parent, e := os.Lstat(p) // #nosec G703 -- Check every ancestor for symlinks and unsafe writers before opening the root.
		if e != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0022 != 0 && parent.Mode()&os.ModeSticky == 0 {
			return nil, ErrUnsafe
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return openMatchingDirectory(path, info)
}

func openMatchingDirectory(path string, expected os.FileInfo) (*os.Root, error) {
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrUnsafe
	}
	actual, err := r.Stat(".")
	if err != nil || !privateInfo(actual, true) || !os.SameFile(expected, actual) {
		r.Close()
		return nil, ErrUnsafe
	}
	return r, nil
}

func readPrivate(root *os.Root, name string, maximum int) ([]byte, error) {
	// NONBLOCK avoids hanging on a substituted FIFO before the post-open stat.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !privateInfo(info, false) || info.Size() > int64(maximum) {
		return nil, ErrUnsafe
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(maximum)+1))
	if err != nil || len(raw) > maximum {
		return nil, ErrInvalid
	}
	return raw, nil
}

// Count abandoned jobs too. Never adopt, chmod or prune an existing destination.
func checkCapacity(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return ErrUnsafe
	}
	defer d.Close()
	entries, err := d.ReadDir(MaxJobs + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrUnsafe
	}
	if len(entries) >= MaxJobs {
		return ErrFull
	}
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil || !ValidJob(entry.Name()) || !privateInfo(info, true) {
			return ErrUnsafe
		}
		job, e := root.OpenRoot(entry.Name())
		if e != nil {
			return ErrUnsafe
		}
		f, e := job.Open(".")
		if e != nil {
			job.Close()
			return ErrUnsafe
		}
		files, e := f.ReadDir(4)
		f.Close()
		if e != nil && !errors.Is(e, io.EOF) || len(files) > 3 {
			job.Close()
			return ErrUnsafe
		}
		for _, file := range files {
			maximum := MaxSummaryBytes
			if file.Name() == "events.jsonl" {
				maximum = MaxEventsBytes
			} else if file.Name() != "summary.json" && file.Name() != ".summary-next" {
				job.Close()
				return ErrUnsafe
			}
			info, e := file.Info()
			if e != nil || !privateInfo(info, false) || info.Size() > int64(maximum) {
				job.Close()
				return ErrUnsafe
			}
		}
		job.Close()
	}
	return nil
}

type Recorder struct {
	mu           sync.Mutex
	root         *os.Root
	file         *os.File
	summary      Summary
	sequence     int
	closePending bool
	closed       bool
}

// Start refuses unsafe/full/unavailable requested storage before model startup.
// The directory flock serializes the final slot across independent processes.
func Start(path string, identity Identity, forbidden ...string) (*Recorder, error) {
	if !identity.valid() || CheckLocation(path, forbidden...) != nil {
		return nil, ErrUnsafe
	}
	for _, f := range forbidden {
		if actual, e := filepath.EvalSymlinks(f); e == nil && overlaps(path, actual) {
			return nil, ErrUnsafe
		}
	}
	root, err := openDirectory(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	lock, err := root.Open(".")
	if err != nil {
		return nil, ErrUnsafe
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return nil, ErrUnavailable
	}
	// The deferred close of this sole lock descriptor releases flock on every path.
	if err := checkCapacity(root); err != nil {
		return nil, err
	}
	if root.Mkdir(identity.Job, 0700) != nil {
		return nil, ErrUnsafe
	}
	expected, err := root.Lstat(identity.Job)
	if err != nil || !privateInfo(expected, true) {
		return nil, ErrUnsafe
	}
	job, err := root.OpenRoot(identity.Job)
	if err != nil {
		return nil, ErrUnsafe
	}
	actual, err := job.Stat(".")
	if err != nil || !os.SameFile(expected, actual) {
		job.Close()
		return nil, ErrUnsafe
	}
	r := &Recorder{root: job, summary: initial(identity)}
	// Initial incomplete state reaches storage before events or runtime startup.
	if err = r.writeSummary(true); err != nil {
		job.Close()
		return nil, err
	}
	r.file, err = job.OpenFile("events.jsonl", os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		job.Close()
		return nil, ErrUnavailable
	}
	info, err := r.file.Stat()
	if err != nil || !privateInfo(info, false) {
		r.file.Close()
		job.Close()
		return nil, ErrUnsafe
	}
	r.append(Record{Source: "host", Event: Event{Kind: "capture_start"}, Identity: &identity})
	if r.summary.CaptureFailure != "" {
		r.file.Close()
		job.Close()
		return nil, ErrUnavailable
	}
	return r, nil
}

func (r *Recorder) writeSummary(first bool) error {
	raw, err := json.Marshal(r.summary)
	if err != nil || len(raw)+1 > MaxSummaryBytes {
		return ErrInvalid
	}
	name := ".summary-next"
	if first {
		name = "summary.json"
	}
	f, err := r.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrUnavailable
	}
	info, e := f.Stat()
	if e != nil || !privateInfo(info, false) {
		f.Close()
		return ErrUnsafe
	}
	_, err = f.Write(append(raw, '\n'))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrUnavailable
	}
	if !first && r.root.Rename(name, "summary.json") != nil {
		return ErrUnavailable
	}
	return nil
}

func (r *Recorder) fail(category string) {
	if r.summary.CaptureFailure == "" {
		r.summary.CaptureFailure = category
	}
	r.summary.Incomplete = true
}
func (r *Recorder) drop() {
	r.summary.Truncated = true
	if r.summary.DroppedEvents < 1000000 {
		r.summary.DroppedEvents++
	}
}
func (r *Recorder) append(row Record) {
	if r.closed {
		return
	}
	// Even a capped/dropped frame is acknowledged only after its updated
	// accounting reaches the reserved summary. Process loss cannot erase drops.
	defer func() {
		if r.writeSummary(false) != nil {
			r.fail("storage")
		}
	}()
	if r.summary.CaptureFailure != "" {
		return
	}
	row.Seq = r.summary.EventCount + 1
	raw, err := json.Marshal(row)
	if err != nil {
		r.fail("encoding")
		return
	}
	if len(raw)+1 > MaxEventBytes || r.summary.EventCount >= MaxEvents || len(raw)+1 > MaxEventsBytes-r.summary.Bytes {
		r.drop()
		return
	}
	raw = append(raw, '\n')
	n, err := r.file.Write(raw)
	if err == nil {
		err = r.file.Sync()
	}
	if err != nil || n != len(raw) {
		r.fail("storage")
		return
	}
	r.summary.EventCount++
	r.summary.Bytes += n
	r.summary.Truncated = r.summary.Truncated || row.Truncated
}

func (r *Recorder) Accept(frame Frame) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.closePending || r.summary.CaptureFailure != "" {
		return ErrUnavailable
	}
	if frame.Sequence != r.sequence+1 || frame.Sequence > MaxEvents+1 {
		r.fail("sequence")
		return ErrInvalid
	}
	if (frame.Event == nil) == (frame.Close == nil) {
		r.fail("protocol")
		return ErrInvalid
	}
	if e := frame.Event; e != nil {
		if frame.Sequence > MaxEvents || !e.valid(true) {
			r.fail("protocol")
			return ErrInvalid
		}
		r.sequence++
		r.append(Record{Source: "runtime", Event: *e})
	} else {
		c := frame.Close
		if c.Events != r.sequence || c.Dropped < 0 || c.Dropped > 1000000 {
			r.fail("protocol")
			return ErrInvalid
		}
		r.sequence++
		r.closePending = true
		r.summary.Truncated = r.summary.Truncated || c.Truncated || c.Dropped > 0
		r.summary.DroppedEvents = min(1000000, r.summary.DroppedEvents+c.Dropped)
		if c.Incomplete {
			r.fail("runtime")
		}
		if r.writeSummary(false) != nil {
			r.fail("storage")
		}
	}
	if r.summary.CaptureFailure != "" {
		return ErrUnavailable
	}
	return nil
}

// ConfirmRuntimeClosure consumes the subsequent result-request header. Receipt
// of a close frame alone does not prove that its fsync acknowledgement reached
// the runtime. A lost acknowledgement is reported as incomplete, without changing
// the separately validated model/accounting result body.
func (r *Recorder) ConfirmRuntimeClosure(acknowledged bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.summary.RuntimeClosed = acknowledged && r.closePending && r.summary.CaptureFailure == ""
	if !r.summary.RuntimeClosed {
		r.fail("runtime")
	}
	if r.writeSummary(false) != nil {
		r.fail("storage")
	}
}

// Host metadata remains in the reserved summary even when private text has used
// the entire event budget. No runtime frame can forge these decision-site facts.
func (r *Recorder) Host(kind, outcome string, counts map[string]int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	e := Event{Kind: kind, Outcome: outcome, Counts: counts}
	if !e.valid(false) {
		r.fail("protocol")
		return
	}
	switch kind {
	case "collection":
		r.summary.Export.Collection = outcome
		r.summary.Export.Allowed = counts["allowed"]
		r.summary.Export.Collected = counts["collected"]
	case "staging":
		r.summary.Export.Staging = outcome
		r.summary.Export.Staged = counts["staged"]
	case "diff":
		r.summary.Export.Diff = outcome
		r.summary.Export.ObservedBytes = counts["bytes"]
		r.summary.Export.TotalBytesKnown = counts["total_known"] == 1
	}
	r.append(Record{Source: "host", Event: e})
}

func (r *Recorder) Close(outcome, failure string, terminated bool) Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.summary
	}
	r.closed = true
	if !member(outcome, "executor_returned", "executor_failed") || failure != "" && !categoryRE.MatchString(failure) {
		r.fail("protocol")
		outcome = "executor_failed"
		failure = "pi_debug_invalid_outcome"
	}
	r.summary.ExecutorOutcome = outcome
	r.summary.Failure = failure
	r.summary.ExecutorClosed = terminated
	if r.file.Sync() != nil {
		r.fail("storage")
	}
	if r.file.Close() != nil {
		r.fail("storage")
	}
	r.summary.Incomplete = !r.summary.RuntimeClosed || !terminated || r.summary.CaptureFailure != ""
	if r.writeSummary(false) != nil {
		r.fail("storage")
	}
	r.root.Close()
	return r.summary
}

type InspectionState struct {
	SummaryMissing bool `json:"summary_missing"`
	SummaryStale   bool `json:"summary_stale"`
	PartialTail    bool `json:"partial_tail"`
}
type Inspection struct {
	V           int             `json:"v"`
	Private     bool            `json:"private"`
	Untrusted   bool            `json:"untrusted"`
	Publishable bool            `json:"publishable"`
	Summary     Summary         `json:"summary"`
	Events      []Record        `json:"events"`
	Inspection  InspectionState `json:"inspection"`
}

// Inspect is read-only and returns bounded JSON, not terminal-rendered prose.
func Inspect(path, name, job, context string, forbidden ...string) (Inspection, error) {
	out := Inspection{V: 1, Private: true, Untrusted: true, Events: []Record{}}
	if !nameRE.MatchString(name) || !ValidJob(job) || !digestRE.MatchString(context) || CheckLocation(path, forbidden...) != nil {
		return out, ErrUnsafe
	}
	root, err := openDirectory(path)
	if err != nil {
		return out, err
	}
	defer root.Close()
	info, err := root.Lstat(job)
	if err != nil || !privateInfo(info, true) {
		return out, ErrUnsafe
	}
	child, err := root.OpenRoot(job)
	if err != nil {
		return out, ErrUnsafe
	}
	defer child.Close()
	actual, err := child.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		return out, ErrUnsafe
	}
	raw, err := readPrivate(child, "summary.json", MaxSummaryBytes)
	if os.IsNotExist(err) {
		out.Inspection.SummaryMissing = true
	} else if err != nil || Decode(raw, &out.Summary) != nil {
		return out, ErrInvalid
	}
	raw, err = readPrivate(child, "events.jsonl", MaxEventsBytes)
	if os.IsNotExist(err) {
		out.Inspection.PartialTail = true
		raw = nil
	} else if err != nil {
		return out, ErrInvalid
	}
	observed := len(raw)
	for len(raw) > 0 {
		line, rest, ok := bytes.Cut(raw, []byte{'\n'})
		if !ok {
			out.Inspection.PartialTail = true
			break
		}
		if len(line)+1 > MaxEventBytes || len(out.Events) >= MaxEvents {
			return out, ErrInvalid
		}
		var row Record
		if Decode(line, &row) != nil || !member(row.Source, "host", "runtime") || !row.valid(row.Source == "runtime") {
			return out, ErrInvalid
		}
		if row.Seq != len(out.Events)+1 {
			out.Inspection.SummaryStale = true
			break
		}
		if row.Seq == 1 {
			if row.Source != "host" || row.Kind != "capture_start" || row.Identity == nil || !row.Identity.valid() {
				return out, ErrInvalid
			}
			if out.Inspection.SummaryMissing {
				out.Summary = initial(*row.Identity)
			} else if out.Summary.Identity != *row.Identity {
				return out, ErrInvalid
			}
		} else if row.Identity != nil || row.Kind == "capture_start" {
			return out, ErrInvalid
		}
		out.Events = append(out.Events, row)
		raw = rest
	}
	if !out.Summary.valid() || out.Summary.Pylon != name || out.Summary.Job != job || out.Summary.Context != context {
		return out, ErrInvalid
	}
	if out.Summary.EventCount < 0 || out.Summary.EventCount > MaxEvents || out.Summary.Bytes < 0 || out.Summary.Bytes > MaxEventsBytes || out.Summary.DroppedEvents < 0 || out.Summary.DroppedEvents > 1000000 {
		return out, ErrInvalid
	}
	if !member(out.Summary.ExecutorOutcome, "", "executor_failed", "executor_returned") || out.Summary.Failure != "" && !categoryRE.MatchString(out.Summary.Failure) || !member(out.Summary.CaptureFailure, "", "encoding", "storage", "sequence", "protocol", "runtime") {
		return out, ErrInvalid
	}
	e := out.Summary.Export
	if !member(e.Collection, "not_attempted", "returned", "failed") || !member(e.Staging, "not_attempted", "returned", "failed") || !member(e.Diff, "not_attempted", "empty", "nonempty", "git_failed", "canceled", "deadline", "output_bound") || e.Allowed < 0 || e.Allowed > 32 || e.Collected < 0 || e.Collected > e.Allowed || e.Staged < 0 || e.Staged > e.Collected || e.ObservedBytes < 0 || e.ObservedBytes > MaxJobBytes {
		return out, ErrInvalid
	}
	if out.Summary.EventCount != len(out.Events) || out.Summary.Bytes != observed {
		out.Inspection.SummaryStale = true
	}
	out.Summary.EventCount = len(out.Events)
	out.Summary.Bytes = observed
	out.Summary.Incomplete = out.Summary.Incomplete || !out.Summary.RuntimeClosed || !out.Summary.ExecutorClosed || out.Summary.CaptureFailure != "" || out.Inspection.SummaryMissing || out.Inspection.SummaryStale || out.Inspection.PartialTail
	return out, nil
}
