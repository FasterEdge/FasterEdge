package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
)

// dockerUnixTransport 通过 unix socket 直连宿主 Docker daemon (Docker Engine API)。
// endpoint 形如 unix:///var/run/docker.sock。
type dockerUnixTransport struct {
	endpoint string
	client   *http.Client
}

func newDockerTransport(endpoint string) (*dockerUnixTransport, error) {
	if !strings.HasPrefix(endpoint, "unix://") {
		return nil, fmt.Errorf("unsupported endpoint %q (仅支持 unix://)", endpoint)
	}
	sockPath := strings.TrimPrefix(endpoint, "unix://")
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	return &dockerUnixTransport{endpoint: endpoint, client: &http.Client{
		Transport: tr,
		// daemon 卡死时旧实现无限阻塞(无客户端超时, main.go 传入的 5s
		// 仅成为 API 查询参数); 30s 与 CmdAbility 命令超时基线一致。
		Timeout: 30 * time.Second,
	}}, nil
}

// do 发送 Docker Engine API 请求并返回响应体; 非 2xx 返回带状态码的错误。
func (t *dockerUnixTransport) do(method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, "http://docker"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 响应体限 1MiB: 旧实现 io.ReadAll 无界, 异常 daemon 响应可撑爆内存。
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("docker api %s %s: response exceeds 1MiB", method, path)
	}
	if resp.StatusCode >= 300 && resp.StatusCode != 304 {
		return nil, fmt.Errorf("docker api %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

type dockerContainerListEntry struct {
	ID      string   `json:"Id"`
	Names   []string `json:"Names"`
	Image   string   `json:"Image"`
	State   string   `json:"State"`
	Status  string   `json:"Status"`
	Created int64    `json:"Created"`
}

type dockerContainerInspect struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Created string `json:"Created"`
	Config  struct {
		Image string `json:"Image"`
	} `json:"Config"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
}

func (t *dockerUnixTransport) List(all bool) ([]ability.DockerContainer, error) {
	query := "?all=0"
	if all {
		query = "?all=1"
	}
	data, err := t.do("GET", "/containers/json"+query, nil)
	if err != nil {
		return nil, err
	}
	var entries []dockerContainerListEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	out := make([]ability.DockerContainer, 0, len(entries))
	for _, e := range entries {
		name := ""
		if len(e.Names) > 0 {
			name = strings.TrimPrefix(e.Names[0], "/")
		}
		out = append(out, ability.DockerContainer{
			ID:      e.ID[:min(len(e.ID), 12)],
			Name:    name,
			Image:   e.Image,
			State:   e.State,
			Status:  e.Status,
			Created: time.Unix(e.Created, 0),
		})
	}
	return out, nil
}

func (t *dockerUnixTransport) Start(idOrName string) error {
	_, err := t.do("POST", "/containers/"+idOrName+"/start", nil)
	return err
}

func (t *dockerUnixTransport) Stop(idOrName string, timeout time.Duration) error {
	secs := int(timeout.Seconds())
	if secs <= 0 {
		secs = 10
	}
	_, err := t.do("POST", "/containers/"+idOrName+"/stop?t="+strconv.Itoa(secs), nil)
	return err
}

func (t *dockerUnixTransport) Restart(idOrName string, timeout time.Duration) error {
	secs := int(timeout.Seconds())
	if secs <= 0 {
		secs = 10
	}
	_, err := t.do("POST", "/containers/"+idOrName+"/restart?t="+strconv.Itoa(secs), nil)
	return err
}

func (t *dockerUnixTransport) Remove(idOrName string, force bool) error {
	forceFlag := 0
	if force {
		forceFlag = 1
	}
	_, err := t.do("DELETE", "/containers/"+idOrName+"?force="+strconv.Itoa(forceFlag), nil)
	return err
}

func (t *dockerUnixTransport) Pull(reference string) error {
	_, err := t.do("POST", "/images/create?fromImage="+reference, nil)
	return err
}

func (t *dockerUnixTransport) Inspect(idOrName string) (ability.DockerContainer, error) {
	data, err := t.do("GET", "/containers/"+idOrName+"/json", nil)
	if err != nil {
		return ability.DockerContainer{}, err
	}
	var e dockerContainerInspect
	if err := json.Unmarshal(data, &e); err != nil {
		return ability.DockerContainer{}, err
	}
	created := time.Time{}
	if parsed, err := time.Parse(time.RFC3339Nano, e.Created); err == nil {
		created = parsed
	}
	return ability.DockerContainer{
		ID:      e.ID[:min(len(e.ID), 12)],
		Name:    strings.TrimPrefix(e.Name, "/"),
		Image:   e.Config.Image,
		State:   e.State.Status,
		Status:  e.State.Status,
		Created: created,
	}, nil
}

// Logs 请求 stdout+stderr; 非 tty 容器返回的日志流是 8 字节帧头 + 负载的
// 多路复用格式 (Docker Engine API 规范), 需要拆帧。
func (t *dockerUnixTransport) Logs(idOrName string, tail int) (string, error) {
	if tail <= 0 {
		tail = 100
	}
	data, err := t.do("GET", fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&tail=%d", idOrName, tail), nil)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for off := 0; off+8 <= len(data); {
		frameLen := int(binary.BigEndian.Uint32(data[off+4 : off+8]))
		if frameLen < 0 || off+8+frameLen > len(data) {
			break // 非帧格式, 直接整体返回
		}
		sb.Write(data[off+8 : off+8+frameLen])
		off += 8 + frameLen
	}
	if sb.Len() == 0 {
		return string(data), nil
	}
	return sb.String(), nil
}

type dockerCreateBody struct {
	Image        string                 `json:"Image"`
	Cmd          []string               `json:"Cmd"`
	Env          []string               `json:"Env"`
	ExposedPorts map[string]struct{}    `json:"ExposedPorts"`
	HostConfig   dockerCreateHostConfig `json:"HostConfig"`
}

type dockerCreateHostConfig struct {
	PortBindings map[string][]dockerPortBinding `json:"PortBindings"`
}

type dockerPortBinding struct {
	HostPort string `json:"HostPort"`
}

func (t *dockerUnixTransport) Create(args ability.DockerCreateArgs) (string, error) {
	body := dockerCreateBody{
		Image:        args.Image,
		Cmd:          args.Command,
		Env:          args.Env,
		ExposedPorts: map[string]struct{}{},
		HostConfig:   dockerCreateHostConfig{PortBindings: map[string][]dockerPortBinding{}},
	}
	for _, spec := range args.Ports {
		// 支持 "8080:80" 与 "80"
		parts := strings.Split(spec, ":")
		containerPort := parts[len(parts)-1]
		key := containerPort + "/tcp"
		body.ExposedPorts[key] = struct{}{}
		if len(parts) == 2 {
			body.HostConfig.PortBindings[key] = []dockerPortBinding{{HostPort: parts[0]}}
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	path := "/containers/create"
	if args.Name != "" {
		path += "?name=" + args.Name
	}
	data, err := t.do("POST", path, payload)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

// min 供 Go < 1.21 的内建 min 不存在时兜底; 容器镜像为 go1.25 时直接用内建。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
