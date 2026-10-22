package secrets

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// TextStream preserves byte boundaries until masking is complete.
type TextStream struct {
	stream *Stream
	held   string
}

func (s *Stream) Lines() *TextStream { return &TextStream{stream: s} }

func (m *Masker) lines() *TextStream { return &TextStream{stream: m.Stream()} }

func (s *TextStream) Push(chunk string, eof bool) []string {
	s.held += s.stream.Mask(chunk)
	if eof {
		s.held += s.stream.Flush()
	}
	var lines []string
	for {
		before, after, ok := strings.Cut(s.held, "\n")
		if !ok {
			break
		}
		lines = append(lines, before)
		s.held = after
	}
	if (eof || len(s.held) >= 1<<20) && s.held != "" {
		text := s.held
		if !eof {
			text = s.Idle()
		} else {
			s.held = ""
		}
		if text != "" {
			lines = append(lines, text)
		}
	}
	return lines
}

// Idle releases an incomplete line only after its bytes have cleared masking.
// A suffix that might complete a secret remains in the underlying Stream.
func (s *TextStream) Idle() string {
	cut := len(s.held)
	for at := max(0, cut-utf8.UTFMax+1); at < len(s.held); at++ {
		if utf8.RuneStart(s.held[at]) && !utf8.FullRuneInString(s.held[at:]) {
			cut = at
			break
		}
	}
	text := s.held[:cut]
	s.held = s.held[cut:]
	return text
}

// LogStreams decodes child stream payloads before masking and building log envelopes.
// One caller serializes access for the process or in-process test host.
type LogStreams struct {
	masker  *Masker
	streams map[string]*logStream
}

type logStream struct {
	lines *TextStream
	frame nodeproto.FrameLog
}

func NewLogStreams(masker *Masker) *LogStreams {
	return &LogStreams{masker: masker, streams: map[string]*logStream{}}
}

func (s *LogStreams) Push(frame nodeproto.FrameLog) ([]nodeproto.FrameLog, error) {
	if frame.Stream == "" {
		if frame.StreamData != "" || frame.StreamEnd {
			return nil, errors.New("log stream data requires a stream identity")
		}
		return []nodeproto.FrameLog{s.masker.Log(frame)}, nil
	}
	if frame.Message != "" || len(frame.Attrs) > 0 {
		return nil, errors.New("log stream cannot contain a presentation payload")
	}
	data, err := base64.StdEncoding.DecodeString(frame.StreamData)
	if err != nil {
		return nil, errors.New("log stream data is not base64")
	}
	return s.Text("sdk:"+frame.Stream, frame, string(data), frame.StreamEnd), nil
}

func (s *LogStreams) Text(key string, frame nodeproto.FrameLog, chunk string, eof bool) []nodeproto.FrameLog {
	stream := s.streams[key]
	if stream == nil {
		frame.Stream, frame.StreamData, frame.StreamEnd = "", "", false
		stream = &logStream{lines: s.masker.lines(), frame: s.masker.Log(frame)}
		s.streams[key] = stream
	}
	if frame.TS != "" {
		stream.frame.TS = frame.TS
	}
	lines := stream.lines.Push(chunk, eof)
	frames := make([]nodeproto.FrameLog, 0, len(lines))
	for _, line := range lines {
		record := stream.frame
		record.Message = line
		frames = append(frames, s.masker.Log(record))
	}
	if eof {
		delete(s.streams, key)
	}
	return frames
}

func (s *LogStreams) Flush() []nodeproto.FrameLog {
	var frames []nodeproto.FrameLog
	for key, stream := range s.streams {
		frames = append(frames, s.Text(key, stream.frame, "", true)...)
	}
	return frames
}

// Buffered reports safe text waiting for a newline or an idle flush.
func (s *LogStreams) Buffered() bool {
	for _, stream := range s.streams {
		if stream.lines.held != "" {
			return true
		}
	}
	return false
}

// Idle flushes safe partial lines without ending a byte stream.
func (s *LogStreams) Idle() []nodeproto.FrameLog {
	var frames []nodeproto.FrameLog
	for _, stream := range s.streams {
		if text := stream.lines.Idle(); text != "" {
			frame := stream.frame
			frame.Message = text
			frames = append(frames, s.masker.Log(frame))
		}
	}
	return frames
}
