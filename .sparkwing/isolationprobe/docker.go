package isolationprobe

import (
	"archive/tar"
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type dockerClient struct {
	client *http.Client
}

func newDocker(socket string) *dockerClient {
	dialer := &net.Dialer{}
	return &dockerClient{client: &http.Client{Transport: &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
	}}}
}

type hostConfig struct {
	Binds        []string `json:"Binds,omitempty"`
	NetworkMode  string   `json:"NetworkMode,omitempty"`
	Privileged   bool     `json:"Privileged,omitempty"`
	PidMode      string   `json:"PidMode,omitempty"`
	IpcMode      string   `json:"IpcMode,omitempty"`
	UTSMode      string   `json:"UTSMode,omitempty"`
	CgroupnsMode string   `json:"CgroupnsMode,omitempty"`
	CapAdd       []string `json:"CapAdd,omitempty"`
	SecurityOpt  []string `json:"SecurityOpt,omitempty"`
}

type containerConfig struct {
	Image      string     `json:"Image"`
	Cmd        []string   `json:"Cmd"`
	HostConfig hostConfig `json:"HostConfig"`
}

type dockerError struct {
	status  int
	message string
}

func (e *dockerError) Error() string { return fmt.Sprintf("docker: HTTP %d: %s", e.status, e.message) }

func (d *dockerClient) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var msg struct {
			Message string `json:"message"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10)) //nolint:errcheck // a best-effort message
		if json.Unmarshal(data, &msg) != nil || msg.Message == "" {
			msg.Message = strings.TrimSpace(string(data))
		}
		return nil, &dockerError{status: resp.StatusCode, message: msg.Message}
	}
	return resp, nil
}

func (d *dockerClient) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(data), "application/json"
	}
	resp, err := d.do(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d *dockerClient) ping(ctx context.Context) error {
	return d.call(ctx, http.MethodGet, "/_ping", nil, nil)
}

const probeBinary = "isolation-probe"

// safety: the image is imported from a tarball holding only the probe's own static executable, so
// no builder runs, nothing is pulled from a registry and the containers run exactly the code tested.
func (d *dockerClient) importImage(ctx context.Context, repo, tag string, executable []byte) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: probeBinary, Mode: 0o755, Size: int64(len(executable)), ModTime: time.Unix(0, 0)}); err != nil {
		return err
	}
	if _, err := tw.Write(executable); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	resp, err := d.do(ctx, http.MethodPost, "/images/create?fromSrc=-&repo="+url.QueryEscape(repo)+"&tag="+url.QueryEscape(tag), &buf, "application/x-tar")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("read import stream: %w", err)
		}
		if msg.Error != "" {
			return fmt.Errorf("docker import: %s", msg.Error)
		}
	}
}

func (d *dockerClient) create(ctx context.Context, name string, cfg containerConfig) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	if err := d.call(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), cfg, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (d *dockerClient) start(ctx context.Context, id string) error {
	return d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
}

func (d *dockerClient) wait(ctx context.Context, id string) (int, error) {
	var out struct {
		StatusCode int `json:"StatusCode"`
	}
	err := d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/wait", nil, &out)
	return out.StatusCode, err
}

// safety: container output is the probe's own lines, read for diagnosis only and capped so a
// runaway container cannot exhaust the job's memory.
func (d *dockerClient) logs(ctx context.Context, id string) (string, error) {
	resp, err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs?stdout=1&stderr=1&tail=200", nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return demuxLogs(io.LimitReader(resp.Body, 256<<10))
}

// safety: the log stream is capped, so a frame cut short at the cap ends the logs rather than
// failing them.
func demuxLogs(r io.Reader) (string, error) {
	var out strings.Builder
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return out.String(), endOfStream(err)
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if _, err := io.CopyN(&out, r, size); err != nil {
			return out.String(), endOfStream(err)
		}
	}
}

func endOfStream(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}

func (d *dockerClient) remove(ctx context.Context, id string) error {
	return d.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1&v=1", nil, nil)
}

func (d *dockerClient) removeImage(ctx context.Context, tag string) error {
	return d.call(ctx, http.MethodDelete, "/images/"+url.PathEscape(tag)+"?force=1", nil, nil)
}

// safety: a FROM scratch image has no dynamic loader, so only an executable without PT_INTERP can
// run in it.
func static(executable []byte) (bool, error) {
	f, err := elf.NewFile(bytes.NewReader(executable))
	if err != nil {
		return false, err
	}
	defer f.Close() //nolint:errcheck // an in-memory reader
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return false, nil
		}
	}
	return true, nil
}
