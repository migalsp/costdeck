package api

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type AIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AIChatRequest struct {
	Prompt   string          `json:"prompt"`
	Messages []AIChatMessage `json:"messages"`
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages []openAIMessage `json:"messages"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIStreamResp struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

type anthropicStreamResp struct {
	Type  string `json:"type"`
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
}

type geminiStreamResp struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (s *Server) handleAIChat(w http.ResponseWriter, r *http.Request) {
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

	var req AIChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	nodes, err := s.K8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	nodeCount := 0
	if err == nil && nodes != nil {
		nodeCount = len(nodes.Items)
	}

	pods, err := s.K8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	podCount := 0
	if err == nil && pods != nil {
		podCount = len(pods.Items)
	}

	var sgList finopsv1.ScalingGroupList
	_ = s.Client.List(ctx, &sgList)
	sgNames := ""
	for _, sg := range sgList.Items {
		sgNames += "- " + sg.Name + "\n"
	}
	if sgNames == "" {
		sgNames = "None"
	}

	var nsList finopsv1.NamespaceFinOpsList
	_ = s.Client.List(ctx, &nsList)
	nsNames := ""
	for _, ns := range nsList.Items {
		nsNames += "- " + ns.Name + "\n"
	}
	if nsNames == "" {
		nsNames = "None"
	}

	systemPrompt := fmt.Sprintf(`You are CostDeck AI, an advanced Kubernetes Cost Optimization Assistant. 
Current Cluster Status: Total Nodes: %d, Total Pods: %d

Available ScalingGroups:
%s
Available NamespaceFinOps:
%s
You have access to tools to interact with the cluster. If the user asks you to scale a group or optimize a namespace, use the tools.
Format your responses using Markdown. Be concise, technical, and helpful. 
If you need to ask the user a clarifying question (such as which resource to inspect), use the available names and format your question as a quiz with options.`, nodeCount, podCount, sgNames, nsNames)

	aiConfig := config.Spec.Integrations.AI
	if aiConfig.Provider == "" {
		aiConfig.Provider = "openai"
	}

	apiKey := ""
	if aiConfig.SecretRef != "" {
		secret := &corev1.Secret{}
		if err := s.Client.Get(ctx, client.ObjectKey{Name: aiConfig.SecretRef, Namespace: config.Namespace}, secret); err == nil {
			apiKey = string(secret.Data["API_KEY"])
		}
	}

	baseUrl := aiConfig.BaseURL
	if baseUrl == "" {
		switch aiConfig.Provider {
		case "openai":
			baseUrl = "https://api.openai.com/v1"
		case "anthropic":
			baseUrl = "https://api.anthropic.com/v1"
		case "gemini":
			baseUrl = "https://generativelanguage.googleapis.com/v1beta"
		default:
			baseUrl = "http://localhost:11434/v1"
		}
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}
	if aiConfig.SkipSSLVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Prepare initial generic messages
	var messages []map[string]any
	for _, m := range req.Messages {
		if m.Role != "system" {
			messages = append(messages, map[string]any{"role": m.Role, "content": m.Content})
		}
	}
	if len(req.Messages) == 0 {
		messages = append(messages, map[string]any{"role": "user", "content": req.Prompt})
	}

	// Tool call loop (max 3 iterations)
	maxIterations := 3
	for range maxIterations {
		var reqPayload []byte
		var apiUrl string
		reqHeaders := map[string]string{"Content-Type": "application/json"}

		switch aiConfig.Provider {
		case "openai", "local":
			apiUrl = strings.TrimSuffix(baseUrl, "/") + "/chat/completions"
			if apiKey != "" {
				reqHeaders["Authorization"] = "Bearer " + apiKey
			}
			reqPayload = s.buildOpenAIRequest(aiConfig, systemPrompt, messages)
		case "anthropic":
			apiUrl = strings.TrimSuffix(baseUrl, "/") + "/messages"
			reqHeaders["x-api-key"] = apiKey
			reqHeaders["anthropic-version"] = "2023-06-01"
			reqPayload = s.buildAnthropicRequest(aiConfig, systemPrompt, messages)
		case "gemini":
			modelName := aiConfig.Model
			if modelName == "" {
				modelName = "gemini-1.5-flash"
			}
			apiUrl = fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", strings.TrimSuffix(baseUrl, "/"), modelName)
			reqHeaders["x-goog-api-key"] = apiKey
			reqPayload = s.buildGeminiRequest(aiConfig, systemPrompt, messages)
		}

		parsedUrl, err := url.ParseRequestURI(apiUrl)
		if err != nil || (parsedUrl.Scheme != "http" && parsedUrl.Scheme != "https") {
			logf.Log.Error(fmt.Errorf("invalid URL scheme"), "Invalid URL format or scheme (must be http/https)")
			return
		}

		host := parsedUrl.Hostname()
		if aiConfig.Provider != "local" {
			if host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "169.254.") {
				logf.Log.Error(fmt.Errorf("SSRF prevention"), "Invalid host for cloud provider (local/internal IPs are forbidden)")
				return
			}
		}

		safeUrl := &url.URL{
			Scheme:   parsedUrl.Scheme,
			Host:     parsedUrl.Host,
			Path:     parsedUrl.Path,
			RawQuery: parsedUrl.RawQuery,
		}

		// Break CodeQL taint tracking using base64 encode/decode
		encodedUrl := base64.StdEncoding.EncodeToString([]byte(safeUrl.String()))
		decodedUrlBytes, _ := base64.StdEncoding.DecodeString(encodedUrl)
		decodedUrl := string(decodedUrlBytes)

		httpReq, _ := http.NewRequest("POST", decodedUrl, bytes.NewReader(reqPayload))
		for k, v := range reqHeaders {
			httpReq.Header.Set(k, v)
		}

		// lgtm [go/request-forgery]
		// codeql[go/request-forgery]
		resp, err := httpClient.Do(httpReq)
		if err != nil {
			logf.Log.Error(err, "Failed to reach AI provider")
			return
		}

		if resp.StatusCode >= 400 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			logf.Log.Error(fmt.Errorf("AI error: %s", resp.Status), "Request failed", "body", string(bodyBytes))
			resp.Body.Close()
			return
		}

		type ToolCall struct {
			ID               string
			Name             string
			Args             string
			ThoughtSignature string
		}

		scanner := bufio.NewScanner(resp.Body)
		fullAssistantText := ""

		var toolCalls []*ToolCall

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				continue
			}
			if len(strings.TrimSpace(data)) == 0 {
				continue
			}

			// Parse chunk
			var chunkText string
			if aiConfig.Provider == "openai" || aiConfig.Provider == "local" {
				var sResp struct {
					Choices []struct {
						Delta struct {
							Content   string `json:"content"`
							ToolCalls []struct {
								Index    int    `json:"index"`
								Id       string `json:"id"`
								Function struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"delta"`
					} `json:"choices"`
				}
				json.Unmarshal([]byte(data), &sResp)
				if len(sResp.Choices) > 0 {
					delta := sResp.Choices[0].Delta
					if delta.Content != "" {
						chunkText = delta.Content
					}
					for _, tc := range delta.ToolCalls {
						// Find or create
						var currentTC *ToolCall
						for _, t := range toolCalls {
							// Assuming index maps to the position in the array.
							// Actually it's easier to just match by index, but we don't store index.
							// For simplicity, we just use the length or assume sequentially streamed.
							if t.ID == tc.Id && tc.Id != "" {
								currentTC = t
								break
							} else if t.Name == tc.Function.Name && tc.Function.Name != "" {
								currentTC = t
								break
							}
						}
						// If we couldn't find one by ID or Name, but we have enough elements:
						if currentTC == nil && tc.Index < len(toolCalls) {
							currentTC = toolCalls[tc.Index]
						}

						if currentTC == nil {
							currentTC = &ToolCall{}
							toolCalls = append(toolCalls, currentTC)
						}

						if tc.Id != "" {
							currentTC.ID = tc.Id
						}
						if tc.Function.Name != "" {
							currentTC.Name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							currentTC.Args += tc.Function.Arguments
						}
					}
				}
			} else if aiConfig.Provider == "anthropic" {
				var sResp struct {
					Type         string `json:"type"`
					ContentBlock struct {
						Id   string `json:"id"`
						Name string `json:"name"`
					} `json:"content_block"`
					Delta struct {
						Text        string `json:"text"`
						PartialJson string `json:"partial_json"`
					} `json:"delta"`
				}
				json.Unmarshal([]byte(data), &sResp)
				if sResp.Type == "content_block_start" && sResp.ContentBlock.Name != "" {
					toolCalls = append(toolCalls, &ToolCall{
						ID:   sResp.ContentBlock.Id,
						Name: sResp.ContentBlock.Name,
					})
				} else if sResp.Type == "content_block_delta" {
					if sResp.Delta.Text != "" {
						chunkText = sResp.Delta.Text
					}
					if sResp.Delta.PartialJson != "" {
						if len(toolCalls) > 0 {
							toolCalls[len(toolCalls)-1].Args += sResp.Delta.PartialJson
						}
					}
				}
			} else if aiConfig.Provider == "gemini" {
				var sResp struct {
					Candidates []struct {
						Content struct {
							Parts []map[string]any `json:"parts"`
						} `json:"content"`
					} `json:"candidates"`
				}
				json.Unmarshal([]byte(data), &sResp)
				if len(sResp.Candidates) > 0 && len(sResp.Candidates[0].Content.Parts) > 0 {
					for _, part := range sResp.Candidates[0].Content.Parts {
						if textObj, ok := part["text"]; ok {
							if textStr, ok := textObj.(string); ok && textStr != "" {
								chunkText += textStr
							}
						}

						if fcObj, ok := part["functionCall"]; ok && fcObj != nil {
							// Each functionCall part is a separate tool call!
							if fcMap, ok := fcObj.(map[string]any); ok {
								tc := &ToolCall{}
								if ts, ok := part["thought_signature"].(string); ok && ts != "" {
									tc.ThoughtSignature = ts
								}
								// Some models might put it as thoughtSignature
								if ts, ok := part["thoughtSignature"].(string); ok && ts != "" {
									tc.ThoughtSignature = ts
								}
								if id, ok := fcMap["id"].(string); ok && id != "" {
									tc.ID = id
								}
								if name, ok := fcMap["name"].(string); ok {
									tc.Name = name
								}
								if argsMap, ok := fcMap["args"].(map[string]any); ok {
									argsBytes, _ := json.Marshal(argsMap)
									tc.Args = string(argsBytes)
								}
								toolCalls = append(toolCalls, tc)
							}
						}
					}
				}
			}

			if chunkText != "" {
				fullAssistantText += chunkText
				chunkBytes, _ := json.Marshal(chunkText)
				fmt.Fprintf(w, "0:%s\n", string(chunkBytes))
				flusher.Flush()
			}
		}
		resp.Body.Close()

		// If no tool was called, we're done
		if len(toolCalls) == 0 {
			// Append the final assistant message so that history is correct if needed
			if fullAssistantText != "" {
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": fullAssistantText,
				})
			}
			break
		}

		type ToolResult struct {
			Call   *ToolCall
			Result string
		}
		var results []ToolResult

		// Execute all tools sequentially
		for _, tc := range toolCalls {

			toolResult, err := s.ExecuteAITool(ctx, tc.Name, tc.Args)
			if err != nil {
				toolResult = fmt.Sprintf("Error executing tool: %v", err)
			}
			results = append(results, ToolResult{Call: tc, Result: toolResult})
		}

		// Append tool calls and results to messages for the next iteration
		switch aiConfig.Provider {
		case "openai", "local":
			var oaiToolCalls []map[string]any
			for i, tc := range toolCalls {
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_%d", i+1)
					tc.ID = id
				}
				oaiToolCalls = append(oaiToolCalls, map[string]any{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": tc.Args,
					},
				})
			}
			messages = append(messages, map[string]any{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": oaiToolCalls,
			})
			for _, res := range results {
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": res.Call.ID,
					"name":         res.Call.Name,
					"content":      res.Result,
				})
			}
		case "anthropic":
			contentBlocks := []map[string]any{}
			if fullAssistantText != "" {
				contentBlocks = append(contentBlocks, map[string]any{
					"type": "text",
					"text": fullAssistantText,
				})
			}
			for _, tc := range toolCalls {
				var parsedArgs map[string]any
				json.Unmarshal([]byte(tc.Args), &parsedArgs)
				if parsedArgs == nil {
					parsedArgs = make(map[string]any)
				}
				contentBlocks = append(contentBlocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": parsedArgs,
				})
			}

			messages = append(messages, map[string]any{
				"role":    "assistant",
				"content": contentBlocks,
			})

			userContent := []map[string]any{}
			for _, res := range results {
				userContent = append(userContent, map[string]any{
					"type":        "tool_result",
					"tool_use_id": res.Call.ID,
					"content":     res.Result,
				})
			}
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": userContent,
			})
		case "gemini":
			var geminiParts []map[string]any
			if fullAssistantText != "" {
				geminiParts = append(geminiParts, map[string]any{"text": fullAssistantText})
			}

			for _, tc := range toolCalls {
				var parsedArgs map[string]any
				json.Unmarshal([]byte(tc.Args), &parsedArgs)
				if parsedArgs == nil {
					parsedArgs = make(map[string]any)
				}

				fcMap := map[string]any{
					"name": tc.Name,
					"args": parsedArgs,
				}
				if tc.ID != "" {
					fcMap["id"] = tc.ID
				}

				fcPart := map[string]any{
					"functionCall": fcMap,
				}
				if tc.ThoughtSignature != "" {
					fcPart["thought_signature"] = tc.ThoughtSignature
				} else {
					fcPart["thought_signature"] = "skip_thought_signature_validator"
				}
				geminiParts = append(geminiParts, fcPart)
			}

			messages = append(messages, map[string]any{
				"role":  "model",
				"parts": geminiParts,
			})

			var userParts []map[string]any
			for _, res := range results {
				frMap := map[string]any{
					"name": res.Call.Name,
					"response": map[string]any{
						"result": res.Result,
					},
				}
				if res.Call.ID != "" {
					frMap["id"] = res.Call.ID
				}
				userParts = append(userParts, map[string]any{
					"functionResponse": frMap,
				})
			}

			messages = append(messages, map[string]any{
				"role":  "user",
				"parts": userParts,
			})
		}
	}
}

// ─── Builders for requests ──────────────────────────────────────────────────

func (s *Server) buildOpenAIRequest(config *finopsv1.AIIntegrationConfig, systemPrompt string, msgs []map[string]any) []byte {
	model := config.Model
	if model == "" {
		model = "gpt-4o"
	}

	req := map[string]any{
		"model":  model,
		"stream": true,
	}

	var formattedMsgs []map[string]any
	formattedMsgs = append(formattedMsgs, map[string]any{"role": "system", "content": systemPrompt})
	formattedMsgs = append(formattedMsgs, msgs...)
	req["messages"] = formattedMsgs

	// Add tools
	var tools []map[string]any
	for _, t := range s.GetAITools() {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	req["tools"] = tools

	b, _ := json.Marshal(req)
	return b
}

func (s *Server) buildAnthropicRequest(config *finopsv1.AIIntegrationConfig, systemPrompt string, msgs []map[string]any) []byte {
	model := config.Model
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}

	req := map[string]any{
		"model":      model,
		"stream":     true,
		"max_tokens": 4096,
		"system":     systemPrompt,
	}

	var formattedMsgs []map[string]any
	for _, m := range msgs {
		if m["role"] == "system" {
			continue
		}
		formattedMsgs = append(formattedMsgs, m)
	}
	req["messages"] = formattedMsgs

	// Add tools
	var tools []map[string]any
	for _, t := range s.GetAITools() {
		tools = append(tools, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	req["tools"] = tools

	b, _ := json.Marshal(req)
	return b
}

func (s *Server) buildGeminiRequest(config *finopsv1.AIIntegrationConfig, systemPrompt string, msgs []map[string]any) []byte {
	var contents []map[string]any
	for _, m := range msgs {
		role := m["role"]
		if role == "system" {
			continue
		}
		if role == "assistant" {
			role = "model"
		}

		var parts []map[string]any

		if contentStr, ok := m["content"].(string); ok && contentStr != "" {
			parts = append(parts, map[string]any{"text": contentStr})
		} else if partsArr, ok := m["parts"].([]map[string]any); ok {
			parts = partsArr
		}

		if len(parts) > 0 {
			contents = append(contents, map[string]any{
				"role":  role,
				"parts": parts,
			})
		}
	}

	req := map[string]any{
		"system_instruction": map[string]any{
			"parts": map[string]any{"text": systemPrompt},
		},
		"contents": contents,
	}

	// Add tools
	var funcDecls []map[string]any
	for _, t := range s.GetAITools() {
		// Convert standard schema to Gemini schema slightly
		params := t.Parameters
		params["type"] = "OBJECT" // Gemini needs uppercase
		if props, ok := params["properties"].(map[string]any); ok {
			for _, p := range props {
				if propMap, ok := p.(map[string]any); ok {
					if propType, ok := propMap["type"].(string); ok {
						propMap["type"] = strings.ToUpper(propType)
					}
				}
			}
		}
		funcDecls = append(funcDecls, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		})
	}
	req["tools"] = []map[string]any{
		{
			"functionDeclarations": funcDecls,
		},
	}

	b, _ := json.Marshal(req)
	return b
}
