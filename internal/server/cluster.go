package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"watermark-backend/internal/runtimecfg"
)

type clusterNodeInfo struct {
	ID                            string `json:"id"`
	Name                          string `json:"name"`
	Role                          string `json:"role"`
	Hostname                      string `json:"hostname"`
	AdvertiseAddr                 string `json:"advertiseAddr,omitempty"`
	StartedAt                     string `json:"startedAt"`
	UptimeSeconds                 int64  `json:"uptimeSeconds"`
	GoVersion                     string `json:"goVersion"`
	OS                            string `json:"os"`
	Arch                          string `json:"arch"`
	ParserEngine                  string `json:"parserEngine"`
	ParserFallback                bool   `json:"parserFallback"`
	ProxyConfigured               bool   `json:"proxyConfigured"`
	DownloadFallbackMode          string `json:"downloadFallbackMode"`
	HTTPTimeoutSeconds            int    `json:"httpTimeoutSeconds"`
	YTDLPTimeoutSeconds           int    `json:"ytdlpTimeoutSeconds"`
	UniversalParserTimeoutSeconds int    `json:"universalParserTimeoutSeconds"`
	MusicDLTimeoutSeconds         int    `json:"musicdlTimeoutSeconds"`
}

type clusterWorkerEndpoint struct {
	Name string
	URL  string
}

type clusterNodeStatus struct {
	clusterNodeInfo
	ConfigID       string               `json:"configId,omitempty"`
	Endpoint       string               `json:"endpoint,omitempty"`
	Enabled        bool                 `json:"enabled"`
	Disabled       bool                 `json:"disabled,omitempty"`
	Healthy        bool                 `json:"healthy"`
	Status         string               `json:"status"`
	Error          string               `json:"error,omitempty"`
	LatencyMS      int64                `json:"latencyMs,omitempty"`
	Infrastructure infrastructureStatus `json:"infrastructure,omitempty"`
}

type clusterStatus struct {
	Enabled       bool                `json:"enabled"`
	DispatchMode  string              `json:"dispatchMode"`
	Total         int                 `json:"total"`
	EnabledNodes  int                 `json:"enabledNodes"`
	DisabledNodes int                 `json:"disabledNodes"`
	Healthy       int                 `json:"healthy"`
	Nodes         []clusterNodeStatus `json:"nodes"`
}

func currentClusterNodeInfo() clusterNodeInfo {
	refreshSharedRuntimeSettings()
	hostname, _ := os.Hostname()
	hostname = strings.TrimSpace(hostname)
	settings := runtimecfg.Current()

	id := firstNonEmpty(os.Getenv("CLUSTER_NODE_ID"), hostname, "local")
	name := firstNonEmpty(os.Getenv("CLUSTER_NODE_NAME"), id)
	role := strings.ToLower(strings.TrimSpace(os.Getenv("CLUSTER_NODE_ROLE")))
	if role == "" {
		role = "standalone"
	}

	return clusterNodeInfo{
		ID:                            id,
		Name:                          name,
		Role:                          role,
		Hostname:                      hostname,
		AdvertiseAddr:                 strings.TrimSpace(os.Getenv("CLUSTER_ADVERTISE_ADDR")),
		StartedAt:                     adminStartedAt.Format(time.RFC3339),
		UptimeSeconds:                 int64(time.Since(adminStartedAt).Seconds()),
		GoVersion:                     runtime.Version(),
		OS:                            runtime.GOOS,
		Arch:                          runtime.GOARCH,
		ParserEngine:                  runtimecfg.ParserEngine(),
		ParserFallback:                settings.ParserFallbackEnabled,
		ProxyConfigured:               strings.TrimSpace(settings.OutboundProxy) != "",
		DownloadFallbackMode:          runtimecfg.DownloadFallbackMode(),
		HTTPTimeoutSeconds:            settings.HTTPTimeoutSeconds,
		YTDLPTimeoutSeconds:           settings.YTDLPTimeoutSeconds,
		UniversalParserTimeoutSeconds: settings.UniversalParserTimeoutSeconds,
		MusicDLTimeoutSeconds:         settings.UniversalParserMusicDLTimeoutSeconds,
	}
}

func currentClusterStatus(ctx context.Context) clusterStatus {
	local := clusterNodeStatus{
		clusterNodeInfo: currentClusterNodeInfo(),
		Endpoint:        strings.TrimSpace(os.Getenv("CLUSTER_ADVERTISE_ADDR")),
		Enabled:         true,
		Healthy:         true,
		Status:          "ok",
		Infrastructure:  currentInfrastructureStatus(ctx),
	}

	workers := configuredClusterWorkers()
	disabled := disabledClusterNodeSet()
	settings := runtimecfg.Current()
	status := clusterStatus{
		Enabled:      len(workers) > 0 && settings.ClusterDispatchMode != runtimecfg.ClusterDispatchLocal,
		DispatchMode: firstNonEmpty(settings.ClusterDispatchMode, runtimecfg.ClusterDispatchAll),
		Total:        1 + len(workers),
		EnabledNodes: 1,
		Healthy:      1,
		Nodes:        []clusterNodeStatus{local},
	}

	for _, worker := range workers {
		if clusterWorkerDisabled(worker, disabled) {
			status.DisabledNodes++
			status.Nodes = append(status.Nodes, clusterNodeStatus{
				clusterNodeInfo: clusterNodeInfo{ID: worker.Name, Name: worker.Name, Role: "worker"},
				ConfigID:        worker.Name,
				Endpoint:        worker.URL,
				Enabled:         false,
				Disabled:        true,
				Status:          "disabled",
			})
			continue
		}
		status.EnabledNodes++
		node := probeClusterWorker(ctx, worker)
		if node.Healthy {
			status.Healthy++
		}
		status.Nodes = append(status.Nodes, node)
	}
	return status
}

func configuredClusterWorkers() []clusterWorkerEndpoint {
	settings := runtimecfg.Current()
	parts := settings.ClusterWorkerEndpoints
	if len(parts) == 0 {
		return nil
	}
	workers := make([]clusterWorkerEndpoint, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := ""
		endpoint := part
		if before, after, ok := strings.Cut(part, "="); ok {
			name = strings.TrimSpace(before)
			endpoint = strings.TrimSpace(after)
		}
		endpoint = strings.TrimRight(endpoint, "/")
		if endpoint == "" {
			continue
		}
		if name == "" {
			name = endpoint
		}
		workers = append(workers, clusterWorkerEndpoint{Name: name, URL: endpoint})
	}
	return workers
}

func probeClusterWorker(ctx context.Context, worker clusterWorkerEndpoint) clusterNodeStatus {
	timeout := time.Duration(runtimecfg.Current().ClusterHealthTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, worker.URL+"/api/health", nil)
	if err != nil {
		return clusterNodeStatus{
			clusterNodeInfo: clusterNodeInfo{ID: worker.Name, Name: worker.Name, Role: "worker"},
			ConfigID:        worker.Name,
			Endpoint:        worker.URL,
			Enabled:         true,
			Status:          "invalid",
			Error:           err.Error(),
		}
	}

	client := &http.Client{Timeout: timeout + time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return clusterNodeStatus{
			clusterNodeInfo: clusterNodeInfo{ID: worker.Name, Name: worker.Name, Role: "worker"},
			ConfigID:        worker.Name,
			Endpoint:        worker.URL,
			Enabled:         true,
			Status:          "error",
			Error:           err.Error(),
			LatencyMS:       time.Since(started).Milliseconds(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return clusterNodeStatus{
			clusterNodeInfo: clusterNodeInfo{ID: worker.Name, Name: worker.Name, Role: "worker"},
			ConfigID:        worker.Name,
			Endpoint:        worker.URL,
			Enabled:         true,
			Status:          "http_error",
			Error:           resp.Status,
			LatencyMS:       time.Since(started).Milliseconds(),
		}
	}

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Node           clusterNodeInfo      `json:"node"`
			Infrastructure infrastructureStatus `json:"infrastructure"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return clusterNodeStatus{
			clusterNodeInfo: clusterNodeInfo{ID: worker.Name, Name: worker.Name, Role: "worker"},
			ConfigID:        worker.Name,
			Endpoint:        worker.URL,
			Enabled:         true,
			Status:          "decode_error",
			Error:           err.Error(),
			LatencyMS:       time.Since(started).Milliseconds(),
		}
	}

	node := payload.Data.Node
	if strings.TrimSpace(node.ID) == "" {
		node.ID = worker.Name
	}
	if strings.TrimSpace(node.Name) == "" {
		node.Name = worker.Name
	}
	if strings.TrimSpace(node.Role) == "" {
		node.Role = "worker"
	}

	return clusterNodeStatus{
		clusterNodeInfo: node,
		ConfigID:        worker.Name,
		Endpoint:        worker.URL,
		Enabled:         true,
		Healthy:         payload.Code == 0,
		Status:          firstNonEmpty(payload.Msg, "ok"),
		LatencyMS:       time.Since(started).Milliseconds(),
		Infrastructure:  payload.Data.Infrastructure,
	}
}

func disabledClusterNodeSet() map[string]struct{} {
	settings := runtimecfg.Current()
	result := make(map[string]struct{}, len(settings.ClusterDisabledNodes))
	for _, id := range settings.ClusterDisabledNodes {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		result[strings.ToLower(id)] = struct{}{}
	}
	return result
}

func clusterWorkerDisabled(worker clusterWorkerEndpoint, disabled map[string]struct{}) bool {
	if len(disabled) == 0 {
		return false
	}
	candidates := []string{
		worker.Name,
		worker.URL,
		strings.TrimRight(worker.URL, "/"),
	}
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if _, ok := disabled[candidate]; ok {
			return true
		}
	}
	return false
}
