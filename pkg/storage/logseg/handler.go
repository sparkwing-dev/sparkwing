package logseg

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// The append protocol a node's log writer speaks: one POST per batch of
// NDJSON lines, numbered by these headers when the writer numbers its
// lines, and a closing seal. The engine serves it over loopback, so a
// pipeline binary built against any SDK keeps logging unchanged.
const (
	LogStreamHeader = "X-Sparkwing-Log-Stream"
	LogSeqHeader    = "X-Sparkwing-Log-Seq"
	LogSeqEndHeader = "X-Sparkwing-Log-Seq-End"
)

// MaxAppendBytes is the largest single append the handler accepts.
const MaxAppendBytes = 4 << 20

var streamIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// WriterSeal is a numbering writer's closing statement, the body of a
// seal request.
type WriterSeal struct {
	Stream   string `json:"stream"`
	FinalSeq int64  `json:"final_seq"`
	Dropped  int64  `json:"dropped"`
}

// AppendHandler serves one node's append protocol into sink:
// POST /api/v1/logs/{run}/{node} appends and POST .../seal records the
// writer's seal. Both answer 204. Any other request answers 404; the
// caller authenticates the request before it reaches the handler.
func AppendHandler(sink *Sink, runID, nodeID string) http.Handler {
	base := "/api/v1/logs/" + url.PathEscape(runID) + "/" + url.PathEscape(nodeID)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		switch r.URL.EscapedPath() {
		case base:
			serveAppend(w, r, sink)
		case base + "/seal":
			serveSeal(w, r, sink)
		default:
			http.NotFound(w, r)
		}
	})
}

func serveAppend(w http.ResponseWriter, r *http.Request, sink *Sink) {
	stream := r.Header.Get(LogStreamHeader)
	var first, last int64
	if stream != "" || r.Header.Get(LogSeqHeader) != "" {
		var err error
		first, err = strconv.ParseInt(r.Header.Get(LogSeqHeader), 10, 64)
		if err != nil || first < 1 || !streamIDPattern.MatchString(stream) {
			http.Error(w, "a numbered append names a stream of 1-64 letters, digits, '-' or '_' and a sequence number from 1", http.StatusBadRequest)
			return
		}
		last = first
		if raw := r.Header.Get(LogSeqEndHeader); raw != "" {
			if last, err = strconv.ParseInt(raw, 10, 64); err != nil || last < first {
				http.Error(w, "the sequence end must not precede its start", http.StatusBadRequest)
				return
			}
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxAppendBytes+1))
	if err != nil {
		http.Error(w, "read the append", http.StatusBadRequest)
		return
	}
	if len(body) > MaxAppendBytes {
		http.Error(w, "an append is limited to 4 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	if err := sink.Append(body, stream, first, last); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func serveSeal(w http.ResponseWriter, r *http.Request, sink *Sink) {
	var seal WriterSeal
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&seal); err != nil ||
		!streamIDPattern.MatchString(seal.Stream) || seal.FinalSeq < 0 || seal.Dropped < 0 {
		http.Error(w, "a seal names its stream, final sequence number and dropped count", http.StatusBadRequest)
		return
	}
	sink.WriterSealed(seal.Stream, seal.FinalSeq, seal.Dropped)
	w.WriteHeader(http.StatusNoContent)
}
