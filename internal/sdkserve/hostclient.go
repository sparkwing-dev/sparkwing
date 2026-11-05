package sdkserve

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

type hostResponse struct {
	status int
	body   []byte
}

type dialFunc func(ctx context.Context, address string) (net.Conn, error)

func dialLoopback(ctx context.Context, address string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", address)
}

// perf: a pipeline that imports net/http pays 7-15s more on an uncached compile; the node host
// speaks HTTP/1.1 over loopback, so one request per connection needs only this much of it.
func roundTrip(ctx context.Context, dial dialFunc, host, method, target, bearer string, body []byte, limit int64) (hostResponse, error) {
	if !validRequestToken(method) || !validRequestText(target) || !validRequestText(bearer) {
		return hostResponse{}, errors.New("node-host request has an invalid method, path or bearer")
	}
	conn, err := dial(ctx, host)
	if err != nil {
		return hostResponse{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() }) //nolint:errcheck // closing only unblocks the exchange; the context error is reported
	response, err := exchange(conn, host, method, target, bearer, body, limit)
	stop()
	_ = conn.Close() //nolint:errcheck // the reply is complete once read; a close failure cannot change it
	if ctxErr := ctx.Err(); err != nil && ctxErr != nil {
		return hostResponse{}, ctxErr
	}
	return response, err
}

func exchange(conn net.Conn, host, method, target, bearer string, body []byte, limit int64) (hostResponse, error) {
	var request bytes.Buffer
	fmt.Fprintf(&request, "%s %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", method, target, host, bearer, len(body))
	request.Write(body)
	if _, err := conn.Write(request.Bytes()); err != nil {
		return hostResponse{}, err
	}
	reader := bufio.NewReader(conn)
	for {
		status, header, err := readHead(reader)
		if err != nil {
			return hostResponse{}, err
		}
		// safety: an interim 1xx reply precedes the final one on the same connection.
		if status >= 100 && status < 200 {
			continue
		}
		data, err := readBody(reader, header, limit)
		return hostResponse{status: status, body: data}, err
	}
}

const maxHeadBytes = 64 << 10

func readHead(reader *bufio.Reader) (int, map[string]string, error) {
	budget := maxHeadBytes
	line, err := readLine(reader, &budget)
	if err != nil {
		return 0, nil, err
	}
	version, rest, _ := strings.Cut(line, " ")
	code, _, _ := strings.Cut(rest, " ")
	status, err := strconv.Atoi(code)
	if !strings.HasPrefix(version, "HTTP/1.") || len(code) != 3 || err != nil {
		return 0, nil, errors.New("invalid node-host response status line")
	}
	header := map[string]string{}
	for {
		line, err := readLine(reader, &budget)
		if err != nil {
			return 0, nil, err
		}
		if line == "" {
			return status, header, nil
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return 0, nil, errors.New("invalid node-host response header")
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if previous, seen := header[name]; seen {
			value = previous + ", " + value
		}
		header[name] = value
	}
}

func readLine(reader *bufio.Reader, budget *int) (string, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		*budget -= len(chunk)
		if *budget < 0 {
			return "", errors.New("node-host response header exceeds its limit")
		}
		line = append(line, chunk...)
		if err == nil {
			return strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r"), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return "", err
		}
	}
}

var errResponseLimit = errors.New("host response exceeds output limit")

func readBody(reader *bufio.Reader, header map[string]string, limit int64) ([]byte, error) {
	if encoding := strings.ToLower(header["transfer-encoding"]); encoding != "" {
		if encoding != "chunked" {
			return nil, fmt.Errorf("unsupported node-host transfer encoding %q", encoding)
		}
		return readChunked(reader, limit)
	}
	if raw, ok := header["content-length"]; ok {
		length, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || length < 0 {
			return nil, errors.New("invalid node-host response length")
		}
		if length > limit {
			return nil, errResponseLimit
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		return data, nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errResponseLimit
	}
	return data, err
}

func readChunked(reader *bufio.Reader, limit int64) ([]byte, error) {
	var data []byte
	for {
		budget := maxHeadBytes
		line, err := readLine(reader, &budget)
		if err != nil {
			return nil, err
		}
		sizeText, _, _ := strings.Cut(line, ";")
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 16, 64)
		if err != nil || size < 0 {
			return nil, errors.New("invalid node-host response chunk")
		}
		if size == 0 {
			for {
				trailer, err := readLine(reader, &budget)
				if err != nil {
					return nil, err
				}
				if trailer == "" {
					return data, nil
				}
			}
		}
		if int64(len(data))+size > limit {
			return nil, errResponseLimit
		}
		start := len(data)
		data = append(data, make([]byte, size)...)
		if _, err := io.ReadFull(reader, data[start:]); err != nil {
			return nil, err
		}
		if line, err := readLine(reader, &budget); err != nil || line != "" {
			return nil, errors.New("invalid node-host response chunk")
		}
	}
}

func validRequestToken(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range []byte(value) {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func validRequestText(value string) bool {
	for _, c := range []byte(value) {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}
