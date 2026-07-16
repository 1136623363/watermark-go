package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

type adminPlatformTestTarget struct {
	Local    bool
	Endpoint string
	Node     clusterNodeInfo
}

type adminPlatformTestSchedulerHooks struct {
	OnRunning  func(index int, target adminPlatformTestTarget)
	OnComplete func(index int, result adminPlatformTestResult)
}

func adminClusterTestConcurrency() int {
	concurrency := runtimecfg.Current().ClusterTestConcurrency
	if concurrency <= 0 {
		return 3
	}
	return concurrency
}

func runAdminPlatformTestsWithScheduler(ctx context.Context, links []adminTestLink, hooks adminPlatformTestSchedulerHooks) []adminPlatformTestResult {
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]adminPlatformTestResult, len(links))
	if len(links) == 0 {
		return results
	}

	targets := adminPlatformTestTargets()
	if len(targets) == 0 {
		targets = []adminPlatformTestTarget{{Local: true, Node: currentClusterNodeInfo()}}
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	slotsPerTarget := adminClusterTestConcurrency()
	if slotsPerTarget < 1 {
		slotsPerTarget = 1
	}

	for _, target := range targets {
		target := target
		for slot := 0; slot < slotsPerTarget; slot++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					if err := ctx.Err(); err != nil {
						result := canceledAdminPlatformTestResult(links[index], err)
						results[index] = result
						if hooks.OnComplete != nil {
							hooks.OnComplete(index, result)
						}
						continue
					}
					if hooks.OnRunning != nil {
						hooks.OnRunning(index, target)
					}
					result := runAdminPlatformTestOnTarget(links[index], target)
					if result.Status == "" || result.Status == "running" {
						result.Status = "completed"
					}
					results[index] = result
					if hooks.OnComplete != nil {
						hooks.OnComplete(index, result)
					}
				}
			}()
		}
	}

sendLoop:
	for index := range links {
		select {
		case <-ctx.Done():
			for ; index < len(links); index++ {
				result := canceledAdminPlatformTestResult(links[index], ctx.Err())
				results[index] = result
				if hooks.OnComplete != nil {
					hooks.OnComplete(index, result)
				}
			}
			break sendLoop
		case jobs <- index:
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

func canceledAdminPlatformTestResult(item adminTestLink, err error) adminPlatformTestResult {
	now := time.Now()
	return adminPlatformTestResult{
		Name:        item.Name,
		URL:         item.URL,
		Platform:    item.Platform,
		Status:      "completed",
		OK:          false,
		Error:       firstNonEmpty(errString(err), "test canceled"),
		StartedAt:   &now,
		RespondedAt: &now,
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func adminPlatformTestTargets() []adminPlatformTestTarget {
	local := currentClusterNodeInfo()
	localTarget := adminPlatformTestTarget{
		Local: true,
		Node:  local,
	}
	workerTargets := make([]adminPlatformTestTarget, 0)
	disabled := disabledClusterNodeSet()
	for _, worker := range configuredClusterWorkers() {
		if clusterWorkerDisabled(worker, disabled) {
			continue
		}
		workerTargets = append(workerTargets, adminPlatformTestTarget{
			Endpoint: strings.TrimRight(worker.URL, "/"),
			Node: clusterNodeInfo{
				ID:   worker.Name,
				Name: worker.Name,
				Role: "worker",
			},
		})
	}

	switch runtimecfg.Current().ClusterDispatchMode {
	case runtimecfg.ClusterDispatchLocal:
		return []adminPlatformTestTarget{localTarget}
	case runtimecfg.ClusterDispatchWorkers:
		if len(workerTargets) > 0 {
			return workerTargets
		}
		return []adminPlatformTestTarget{localTarget}
	default:
		return append([]adminPlatformTestTarget{localTarget}, workerTargets...)
	}
}

func runAdminPlatformTestOnTarget(item adminTestLink, target adminPlatformTestTarget) adminPlatformTestResult {
	if target.Local || strings.TrimSpace(target.Endpoint) == "" {
		return runAdminPlatformTest(item)
	}
	started := time.Now()
	result, err := runRemoteAdminPlatformTest(item, target)
	if err == nil {
		return result
	}

	respondedAt := time.Now()
	return adminPlatformTestResult{
		Name:        item.Name,
		URL:         item.URL,
		Platform:    item.Platform,
		Status:      "completed",
		OK:          false,
		Error:       err.Error(),
		DurationMS:  respondedAt.Sub(started).Milliseconds(),
		StartedAt:   &started,
		RespondedAt: &respondedAt,
		NodeID:      target.Node.ID,
		NodeName:    target.Node.Name,
		NodeRole:    firstNonEmpty(target.Node.Role, "worker"),
	}
}

func runRemoteAdminPlatformTest(item adminTestLink, target adminPlatformTestTarget) (adminPlatformTestResult, error) {
	body, err := json.Marshal(item)
	if err != nil {
		return adminPlatformTestResult{}, err
	}

	timeout := time.Duration(runtimecfg.Current().ClusterRemoteTestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Endpoint+"/api/internal/platform-test", bytes.NewReader(body))
	if err != nil {
		return adminPlatformTestResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(os.Getenv("CLUSTER_INTERNAL_TOKEN")); token != "" {
		req.Header.Set("X-Cluster-Token", token)
	}

	client := &http.Client{Timeout: timeout + 5*time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return adminPlatformTestResult{}, fmt.Errorf("worker %s request failed: %w", firstNonEmpty(target.Node.Name, target.Endpoint), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adminPlatformTestResult{}, fmt.Errorf("worker %s returned HTTP %d", firstNonEmpty(target.Node.Name, target.Endpoint), resp.StatusCode)
	}

	var payload struct {
		Code int                     `json:"code"`
		Msg  string                  `json:"msg"`
		Data adminPlatformTestResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return adminPlatformTestResult{}, err
	}
	if payload.Code != 0 {
		return adminPlatformTestResult{}, errors.New(firstNonEmpty(payload.Msg, "worker platform test failed"))
	}
	return payload.Data, nil
}

func handleInternalPlatformTest(c *gin.Context) {
	if !allowInternalClusterRequest(c) {
		c.JSON(http.StatusForbidden, httpResponse{Code: 403, Msg: "forbidden"})
		return
	}
	var item adminTestLink
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: "invalid platform test payload"})
		return
	}
	result := runAdminPlatformTest(item)
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok", Data: result})
}

func allowInternalClusterRequest(c *gin.Context) bool {
	token := strings.TrimSpace(os.Getenv("CLUSTER_INTERNAL_TOKEN"))
	if token != "" {
		return c.GetHeader("X-Cluster-Token") == token
	}
	ip := net.ParseIP(strings.TrimSpace(c.ClientIP()))
	return isInternalClusterIP(ip)
}

func isInternalClusterIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}
