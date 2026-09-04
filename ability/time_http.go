// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var ErrHTTPSDowngrade = errors.New("HTTPS redirect downgrade is disallowed")
var ErrTimeResponseTooLarge = errors.New("time response exceeds configured limit")

func (t *TimeAbility) fetchNetworkTime(ctx context.Context, rawURL string) (time.Time, error) {
	u, err := validateTimeURL(rawURL)
	if err != nil {
		return time.Time{}, err
	}
	p := t.networkPolicy
	tr := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second, ExpectContinueTimeout: time.Second}
	tr.DialContext = p.dialContext(&net.Dialer{})
	client := &http.Client{Transport: tr, Timeout: t.httpTimeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return http.ErrUseLastResponse
		}
		prev := via[len(via)-1].URL
		if prev.Scheme == "https" && req.URL.Scheme == "http" {
			return ErrHTTPSDowngrade
		}
		_, e := validateTimeURL(req.URL.String())
		if e != nil {
			return e
		}
		if _, e = p.resolve(req.Context(), req.URL.Hostname()); e != nil {
			return e
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("time request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("time request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Time{}, fmt.Errorf("time source status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, t.maxResponseBytes+1))
	if err != nil {
		return time.Time{}, fmt.Errorf("time response: %w", err)
	}
	if int64(len(body)) > t.maxResponseBytes {
		return time.Time{}, ErrTimeResponseTooLarge
	}
	var pld struct {
		DateTime string `json:"dateTime"`
		Upper    string `json:"DateTime"`
	}
	if err := json.Unmarshal(body, &pld); err != nil {
		return time.Time{}, fmt.Errorf("time response JSON: %w", err)
	}
	if pld.DateTime == "" {
		pld.DateTime = pld.Upper
	}
	if strings.TrimSpace(pld.DateTime) == "" {
		return time.Time{}, errors.New("datetime not found")
	}
	ts, err := time.Parse(time.RFC3339Nano, pld.DateTime)
	if err != nil {
		// timeapi.io 等来源返回无时区后缀的 UTC datetime
		// (如 "2026-09-04T12:28:07.3240057"), RFC3339 解析失败;
		// 回退按无时区格式解析并视为 UTC。
		if tsNoZone, err2 := time.Parse("2006-01-02T15:04:05.999999999", pld.DateTime); err2 == nil {
			return tsNoZone.UTC(), nil
		}
		return time.Time{}, fmt.Errorf("datetime parse: %w", err)
	}
	return ts, nil
}
