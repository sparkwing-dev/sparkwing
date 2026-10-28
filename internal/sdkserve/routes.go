package sdkserve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type routeServices struct {
	ctx       context.Context
	binding   *nodeproto.ProcessBinding
	emit      func(nodeproto.FrameLog)
	transport http.RoundTripper
}

func routeSegment(id string) string {
	// safety: escaped dots keep opaque identifiers out of the router's path cleaning.
	return strings.ReplaceAll(url.PathEscape(id), ".", "%2E")
}

func (s *routeServices) call(ctx context.Context, method, path string, body, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.binding == nil {
		return fmt.Errorf("host services require an execution binding")
	}
	u, err := url.Parse(s.binding.Routes.URL)
	if err != nil {
		return fmt.Errorf("invalid node-host URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("node-host URL must be a loopback HTTP origin")
	}
	raw, err := EncodeServiceRequest(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(u.String(), "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.binding.Routes.Bearer)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Transport: s.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limits.NodeOutputBytes+limits.ProtocolEnvelopeBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limits.NodeOutputBytes+limits.ProtocolEnvelopeBytes {
		return fmt.Errorf("host response exceeds output limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return fmt.Errorf("invalid host error response")
		}
		e, err := apierr.DecodeInner(envelope.Error)
		if err != nil {
			return err
		}
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (s *routeServices) Secret(ctx context.Context, name string) (string, bool, error) {
	var out struct {
		Value  *string `json:"value"`
		Masked *bool   `json:"masked"`
	}
	if err := s.call(ctx, "GET", "/node/v1/secrets/"+routeSegment(name), nil, &out); err != nil {
		return "", false, err
	}
	if out.Value == nil || out.Masked == nil {
		return "", false, fmt.Errorf("host secret response needs value and masking classification")
	}
	return *out.Value, *out.Masked, nil
}

func (s *routeServices) Output(ctx context.Context, job string) (json.RawMessage, error) {
	var out struct {
		Output json.RawMessage `json:"output"`
	}
	err := s.call(ctx, "GET", "/node/v1/jobs/"+routeSegment(job)+"/output", nil, &out)
	if err == nil && out.Output == nil {
		err = fmt.Errorf("host output response needs output")
	}
	return out.Output, err
}

func (s *routeServices) Latest(ctx context.Context, pipeline, job string, age time.Duration) (json.RawMessage, error) {
	var out struct {
		Output json.RawMessage `json:"output"`
	}
	err := s.call(ctx, "GET", "/node/v1/pipelines/"+routeSegment(pipeline)+"/latest?job="+url.QueryEscape(job)+"&max_age_ms="+strconv.FormatInt(age.Milliseconds(), 10), nil, &out)
	if err == nil && out.Output == nil {
		err = fmt.Errorf("host reference response needs output")
	}
	return out.Output, err
}

func (s *routeServices) RunPipeline(ctx context.Context, request ChildRequest) (json.RawMessage, error) {
	var started struct {
		RunID string `json:"run_id"`
	}
	if err := s.call(ctx, "POST", "/node/v1/children", request, &started); err != nil {
		return nil, err
	}
	if started.RunID == "" {
		return nil, fmt.Errorf("child start returned no run ID")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var state struct {
			Status string          `json:"status"`
			Output json.RawMessage `json:"output"`
			Error  json.RawMessage `json:"error"`
		}
		path := "/node/v1/children/" + routeSegment(started.RunID) + "/await?job=" + url.QueryEscape(request.Job) + "&wait_ms=" + strconv.FormatInt(limits.ChildAwait.Milliseconds(), 10)
		if err := s.call(ctx, "GET", path, nil, &state); err != nil {
			return nil, err
		}
		switch state.Status {
		case "success", "memoized":
			if state.Output == nil {
				return nil, fmt.Errorf("child success response needs output")
			}
			return state.Output, nil
		case "pending", "running", "queued":
			continue
		case "failed", "cancelled", "skipped", "superseded":
			if state.Error != nil {
				failure, err := apierr.DecodeInner(state.Error)
				if err != nil {
					return nil, err
				}
				return nil, failure
			}
			return nil, fmt.Errorf("child run ended %s", state.Status)
		default:
			return nil, fmt.Errorf("invalid child state")
		}
	}
}

func (s *routeServices) OIDC(ctx context.Context, audience string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := s.call(ctx, "POST", "/node/v1/oidc-token", map[string]string{"audience": audience}, &out)
	if err == nil && out.Token == "" {
		err = fmt.Errorf("host OIDC response needs a token")
	}
	return out.Token, err
}

func (s *routeServices) acknowledge(ctx context.Context, path string, request any) error {
	var ack map[string]json.RawMessage
	if err := s.call(ctx, "POST", path, request, &ack); err != nil {
		return err
	}
	if ack == nil {
		return fmt.Errorf("host acknowledgement must be an object")
	}
	return nil
}

func (s *routeServices) Mask(ctx context.Context, value string) error {
	return s.acknowledge(ctx, "/node/v1/masks", nodeproto.MaskRequest{Value: value})
}

func (s *routeServices) Emit(r nodeproto.FrameLog) {
	var err error
	switch r.Event {
	case "annotation":
		request := nodeproto.AnnotationRequest{Annotation: nodeproto.Annotation{Level: r.Level, Message: r.Message}, Step: r.Step}
		if len(r.Attrs) != 0 {
			err = json.Unmarshal(r.Attrs, &request)
		}
		if err == nil {
			err = s.acknowledge(s.ctx, "/node/v1/annotations", request)
		}
	case "summary":
		err = s.acknowledge(s.ctx, "/node/v1/summary", nodeproto.SummaryRequest{Markdown: r.Message, Step: r.Step})
	default:
		s.emit(r)
	}
	if err != nil {
		panic(err)
	}
}

func (s *routeServices) RegisterCleanup(ctx context.Context, argv []string, description string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := s.call(ctx, "POST", "/node/v1/cleanups", map[string]any{"argv": argv, "description": description}, &out)
	if err == nil && out.ID == "" {
		err = fmt.Errorf("host cleanup response needs an id")
	}
	return out.ID, err
}

func (s *routeServices) ReleaseCleanup(ctx context.Context, id string) error {
	return s.call(ctx, "DELETE", "/node/v1/cleanups/"+routeSegment(id), nil, nil)
}
