package logs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A writer that numbers its appends names its stream and each append's
// sequence number in these headers. Sending them is how a writer
// advertises that it will seal the stream when it finishes; a writer that
// never sends them reads back as [StateUnconfirmed] rather than
// [StateCutOff].
const (
	LogStreamHeader = "X-Sparkwing-Log-Stream"
	LogSeqHeader    = "X-Sparkwing-Log-Seq"
)

// SealGrace is how long after a node finishes a reader waits for the
// writer's seal before calling the log cut off.
const SealGrace = 60 * time.Second

// Completeness states a reader reports for one node's log.
const (
	// StateStreaming: the node is still running, or finished less than
	// [SealGrace] ago and its seal has not arrived yet.
	StateStreaming = "streaming"
	// StateComplete: every stream was sealed, with no gaps and nothing
	// the writer failed to deliver.
	StateComplete = "complete"
	// StateIncomplete: sealed, but lines are missing.
	StateIncomplete = "incomplete"
	// StateCutOff: the node finished and a stream that promised a seal
	// never sent one.
	StateCutOff = "cut_off"
	// StateUnconfirmed: the writer never advertised seal support, so
	// nothing says whether the log is whole.
	StateUnconfirmed = "unconfirmed"
	// StateUnknown: the log store does not track completeness.
	StateUnknown = "unknown"
)

const (
	sealsDir        = ".seals"
	maxTrackedGaps  = 32
	maxSealBody     = 4 << 10
	maxLiveTrackers = 10000
	trackerIdle     = time.Hour
)

var streamIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Seal is a writer's closing statement for one stream: how far it
// numbered, what it sent, and what it failed to deliver. A seal is
// metadata beside the log, never a line in it.
type Seal struct {
	Stream string `json:"stream"`
	// FinalSeq is the last sequence number the stream assigned; zero for
	// a stream that wrote nothing.
	FinalSeq int64 `json:"final_seq"`
	Lines    int64 `json:"lines"`
	Bytes    int64 `json:"bytes"`
	// Dropped counts lines the writer numbered but never delivered: an
	// append that failed after its retries, a line held back while the
	// store was failing, a record that could not be encoded, and similar.
	Dropped int64 `json:"dropped"`
	// SHA256 is the hex digest of every byte the stream numbered, in
	// order, so a copy of the log held elsewhere can be checked against it.
	SHA256 string `json:"sha256,omitempty"`
}

// Gap is an inclusive range of sequence numbers the service never received.
type Gap struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// StreamReport is what the service knows about one writer's stream.
type StreamReport struct {
	Stream string `json:"stream"`
	// File is the stored log file the stream last wrote, relative to the
	// run; Files is every file it wrote, since a writer that spans
	// execution attempts writes one per attempt.
	File     string    `json:"file"`
	Files    []string  `json:"files"`
	OpenedAt time.Time `json:"opened_at"`
	Sealed   bool      `json:"sealed"`
	Seal     *Seal     `json:"seal,omitempty"`
	SealedAt time.Time `json:"sealed_at,omitzero"`
	// Received and Missing count sequence numbers up to the seal's
	// FinalSeq, or up to the highest one seen for an unsealed stream.
	Received int64 `json:"received"`
	Missing  int64 `json:"missing"`
	Gaps     []Gap `json:"gaps,omitempty"`
}

// SealReport is the service's account of one node's log streams.
type SealReport struct {
	// Lines counts the newline-terminated lines stored for the node.
	Lines   int64          `json:"lines"`
	Streams []StreamReport `json:"streams"`
	// UnconfirmedFiles counts stored log files that no numbering writer
	// wrote, which is what a runner without seal support leaves.
	UnconfirmedFiles int `json:"unconfirmed_files"`
	// LatestFile is the node's newest execution attempt's log file, the
	// one a verdict judges; LatestUnconfirmed says no numbering writer
	// wrote it.
	LatestFile        string `json:"latest_file,omitempty"`
	LatestUnconfirmed bool   `json:"latest_unconfirmed,omitempty"`
}

// latest narrows the report to the node's newest execution attempt, so a
// clean retry is not held to an attempt it replaced.
func (r SealReport) latest() SealReport {
	if r.LatestFile == "" {
		return r
	}
	out := r
	out.Streams = nil
	for _, s := range r.Streams {
		if slices.Contains(s.Files, r.LatestFile) {
			out.Streams = append(out.Streams, s)
		}
	}
	out.UnconfirmedFiles = 0
	if r.LatestUnconfirmed {
		out.UnconfirmedFiles = 1
	}
	return out
}

// NodeProgress is what a reader knows about the node from the controller.
type NodeProgress struct {
	Started    bool
	Terminal   bool
	FinishedAt time.Time
}

// Completeness is a reader's verdict on one node's log. Message is the
// synthetic line a reader renders after the log; it is empty when there
// is nothing to say.
type Completeness struct {
	State        string `json:"state"`
	Lines        int64  `json:"lines"`
	MissingLines int64  `json:"missing_lines,omitempty"`
	Message      string `json:"message,omitempty"`
}

// Assess turns the service's report and the node's progress into one
// verdict on the node's newest execution attempt. A worse state wins:
// cut off, then incomplete, then unconfirmed.
func (r SealReport) Assess(node NodeProgress, now time.Time) Completeness {
	r = r.latest()
	c := Completeness{State: StateComplete, Lines: r.Lines}
	if !node.Started && len(r.Streams) == 0 && r.UnconfirmedFiles == 0 {
		return c
	}
	if !node.Terminal {
		c.State = StateStreaming
		return c
	}
	var missing int64
	unsealed := false
	for _, s := range r.Streams {
		if !s.Sealed {
			unsealed = true
			continue
		}
		lost := s.Missing
		if s.Seal != nil && s.Seal.Dropped > lost {
			lost = s.Seal.Dropped
		}
		missing += lost
	}
	unconfirmed := r.UnconfirmedFiles > 0
	nothing := len(r.Streams) == 0 && !unconfirmed
	switch {
	case unsealed || nothing:
		if now.Sub(node.FinishedAt) < SealGrace {
			c.State = StateStreaming
			return c
		}
		c.State = StateCutOff
		c.Message = fmt.Sprintf("logs cut off: the log stream ended without the runner's confirmation after line %s", groupDigits(r.Lines))
	case missing > 0:
		c.State = StateIncomplete
		c.MissingLines = missing
		c.Message = fmt.Sprintf("logs incomplete: %s %s missing", groupDigits(missing), plural(missing, "line", "lines"))
	case unconfirmed:
		c.State = StateUnconfirmed
		c.Message = fmt.Sprintf("logs unconfirmed: this runner does not report whether its log is complete (%s %s stored)", groupDigits(r.Lines), plural(r.Lines, "line", "lines"))
	}
	return c
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// SyntheticLine renders c's message as the line a reader shows after the
// log, or "" when there is nothing to show. It is never stored.
func (c Completeness) SyntheticLine() string {
	if c.Message == "" {
		return ""
	}
	return "— " + c.Message + " —"
}

type appendSequenceKey struct{}

type appendSequence struct {
	stream string
	seq    int64
}

// WithAppendSequence numbers the append made with the returned context:
// seq is its position in stream. [Client.Append] sends both, and a writer
// that numbers its appends seals the stream with [Client.Seal] when it
// finishes.
func WithAppendSequence(ctx context.Context, stream string, seq int64) context.Context {
	return context.WithValue(ctx, appendSequenceKey{}, appendSequence{stream: stream, seq: seq})
}

func appendSequenceFromContext(ctx context.Context) (appendSequence, bool) {
	s, ok := ctx.Value(appendSequenceKey{}).(appendSequence)
	return s, ok
}

func setClaimHeaders(ctx context.Context, req *http.Request) {
	if fence, ok := store.NodeClaimFenceFromContext(ctx); ok {
		req.Header.Set(store.ClaimHolderHeader, fence.HolderID)
		req.Header.Set(store.ClaimMembershipHeader, fence.MembershipID)
		req.Header.Set(store.ClaimReservationHeader, fence.ReservationID)
		req.Header.Set(store.ClaimGenerationHeader, fmt.Sprint(fence.ClaimGeneration))
	}
	if ordinal, ok := store.ExecutionAttemptOrdinalFromContext(ctx); ok {
		req.Header.Set(store.AttemptOrdinalHeader, fmt.Sprint(ordinal))
	}
	if fence, ok := store.TriggerClaimFenceFromContext(ctx); ok {
		req.Header.Set(store.TriggerGenerationHeader, fmt.Sprint(fence.ClaimGeneration))
	}
}

// Seal records the end of one numbered stream of a node's log. It carries
// the same claim identity as the stream's appends, so only the node's
// current claim holder can seal it. Sealing a stream twice is a no-op.
// Errors follow [Client.Append].
func (c *Client) Seal(ctx context.Context, runID, nodeID string, seal Seal) error {
	body, err := json.Marshal(seal)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sealURL(runID, nodeID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setClaimHeaders(ctx, req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	err = writeResponseError("logs seal", resp)
	var authErr *AuthError
	if err != nil && resp.StatusCode/100 == 4 && !errors.As(err, &authErr) && !errors.Is(err, ErrClaimConflict) {
		return fmt.Errorf("%w: %w", ErrSealRefused, err)
	}
	return err
}

// ErrSealRefused is the error [Client.Seal] wraps when the service turns a
// seal down for good: a service that predates seals, a malformed seal, and
// similar. Retrying it cannot succeed.
var ErrSealRefused = errors.New("logs seal refused")

// ReadSeals returns the service's account of a node's log streams.
func (c *Client) ReadSeals(ctx context.Context, runID, nodeID string) (SealReport, error) {
	var report SealReport
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sealURL(runID, nodeID), nil)
	if err != nil {
		return report, err
	}
	c.setRunnerIdentity(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return report, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return report, writeResponseError("logs read seals", resp)
	}
	return report, json.NewDecoder(resp.Body).Decode(&report)
}

func (c *Client) sealURL(runID, nodeID string) string {
	return fmt.Sprintf("%s/api/v1/logs/%s/%s/seal", c.baseURL, url.PathEscape(runID), url.PathEscape(nodeID))
}

func writeResponseError(op string, resp *http.Response) error {
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return fmt.Errorf("%s %d: read response: %w", op, resp.StatusCode, err)
	}
	trimmed := string(bytes.TrimSpace(body))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &AuthError{Status: resp.StatusCode, Scope: parseMissingScope(trimmed), RawBody: trimmed}
	}
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("%w: %s", ErrClaimConflict, trimmed)
	}
	return fmt.Errorf("%s %d: %s", op, resp.StatusCode, trimmed)
}

func appendSequenceFromRequest(r *http.Request) (appendSequence, bool, error) {
	stream, rawSeq := r.Header.Get(LogStreamHeader), r.Header.Get(LogSeqHeader)
	if stream == "" && rawSeq == "" {
		return appendSequence{}, false, nil
	}
	if !streamIDPattern.MatchString(stream) {
		return appendSequence{}, false, errors.New("log stream must be 1-64 letters, digits, '-' or '_'")
	}
	seq, err := strconv.ParseInt(rawSeq, 10, 64)
	if err != nil || seq < 1 {
		return appendSequence{}, false, errors.New("log sequence must be a positive integer")
	}
	return appendSequence{stream: stream, seq: seq}, true, nil
}

// sealRecord is one line of a node's seal file. The file is append-only,
// so the archive, which re-uploads a file only when it grows, never
// misses a change.
type sealRecord struct {
	Kind     string    `json:"kind"`
	File     string    `json:"file"`
	Stream   string    `json:"stream"`
	At       time.Time `json:"at"`
	Seal     *Seal     `json:"seal,omitempty"`
	Received int64     `json:"received,omitempty"`
	Missing  int64     `json:"missing,omitempty"`
	Gaps     []Gap     `json:"gaps,omitempty"`
}

const (
	recordOpen = "open"
	recordSeal = "seal"
)

// safety: the seal file ends in .log so the archive carries it, and sits
// in its own directory so no read or search takes it for a node's log.
func nodeSealPath(runID, nodeID string) string {
	return filepath.Join(runID, sealsDir, nodeFile(nodeID))
}

func readSealRecords(root *os.Root, runID, nodeID string) ([]sealRecord, error) {
	data, err := readLogFile(root, nodeSealPath(runID, nodeID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []sealRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var rec sealRecord
		// safety: a torn last line from a crash mid-write is skipped rather
		// than hiding every record before it.
		if json.Unmarshal(sc.Bytes(), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (s *Server) appendSealRecord(root *os.Root, runID, nodeID string, rec sealRecord) error {
	if err := s.ensureRunDir(root, runID); err != nil {
		return err
	}
	if err := root.Mkdir(filepath.Join(runID, sealsDir), s.dirMode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := s.openAppend(root, nodeSealPath(runID, nodeID))
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	return errors.Join(werr, f.Close())
}

// hasRecord reports whether stream has a record of kind; an empty kind
// matches any.
func hasRecord(recs []sealRecord, kind, stream string) bool {
	for _, r := range recs {
		if r.Stream == stream && (kind == "" || r.Kind == kind) {
			return true
		}
	}
	return false
}

// streamTracker counts what arrived of one stream since this process
// first saw it. One stream can span several log files, because a writer
// keeps numbering across the node's execution attempts; files names the
// ones it has an open record in.
type streamTracker struct {
	highest  int64
	missing  int64
	gaps     []Gap
	files    map[string]bool
	lastSeen time.Time
}

func (t *streamTracker) observe(seq int64, now time.Time) {
	t.lastSeen = now
	switch {
	case seq == t.highest+1:
		t.highest = seq
	case seq > t.highest+1:
		t.addGap(t.highest+1, seq-1)
		t.highest = seq
	default:
		t.fill(seq)
	}
}

func (t *streamTracker) addGap(from, to int64) {
	t.missing += to - from + 1
	if len(t.gaps) < maxTrackedGaps {
		t.gaps = append(t.gaps, Gap{From: from, To: to})
	}
}

// fill counts a late arrival inside a recorded gap; anything else below
// the highest number is a retried duplicate.
func (t *streamTracker) fill(seq int64) {
	for i, g := range t.gaps {
		if seq < g.From || seq > g.To {
			continue
		}
		t.missing--
		switch {
		case g.From == g.To:
			t.gaps = append(t.gaps[:i], t.gaps[i+1:]...)
		case seq == g.From:
			t.gaps[i].From++
		case seq == g.To:
			t.gaps[i].To--
		default:
			rest := Gap{From: seq + 1, To: g.To}
			t.gaps[i].To = seq - 1
			t.gaps = append(t.gaps[:i+1], append([]Gap{rest}, t.gaps[i+1:]...)...)
		}
		return
	}
}

// upTo reports the tracker's counts over sequence numbers 1..final.
func (t *streamTracker) upTo(final int64) (received, missing int64, gaps []Gap) {
	missing = t.missing
	gaps = append([]Gap(nil), t.gaps...)
	if final > t.highest {
		missing += final - t.highest
		if len(gaps) < maxTrackedGaps {
			gaps = append(gaps, Gap{From: t.highest + 1, To: final})
		}
	}
	return max(final, t.highest) - missing, missing, gaps
}

// sealTrackers holds the live streams' trackers. It lives in memory: a
// restart loses what arrived before it, and a stream seen again after one
// is trusted for the numbers it sent before.
type sealTrackers struct {
	mu sync.Mutex
	m  map[string]*streamTracker
}

func trackerKey(runID, nodeID, stream string) string {
	return nodePath(runID, nodeID) + "\x00" + stream
}

func (ts *sealTrackers) put(key string, t *streamTracker, now time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.m == nil {
		ts.m = map[string]*streamTracker{}
	}
	// safety: a writer that dies before sealing leaves its tracker behind,
	// so the map sheds idle ones before it grows past its bound.
	if len(ts.m) >= maxLiveTrackers {
		for k, old := range ts.m {
			if now.Sub(old.lastSeen) > trackerIdle {
				delete(ts.m, k)
			}
		}
	}
	ts.m[key] = t
}

func (ts *sealTrackers) drop(key string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.m, key)
}

func (ts *sealTrackers) snapshot(key string) (streamTracker, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.m[key]
	if !ok {
		return streamTracker{}, false
	}
	cp := *t
	cp.gaps = append([]Gap(nil), t.gaps...)
	return cp, true
}

// observeSequence records one numbered append. The caller holds the
// node's append lock, so the seal file and the tracker change together.
func (s *Server) observeSequence(root *os.Root, runID, nodeID, file string, seq appendSequence) error {
	now := time.Now()
	key := trackerKey(runID, nodeID, seq.stream)
	rel := sealFileRel(runID, file)
	s.seals.mu.Lock()
	t := s.seals.m[key]
	opened := false
	if t != nil {
		t.observe(seq.seq, now)
		opened = t.files[rel]
		t.files[rel] = true
	}
	s.seals.mu.Unlock()
	if t != nil {
		if opened {
			return nil
		}
		return s.appendSealRecord(root, runID, nodeID, sealRecord{Kind: recordOpen, File: rel, Stream: seq.stream, At: now})
	}
	recs, err := readSealRecords(root, runID, nodeID)
	if err != nil {
		return err
	}
	t = &streamTracker{files: map[string]bool{}, lastSeen: now}
	for _, r := range recs {
		if r.Stream == seq.stream {
			t.files[r.File] = true
		}
	}
	// safety: a stream this process has no tracker for but the seal file
	// knows was seen before a restart, so the numbers it sent then count.
	if len(t.files) > 0 {
		t.highest = seq.seq - 1
	}
	if !t.files[rel] {
		if err := s.appendSealRecord(root, runID, nodeID, sealRecord{Kind: recordOpen, File: rel, Stream: seq.stream, At: now}); err != nil {
			return err
		}
		t.files[rel] = true
	}
	t.observe(seq.seq, now)
	s.seals.put(key, t, now)
	return nil
}

func sealFileRel(runID, file string) string {
	return filepath.ToSlash(strings.TrimPrefix(file, runID+string(filepath.Separator)))
}

func (s *Server) handleSeal(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	if err := validateIDs(runID, nodeID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	identity, err := appendIdentityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var seal Seal
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSealBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&seal); err != nil {
		http.Error(w, "seal body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !streamIDPattern.MatchString(seal.Stream) {
		http.Error(w, "seal stream must be 1-64 letters, digits, '-' or '_'", http.StatusBadRequest)
		return
	}
	if seal.FinalSeq < 0 || seal.Lines < 0 || seal.Bytes < 0 || seal.Dropped < 0 {
		http.Error(w, "seal counts must not be negative", http.StatusBadRequest)
		return
	}
	if _, status, err := s.validateAppendClaim(r, runID, nodeID); err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer s.closeRoot(root, "seal")
	if err := s.ensureRunDir(root, runID); err != nil {
		s.storeError(w, "create run dir", err)
		return
	}
	if err := s.labelRun(r, root, runID); err != nil {
		if errors.Is(err, errRunOfAnotherTeam) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		s.storeError(w, "label run", err)
		return
	}
	file := identity.path(runID, nodeID)
	rel := sealFileRel(runID, file)
	lock := s.appendNodeLock(runID, nodeID)
	lock.Lock()
	defer lock.Unlock()
	recs, err := readSealRecords(root, runID, nodeID)
	if err != nil {
		s.storeError(w, "read seal file", err)
		return
	}
	if hasRecord(recs, recordSeal, seal.Stream) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	key := trackerKey(runID, nodeID, seal.Stream)
	rec := sealRecord{Kind: recordSeal, File: rel, Stream: seal.Stream, At: time.Now(), Seal: &seal}
	switch t, ok := s.seals.snapshot(key); {
	case ok:
		rec.Received, rec.Missing, rec.Gaps = t.upTo(seal.FinalSeq)
	case hasRecord(recs, "", seal.Stream):
		rec.Received = seal.FinalSeq
	default:
		rec.Missing = seal.FinalSeq
		if seal.FinalSeq > 0 {
			rec.Gaps = []Gap{{From: 1, To: seal.FinalSeq}}
		}
	}
	if err := s.appendSealRecord(root, runID, nodeID, rec); err != nil {
		s.storeError(w, "write seal file", err)
		return
	}
	s.seals.drop(key)
	// safety: a sealed node wrote its last line, so its run's block is
	// committed now rather than held until two idle settles pass.
	s.settleRunLogBlocks(r.Context(), runID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReadSeals(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	if err := validateIDs(runID, nodeID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer s.closeRoot(root, "read seals")
	report, err := s.sealReport(root, runID, nodeID)
	if err != nil {
		s.storeError(w, "read seals", err)
		return
	}
	writeJSONResponse(w, http.StatusOK, report)
}

func (s *Server) sealReport(root *os.Root, runID, nodeID string) (SealReport, error) {
	report := SealReport{Streams: []StreamReport{}}
	names, err := nodeLogNames(root, runID, nodeID)
	if err != nil && !os.IsNotExist(err) {
		return report, err
	}
	recs, err := readSealRecords(root, runID, nodeID)
	if err != nil {
		return report, err
	}
	claimed := map[string]bool{}
	index := map[string]int{}
	for _, rec := range recs {
		claimed[rec.File] = true
		i, seen := index[rec.Stream]
		if !seen {
			i = len(report.Streams)
			index[rec.Stream] = i
			report.Streams = append(report.Streams, StreamReport{Stream: rec.Stream, File: rec.File, OpenedAt: rec.At})
		}
		if sr := &report.Streams[i]; !slices.Contains(sr.Files, rec.File) {
			sr.Files = append(sr.Files, rec.File)
		}
		if rec.Kind == recordSeal {
			sr := &report.Streams[i]
			sr.File = rec.File
			sr.Sealed, sr.Seal, sr.SealedAt = true, rec.Seal, rec.At
			sr.Received, sr.Missing, sr.Gaps = rec.Received, rec.Missing, rec.Gaps
		}
	}
	for i := range report.Streams {
		sr := &report.Streams[i]
		if sr.Sealed {
			continue
		}
		if t, ok := s.seals.snapshot(trackerKey(runID, nodeID, sr.Stream)); ok {
			sr.Received, sr.Missing, sr.Gaps = t.upTo(t.highest)
		}
	}
	for _, name := range names {
		lines, size, err := countLines(root, filepath.Join(runID, name))
		if err != nil {
			return report, err
		}
		report.Lines += lines
		unconfirmed := size > 0 && !claimed[filepath.ToSlash(name)]
		if unconfirmed {
			report.UnconfirmedFiles++
		}
		// safety: nodeLogNames orders the legacy file first and attempts by
		// ordinal, so the last name is the newest attempt.
		report.LatestFile, report.LatestUnconfirmed = filepath.ToSlash(name), unconfirmed
	}
	return report, nil
}

// perf: a node's log can run to its cap, so it is counted in chunks rather
// than read whole on every completeness question.
func countLines(root *os.Root, path string) (lines, size int64, err error) {
	f, err := root.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	buf := make([]byte, 64<<10)
	for {
		n, rerr := f.Read(buf)
		size += int64(n)
		lines += int64(bytes.Count(buf[:n], []byte{'\n'}))
		if errors.Is(rerr, io.EOF) {
			return lines, size, nil
		}
		if rerr != nil {
			return lines, size, rerr
		}
	}
}
