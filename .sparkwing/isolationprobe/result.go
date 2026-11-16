package isolationprobe

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Verdict is a probe's judgement of one outcome.
type Verdict string

const (
	// Pass means the containment held, or a positive control succeeded.
	Pass Verdict = "pass"
	// Breach means the job reached something the design fences off.
	Breach Verdict = "BREACH"
	// Inconclusive means the probe proved nothing, which fails the run like a breach.
	Inconclusive Verdict = "INCONCLUSIVE"
	// Observed records a fact without judging it.
	Observed Verdict = "observed"
	// NotProbed marks a best-effort probe that could not run; it never fails the run.
	NotProbed Verdict = "not-probed"
)

// Fails reports whether the verdict fails the run.
func (v Verdict) Fails() bool { return v == Breach || v == Inconclusive }

// Contexts a probe runs from: the job container itself, a DinD container on Docker's bridge, and a
// DinD container with every privilege the sidecar can hand out.
const (
	ContextJob        = "job"
	ContextBridge     = "dind-bridge"
	ContextPrivileged = "dind-privileged"
)

// Result is one probe's outcome. Outcome says what happened, such as timeout, refused or
// connected; http-401, and never carries a value the probe read.
type Result struct {
	Item    string  `json:"item"`
	Context string  `json:"context"`
	Probe   string  `json:"probe"`
	Target  string  `json:"target,omitempty"`
	Expect  Expect  `json:"expect"`
	Outcome string  `json:"outcome"`
	Verdict Verdict `json:"verdict"`
	Millis  int64   `json:"ms"`
}

// Ready is what a run announces once its listener is up, so the driver can aim later runs at it
// and knows which checks the run owes.
type Ready struct {
	PodIPs     []string `json:"pod_ips"`
	ListenPort int      `json:"listen_port"`
	NetNS      string   `json:"netns"`
	NodeIP     string   `json:"node_ip,omitempty"`
	Home       string   `json:"home,omitempty"`
}

// Summary counts a run's verdicts. OK is false when any verdict fails the run or no result exists.
type Summary struct {
	Pass         int  `json:"pass"`
	Breach       int  `json:"breach"`
	Inconclusive int  `json:"inconclusive"`
	Observed     int  `json:"observed"`
	NotProbed    int  `json:"not_probed"`
	OK           bool `json:"ok"`
}

// Markers that open the probe's log lines. Log transports may prefix a line, so a reader searches
// for the marker anywhere in it.
const (
	ResultMarker  = "isolation-probe-result "
	ReadyMarker   = "isolation-probe-ready "
	SummaryMarker = "isolation-probe-summary "
)

// Summarize counts results into a Summary.
func Summarize(results []Result) Summary {
	var s Summary
	for _, r := range results {
		switch r.Verdict {
		case Pass:
			s.Pass++
		case Breach:
			s.Breach++
		case Observed:
			s.Observed++
		case NotProbed:
			s.NotProbed++
		default:
			s.Inconclusive++
		}
	}
	s.OK = len(results) > 0 && s.Breach == 0 && s.Inconclusive == 0
	return s
}

// FormatLine renders one marker line.
func FormatLine(marker string, v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return marker + fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return marker + string(data)
}

// Log is what ParseLog finds in a run's log text.
type Log struct {
	Results   []Result
	Ready     []Ready
	Summaries []Summary
}

// ParseLog extracts every marker line from log text. A marker line that does not parse is an
// error, because a result the reader cannot judge must not disappear.
func ParseLog(text string) (Log, error) {
	var out Log
	for n, line := range strings.Split(text, "\n") {
		for _, m := range []string{ResultMarker, ReadyMarker, SummaryMarker} {
			i := strings.Index(line, m)
			if i < 0 {
				continue
			}
			payload := strings.TrimSpace(line[i+len(m):])
			var err error
			switch m {
			case ResultMarker:
				var r Result
				if err = json.Unmarshal([]byte(payload), &r); err == nil {
					out.Results = append(out.Results, r)
				}
			case ReadyMarker:
				var r Ready
				if err = json.Unmarshal([]byte(payload), &r); err == nil {
					out.Ready = append(out.Ready, r)
				}
			case SummaryMarker:
				var s Summary
				if err = json.Unmarshal([]byte(payload), &s); err == nil {
					out.Summaries = append(out.Summaries, s)
				}
			}
			if err != nil {
				return out, fmt.Errorf("line %d: %s: %w", n+1, strings.TrimSpace(m), err)
			}
			break
		}
	}
	return out, nil
}
