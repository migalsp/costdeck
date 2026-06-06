package api

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

type AIReportSaveRequest struct {
	Report string `json:"report"`
}

func (s *Server) handleAIReportGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	config := s.getOrCreateDefaultConfig(ctx)
	operatorNs := config.Namespace

	cm, err := s.K8sClient.CoreV1().ConfigMaps(operatorNs).Get(ctx, "costdeck-ai-report", metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"report": ""})
			return
		}
		http.Error(w, "Failed to get report map", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"report": cm.Data["report.md"]})
}

func (s *Server) handleAIReportSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AIReportSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	config := s.getOrCreateDefaultConfig(ctx)
	operatorNs := config.Namespace

	cm, err := s.K8sClient.CoreV1().ConfigMaps(operatorNs).Get(ctx, "costdeck-ai-report", metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			newCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "costdeck-ai-report",
					Namespace: operatorNs,
				},
				Data: map[string]string{
					"report.md": req.Report,
				},
			}
			if _, err := s.K8sClient.CoreV1().ConfigMaps(operatorNs).Create(ctx, newCM, metav1.CreateOptions{}); err != nil {
				http.Error(w, "Failed to save report", http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, "Failed to get ConfigMap", http.StatusInternalServerError)
			return
		}
	} else {
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		cm.Data["report.md"] = req.Report
		if _, err := s.K8sClient.CoreV1().ConfigMaps(operatorNs).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			http.Error(w, "Failed to update report", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleAIReportGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	config := s.getOrCreateDefaultConfig(ctx)

	if config.Spec.Integrations.AI == nil || !config.Spec.Integrations.AI.Enabled {
		http.Error(w, "AI features are disabled", http.StatusBadRequest)
		return
	}

	// 1. Gather all cluster data for the report
	var nsList finopsv1.NamespaceFinOpsList
	s.Client.List(ctx, &nsList)

	var scalingGroups finopsv1.ScalingGroupList
	s.Client.List(ctx, &scalingGroups)

	nodes, err := s.K8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	nodeCount := 0
	provider := "local"
	var totalClusterCostMonthly float64

	if err == nil && nodes != nil {
		nodeCount = len(nodes.Items)
		if nodeCount > 0 {
			providerID := strings.ToLower(nodes.Items[0].Spec.ProviderID)
			if strings.HasPrefix(providerID, "aws://") {
				provider = "aws"
			} else if strings.HasPrefix(providerID, "azure://") {
				provider = "azure"
			} else if strings.HasPrefix(providerID, "gce://") {
				provider = "gcp"
			}
		}

		cpuRate, ramRate := getDefaultRates(provider)
		for _, n := range nodes.Items {
			cpuQty := n.Status.Capacity.Cpu()
			memQty := n.Status.Capacity.Memory()
			cpuCores := float64(cpuQty.MilliValue()) / 1000.0
			ramGb := float64(memQty.Value()) / (1024 * 1024 * 1024)
			hourly := (cpuCores * cpuRate) + (ramGb * ramRate)
			totalClusterCostMonthly += hourly * 730
		}
	}

	cpuRate, ramRate := getDefaultRates(provider)

	pods, err := s.K8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	podCount := 0
	if err == nil && pods != nil {
		podCount = len(pods.Items)
	}

	// Prepare data summary
	type NsSummary struct {
		Name                  string
		CPUUsage              string
		CPULimits             string
		MemUsage              string
		MemLimits             string
		Insights              []string
		EstimatedCostMonthly  float64
		EstimatedWasteMonthly float64
	}
	var summaries []NsSummary
	for _, ns := range nsList.Items {
		var cpuU, cpuL, memU, memL string
		var cost, waste float64
		if len(ns.Status.History) > 0 {
			last := ns.Status.History[len(ns.Status.History)-1]
			cpuU = last.CPU.Usage
			cpuL = last.CPU.Limits
			memU = last.Memory.Usage
			memL = last.Memory.Limits

			// Approximate cost
			cpuUsedQty, _ := resource.ParseQuantity(cpuU)
			cpuLimQty, _ := resource.ParseQuantity(cpuL)
			memUsedQty, _ := resource.ParseQuantity(memU)
			memLimQty, _ := resource.ParseQuantity(memL)

			cLimCores := float64(cpuLimQty.MilliValue()) / 1000.0
			if cLimCores == 0 {
				cLimCores = float64(cpuUsedQty.MilliValue()) / 1000.0
			}
			mLimGb := float64(memLimQty.Value()) / (1024 * 1024 * 1024)
			if mLimGb == 0 {
				mLimGb = float64(memUsedQty.Value()) / (1024 * 1024 * 1024)
			}

			hourly := (cLimCores * cpuRate) + (mLimGb * ramRate)
			cost = hourly * 730

			// Waste
			cWaste := cLimCores - (float64(cpuUsedQty.MilliValue()) / 1000.0)
			mWaste := mLimGb - (float64(memUsedQty.Value()) / (1024 * 1024 * 1024))
			if cWaste > 0 || mWaste > 0 {
				hourlyWaste := 0.0
				if cWaste > 0 {
					hourlyWaste += cWaste * cpuRate
				}
				if mWaste > 0 {
					hourlyWaste += mWaste * ramRate
				}
				waste = hourlyWaste * 730
			}
		}
		summaries = append(summaries, NsSummary{
			Name:                  ns.Name,
			CPUUsage:              cpuU,
			CPULimits:             cpuL,
			MemUsage:              memU,
			MemLimits:             memL,
			Insights:              ns.Status.Insights,
			EstimatedCostMonthly:  cost,
			EstimatedWasteMonthly: waste,
		})
	}

	summaryBytes, _ := json.Marshal(summaries)
	groupBytes, _ := json.Marshal(scalingGroups.Items)

	systemPrompt := `You are an elite FinOps Analyst and Kubernetes Expert. 
Your task is to generate a comprehensive, industry-standard AI FinOps Report for the current Kubernetes cluster.
The report must be extremely informative, highlighting BOTH what is currently working well for cost optimization AND critical bottlenecks/wasted resources.
You MUST provide explicit dollar ($) estimates for costs, potential savings, and waste based on the data provided. Use professional markdown formatting, including headers, tables, and bullet points.

The report should include:
1. **Executive Summary**: High-level overview of cluster health, total estimated cluster cost ($), and total potential savings ($).
2. **What's Working Well**: Highlight positive FinOps practices, such as active Scaling Groups or well-optimized namespaces.
3. **Resource Bottlenecks & Financial Waste**: Detailed breakdown of wasted money ($) per namespace due to over-provisioned limits or low usage. Use tables.
4. **Scaling Opportunities**: Review the provided scaling groups and suggest expansions or optimizations with estimated ROI.
5. **Actionable Recommendations**: Clear next steps to realize the estimated savings.

Data Context:
Total Nodes: ` + fmt.Sprintf("%d", nodeCount) + `
Total Pods: ` + fmt.Sprintf("%d", podCount) + `
Total Estimated Cluster Cost (Monthly): $` + fmt.Sprintf("%.2f", totalClusterCostMonthly) + `
Namespace Metrics with Estimated Monthly Cost and Waste ($) (JSON): ` + string(summaryBytes) + `
Scaling Groups (JSON): ` + string(groupBytes)

	userPrompt := "Generate the comprehensive FinOps Report based on the provided cluster data."

	aiConfig := config.Spec.Integrations.AI
	if aiConfig.Provider == "" {
		aiConfig.Provider = "openai"
	}

	apiKey := ""
	if aiConfig.SecretRef != "" {
		secret := &corev1.Secret{}
		err := s.Client.Get(ctx, client.ObjectKey{Name: aiConfig.SecretRef, Namespace: config.Namespace}, secret)
		if err == nil {
			apiKey = string(secret.Data["API_KEY"])
		}
	}

	baseUrl := aiConfig.BaseURL
	if baseUrl == "" {
		if aiConfig.Provider == "openai" {
			baseUrl = "https://api.openai.com/v1"
		} else if aiConfig.Provider == "anthropic" {
			baseUrl = "https://api.anthropic.com/v1"
		} else if aiConfig.Provider == "gemini" {
			baseUrl = "https://generativelanguage.googleapis.com/v1beta"
		} else {
			baseUrl = "http://localhost:11434/v1"
		}
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}
	if aiConfig.SkipSSLVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	var apiUrl string
	var payloadBytes []byte
	var reqHeaders map[string]string

	if aiConfig.Provider == "openai" || aiConfig.Provider == "local" {
		apiUrl = strings.TrimSuffix(baseUrl, "/") + "/chat/completions"
		reqPayload := openAIRequest{
			Model:  aiConfig.Model,
			Stream: true,
			Messages: []openAIMessage{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: userPrompt},
			},
		}
		if reqPayload.Model == "" {
			reqPayload.Model = "gpt-4o"
		}
		payloadBytes, _ = json.Marshal(reqPayload)
		reqHeaders = map[string]string{"Content-Type": "application/json"}
		if apiKey != "" {
			reqHeaders["Authorization"] = "Bearer " + apiKey
		}
	} else if aiConfig.Provider == "anthropic" {
		apiUrl = strings.TrimSuffix(baseUrl, "/") + "/messages"
		reqPayload := map[string]interface{}{
			"model":      aiConfig.Model,
			"max_tokens": 4096,
			"stream":     true,
			"system":     systemPrompt,
			"messages": []map[string]interface{}{
				{"role": "user", "content": userPrompt},
			},
		}
		if reqPayload["model"] == "" {
			reqPayload["model"] = "claude-3-5-sonnet-20241022"
		}
		payloadBytes, _ = json.Marshal(reqPayload)
		reqHeaders = map[string]string{
			"Content-Type":      "application/json",
			"x-api-key":         apiKey,
			"anthropic-version": "2023-06-01",
		}
	} else if aiConfig.Provider == "gemini" {
		modelName := aiConfig.Model
		if modelName == "" {
			modelName = "gemini-1.5-pro"
		}
		apiUrl = fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", strings.TrimSuffix(baseUrl, "/"), modelName)
		reqPayload := map[string]interface{}{
			"system_instruction": map[string]interface{}{
				"parts": map[string]interface{}{"text": systemPrompt},
			},
			"contents": []map[string]interface{}{
				{"role": "user", "parts": []map[string]interface{}{{"text": userPrompt}}},
			},
		}
		payloadBytes, _ = json.Marshal(reqPayload)
		reqHeaders = map[string]string{
			"Content-Type":   "application/json",
			"x-goog-api-key": apiKey,
		}
	}

	reqAI, err := http.NewRequestWithContext(ctx, "POST", apiUrl, bytes.NewBuffer(payloadBytes))
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	for k, v := range reqHeaders {
		reqAI.Header.Set(k, v)
	}

	resp, err := httpClient.Do(reqAI)
	if err != nil {
		logf.Log.Error(err, "Failed to call AI API")
		http.Error(w, "Failed to call AI API", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		logf.Log.Error(fmt.Errorf("status %d", resp.StatusCode), "AI API error", "body", string(b))
		http.Error(w, "AI API error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line == "data: [DONE]" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			dataStr := strings.TrimPrefix(line, "data: ")
			var textChunk string

			if aiConfig.Provider == "openai" || aiConfig.Provider == "local" {
				var chunk openAIStreamResp
				if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
					if len(chunk.Choices) > 0 {
						textChunk = chunk.Choices[0].Delta.Content
					}
				}
			} else if aiConfig.Provider == "anthropic" {
				var chunk anthropicStreamResp
				if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
					if chunk.Type == "content_block_delta" {
						textChunk = chunk.Delta.Text
					}
				}
			} else if aiConfig.Provider == "gemini" {
				var chunk geminiStreamResp
				if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
					if len(chunk.Candidates) > 0 && len(chunk.Candidates[0].Content.Parts) > 0 {
						textChunk = chunk.Candidates[0].Content.Parts[0].Text
					}
				}
			}

			if textChunk != "" {
				chunkData, _ := json.Marshal(textChunk)
				fmt.Fprintf(w, "data: %s\n\n", chunkData)
				flusher.Flush()
			}
		}
	}
}
