package sandbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ManagedEngine separates transport observation from command lifetime. Detaching
// Attach or Inspect never kills a container. Only Kill is an execution effect.
type ManagedEngine interface {
	Create(context.Context, Job) (string, error)
	StartContainer(context.Context, string) error
	Inspect(context.Context, string) (ContainerState, error)
	Attach(context.Context, string, bool) (io.ReadCloser, error)
	Kill(context.Context, string) error
	Remove(context.Context, string) error
}

// TimestampedEngine supplies daemon-origin record identities. Child text alone
// is insufficient to recognize rotation when the command repeats its output.
type TimestampedEngine interface {
	AttachTimestamped(context.Context, string) (io.ReadCloser, error)
	ReleaseOutput(context.Context, string) error
}

func (d *Docker) AttachTimestamped(ctx context.Context, id string) (io.ReadCloser, error) {
	res, e := d.request(ctx, "GET", "/containers/"+url.PathEscape(id)+"/logs?follow=1&stdout=1&stderr=1&timestamps=1", nil)
	if e != nil {
		return nil, e
	}
	return res.Body, nil
}
func (d *Docker) ReleaseOutput(ctx context.Context, id string) error {
	res, e := d.request(ctx, "POST", "/containers/"+url.PathEscape(id)+"/attach?logs=0&stream=1&stdin=1&stdout=0&stderr=0", nil)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	w, ok := res.Body.(io.Writer)
	if !ok {
		return errors.New("Docker stdin upgrade unavailable")
	}
	_, e = io.WriteString(w, "go\n")
	return e
}

var errContainerMissing = errors.New("managed container does not exist")

type ContainerState struct {
	ID     string `json:"Id"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		ExitCode  int32  `json:"ExitCode"`
		OOMKilled bool   `json:"OOMKilled"`
		Error     string `json:"Error"`
	} `json:"State"`
}

func (d *Docker) Create(ctx context.Context, j Job) (string, error) {
	res, e := d.request(ctx, "POST", "/containers/create?name="+url.QueryEscape(j.Name), containerConfig(j))
	if e != nil {
		return "", e
	}
	defer res.Body.Close()
	var obj struct{ ID string }
	if json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&obj) != nil || obj.ID == "" {
		return "", errors.New("container creation failed")
	}
	return obj.ID, nil
}
func (d *Docker) StartContainer(ctx context.Context, id string) error {
	res, e := d.request(ctx, "POST", "/containers/"+url.PathEscape(id)+"/start", nil)
	if e == nil {
		res.Body.Close()
	}
	return e
}
func (d *Docker) Inspect(ctx context.Context, id string) (ContainerState, error) {
	var obj ContainerState
	req, e := http.NewRequestWithContext(ctx, "GET", "http://docker"+d.prefix+"/containers/"+url.PathEscape(id)+"/json", nil)
	if e != nil {
		return obj, e
	}
	res, e := d.HTTP.Do(req)
	if e != nil {
		return obj, errors.New("Docker API unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return obj, errContainerMissing
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 262144)).Decode(&obj) != nil || obj.ID == "" {
		return obj, errors.New("container inspect failed")
	}
	return obj, nil
}
func (d *Docker) Attach(ctx context.Context, id string, logs bool) (io.ReadCloser, error) {
	path := "/containers/" + url.PathEscape(id) + "/attach?logs=0&stream=1&stdout=1&stderr=1"
	if logs {
		path = "/containers/" + url.PathEscape(id) + "/logs?follow=1&stdout=1&stderr=1"
	}
	method := "POST"
	if logs {
		method = "GET"
	}
	res, e := d.request(ctx, method, path, nil)
	if e != nil {
		return nil, e
	}
	return res.Body, nil
}
func (d *Docker) Kill(ctx context.Context, id string) error {
	res, e := d.request(ctx, "POST", "/containers/"+url.PathEscape(id)+"/kill?signal=KILL", nil)
	if e == nil {
		res.Body.Close()
	}
	return e
}
func (d *Docker) Remove(ctx context.Context, id string) error {
	return d.remove(ctx, url.PathEscape(id))
}

// Each output snapshot is bounded and fsynced by the trusted supervisor. The
// child can neither read nor alter it. Recovery replays Docker's bounded log and
// replaces snapshots, so transport reconnection cannot duplicate output.
type outputPart struct {
	Stream byte   `json:"stream"`
	Data   string `json:"data"`
}

// Persist raw output bytes losslessly even when a command writes non-UTF8 data.
func (p outputPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Stream byte   `json:"stream"`
		Data   []byte `json:"dataBase64"`
	}{p.Stream, []byte(p.Data)})
}
func (p *outputPart) UnmarshalJSON(b []byte) error {
	var wire struct {
		Stream byte   `json:"stream"`
		Data   []byte `json:"dataBase64"`
		Legacy string `json:"data"`
	}
	if e := json.Unmarshal(b, &wire); e != nil {
		return e
	}
	p.Stream, p.Data = wire.Stream, string(wire.Data)
	if wire.Data == nil {
		p.Data = wire.Legacy
	}
	return nil
}
func flattenOutput(parts []outputPart) (string, string) {
	var values, streams strings.Builder
	for _, part := range parts {
		values.WriteString(part.Data)
		streams.WriteString(strings.Repeat(string([]byte{part.Stream}), len(part.Data)))
	}
	return values.String(), streams.String()
}

func captureFrames(r io.Reader, persist func(Result, []outputPart) error) (result Result, err error) {
	var header [8]byte
	var parts []outputPart
	for {
		_, e := io.ReadFull(r, header[:])
		if e == io.EOF {
			return result, nil
		}
		if e != nil {
			return result, errors.New("invalid container output")
		}
		if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return result, errors.New("invalid container stream")
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if size > 16*1024*1024 {
			return result, errors.New("container output frame exceeded limits")
		}
		keep := int64(OutputLimit - len(result.Stdout) - len(result.Stderr))
		if keep > size {
			keep = size
		}
		data := make([]byte, keep)
		if _, e = io.ReadFull(r, data); e != nil {
			return result, e
		}
		if header[0] == 1 {
			result.Stdout += string(data)
		} else {
			result.Stderr += string(data)
		}
		if len(data) > 0 {
			parts = append(parts, outputPart{header[0], string(data)})
		}
		result.Truncated = size > keep
		if e = persist(result, parts); e != nil {
			return result, e
		}
		if result.Truncated {
			return result, nil
		}
	}
}
func operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
