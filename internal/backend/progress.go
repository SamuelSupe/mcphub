package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type progressRoundTripper struct {
	base   http.RoundTripper
	handle func(*mcp.ProgressNotificationParams)
}

func (t *progressRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" && response.Body != nil {
		response.Body = &progressBody{
			body:   response.Body,
			handle: t.handle,
		}
	}
	return response, nil
}

const maximumInspectedSSEEventBytes = 1 << 20

type progressBody struct {
	body      io.ReadCloser
	handle    func(*mcp.ProgressNotificationParams)
	event     []byte
	tail      [4]byte
	tailLen   int
	oversized bool
}

func (b *progressBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.inspectBytes(p[:n])
	}
	if err != nil && (len(b.event) > 0 || b.oversized) {
		if !b.oversized {
			b.inspect(b.event)
		}
		b.resetEvent()
	}
	return n, err
}

func (b *progressBody) Close() error {
	return b.body.Close()
}

func (b *progressBody) inspectBytes(data []byte) {
	for _, value := range data {
		if !b.oversized {
			if len(b.event) < maximumInspectedSSEEventBytes {
				b.event = append(b.event, value)
			} else {
				b.event = nil
				b.oversized = true
			}
		}
		b.pushTail(value)
		if b.hasEventBoundary() {
			if !b.oversized {
				b.inspect(b.event)
			}
			b.resetEvent()
		}
	}
}

func (b *progressBody) resetEvent() {
	b.event = nil
	b.tailLen = 0
	b.oversized = false
}

func (b *progressBody) pushTail(value byte) {
	if b.tailLen < len(b.tail) {
		b.tail[b.tailLen] = value
		b.tailLen++
		return
	}
	copy(b.tail[:], b.tail[1:])
	b.tail[len(b.tail)-1] = value
}

func (b *progressBody) hasEventBoundary() bool {
	if b.tailLen >= 2 && b.tail[b.tailLen-2] == '\n' && b.tail[b.tailLen-1] == '\n' {
		return true
	}
	return b.tailLen == len(b.tail) && b.tail == [4]byte{'\r', '\n', '\r', '\n'}
}

func (b *progressBody) inspect(event []byte) {
	data := sseData(event)
	if len(data) == 0 {
		return
	}
	message, err := jsonrpc.DecodeMessage(data)
	if err != nil {
		return
	}
	request, ok := message.(*jsonrpc.Request)
	if !ok || request.IsCall() || request.Method != "notifications/progress" {
		return
	}
	var params mcp.ProgressNotificationParams
	if err := json.Unmarshal(request.Params, &params); err == nil && b.handle != nil {
		// Inspect before the SDK sees the event. Its notification dispatcher is
		// asynchronous, so forwarding there can race a stateless response close.
		b.handle(&params)
	}
}

func sseData(event []byte) []byte {
	var data []byte
	for line := range bytes.Lines(event) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		value := bytes.TrimPrefix(line, []byte("data:"))
		value = bytes.TrimPrefix(value, []byte(" "))
		data = append(data, value...)
		data = append(data, '\n')
	}
	return bytes.TrimSuffix(data, []byte("\n"))
}
