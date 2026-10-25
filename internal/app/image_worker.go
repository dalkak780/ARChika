package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

type imageRequest struct {
	Transform Transform `json:"transform"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	MaxPixels int64     `json:"max_pixels"`
	Pure      bool      `json:"pure"`
}
type imageResponse struct {
	Size     int64  `json:"size"`
	MIME     string `json:"mime"`
	Still    bool   `json:"still"`
	Error    string `json:"error,omitempty"`
	Backend  string `json:"backend"`
	CPUUS    int64  `json:"cpu_us"`
	PeakRSS  int64  `json:"peak_rss_bytes"`
	Duration int64  `json:"duration_ns"`
}

func ImageMain() error {
	if e := limitHelper(); e != nil {
		return e
	}
	reader := bufio.NewReader(os.Stdin)
	line, e := readLine(reader, 4096)
	if e != nil {
		return e
	}
	var request imageRequest
	if e = json.Unmarshal(line, &request); e != nil {
		return e
	}
	if request.Size < 1 || request.Size > 512<<20 || request.MaxPixels < 1 || request.MaxPixels > 256_000_000 {
		return errors.New("invalid image limits")
	}
	b := make([]byte, request.Size)
	if _, e = io.ReadFull(reader, b); e != nil {
		return e
	}
	processor := newImageProcessor()
	if request.Pure {
		processor = GoProcessor{}
	}
	start := time.Now()
	var v PageValue
	if sniffMIME(b, "") == "" {
		e = errUnsupportedTransform
	} else {
		v, e = processor.Process(context.Background(), PageValue{Bytes: b, MIME: request.MIME}, request.Transform, request.MaxPixels)
	}
	cpu, rss := helperUsage()
	res := imageResponse{Size: int64(len(v.Bytes)), MIME: v.MIME, Still: v.Still, Backend: processor.Version(), CPUUS: cpu, PeakRSS: rss, Duration: time.Since(start).Nanoseconds()}
	if e != nil {
		res.Error = "unsupported or resource-limited transformation"
		res.Size = 0
	}
	if e = json.NewEncoder(os.Stdout).Encode(res); e != nil {
		return e
	}
	if res.Size > 0 {
		_, e = os.Stdout.Write(v.Bytes)
	}
	return e
}
func processImage(ctx context.Context, input PageValue, t Transform, maxPixels, maxBytes int64, pure bool) (PageValue, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	exe, e := os.Executable()
	if e != nil {
		return PageValue{}, e
	}
	cmd := exec.CommandContext(ctx, exe, "image")
	cmd.Env = []string{"GOMEMLIMIT=256MiB", "GOMAXPROCS=2", "PATH=/usr/bin:/bin", "VIPS_CONCURRENCY=1"}
	cmd.Stderr = io.Discard
	in, e := cmd.StdinPipe()
	if e != nil {
		return PageValue{}, e
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		return PageValue{}, e
	}
	if e = cmd.Start(); e != nil {
		return PageValue{}, e
	}
	writeDone := make(chan error, 1)
	go func() {
		e := json.NewEncoder(in).Encode(imageRequest{Transform: t, MIME: input.MIME, Size: int64(len(input.Bytes)), MaxPixels: maxPixels, Pure: pure})
		if e == nil {
			_, e = in.Write(input.Bytes)
		}
		_ = in.Close()
		writeDone <- e
	}()
	reader := bufio.NewReader(out)
	line, e := readLine(reader, 4096)
	var res imageResponse
	if e == nil {
		e = json.Unmarshal(line, &res)
	}
	var b []byte
	if e == nil && res.Size > 0 && res.Size <= maxBytes {
		b = make([]byte, res.Size)
		_, e = io.ReadFull(reader, b)
	} else if e == nil {
		e = errUnsupportedTransform
	}
	if e != nil {
		helperLog(ctx).Warn("image process rejected", "error", e.Error())
		_ = cmd.Process.Kill()
	}
	we := <-writeDone
	waitErr := cmd.Wait()
	if waitErr != nil {
		helperLog(ctx).Warn("image process failed", "error", waitErr.Error())
	}
	if ctx.Err() != nil {
		helperLog(ctx).Warn("image process timed out or cancelled", "error", ctx.Err().Error())
	}
	if e == nil {
		e = we
	}
	if e == nil {
		e = waitErr
	}
	if e != nil || res.Error != "" {
		return PageValue{}, errUnsupportedTransform
	}
	return PageValue{Bytes: b, MIME: res.MIME, Still: res.Still}, nil
}
