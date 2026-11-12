package inspect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

var contentRange = regexp.MustCompile(`^bytes \d+-\d+/(\d+)$`)

// Probe classifies the file at url from its first HeaderSize bytes (a Range request; servers that
// ignore Range are read only that far) and the size the server states. It runs in the probe Job,
// whose network policy only allows the internet.
func Probe(ctx context.Context, url string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", HeaderSize-1))
	req.Header.Set("User-Agent", "vmservice-inspect")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("downloading: %w", err)
	}
	defer resp.Body.Close()
	var total int64
	switch resp.StatusCode {
	case http.StatusPartialContent:
		m := contentRange.FindStringSubmatch(resp.Header.Get("Content-Range"))
		if m == nil {
			return Result{}, errors.New("the server didn't say the file's size")
		}
		total, _ = strconv.ParseInt(m[1], 10, 64)
	case http.StatusOK:
		total = resp.ContentLength
	default:
		return Result{}, fmt.Errorf("the server answered %s", resp.Status)
	}
	if total <= 0 {
		return Result{}, errors.New("the server didn't say the file's size")
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, HeaderSize))
	if err != nil {
		return Result{}, fmt.Errorf("downloading: %w", err)
	}
	return Header(head, total)
}
