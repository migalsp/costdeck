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

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Prepare initial generic messages
	var messages []map[string]interface{}
	for _, m := range req.Messages {
		if m.Role != "system" {
			messages = append(messages, map[string]interface{}{"role": m.Role, "content": m.Content})
		}
	}
	if len(req.Messages) == 0 {
		messages = append(messages, map[string]interface{}{"role": "user", "content": req.Prompt})
	}

	// Tool call loop (max 3 iterations)
	maxIterations := 3
	for i := 0; i < maxIterations; i++ {
		toolCallName := ""
		toolCallArgs := ""
		toolCallId := ""

		var reqPayload []byte
		var apiUrl string
		reqHeaders := map[string]string{"Content-Type": "application/json"}

		if aiConfig.Provider == "openai" || aiConfig.Provider == "local" {
			apiUrl = strings.TrimSuffix(baseUrl, "/") + "/chat/completions"
			if apiKey != "" {
				reqHeaders["Authorization"] = "Bearer " + apiKey
			}
			reqPayload = s.buildOpenAIRequest(aiConfig, systemPrompt, messages)
		} else if aiConfig.Provider == "anthropic" {
			apiUrl = strings.TrimSuffix(baseUrl, "/") + "/messages"
			reqHeaders["x-api-key"] = apiKey
			reqHeaders["anthropic-version"] = "2023-06-01"
			reqPayload = s.buildAnthropicRequest(aiConfig, systemPrompt, messages)
		} else if aiConfig.Provider == "gemini" {
			modelName := aiConfig.Model
			if modelName == "" {
				modelName = "gemini-1.5-flash"
			}
			apiUrl = fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", strings.TrimSuffix(baseUrl, "/"), modelName)
			reqHeaders["x-goog-api-key"] = apiKey
			reqPayload = s.buildGeminiRequest(aiConfig, systemPrompt, messages)
		}

		httpReq, _ := http.NewRequest("POST", apiUrl, bytes.NewReader(reqPayload))
		for k, v := range reqHeaders {
			httpReq.Header.Set(k, v)
		}

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

		scanner := bufio.NewScanner(resp.Body)
		fullAssistantText := ""
		var rawGeminiPart map[string]interface{}
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
					if len(delta.ToolCalls) > 0 {
						tc := delta.ToolCalls[0]
						if tc.Id != "" {
							toolCallId = tc.Id
						}
						if tc.Function.Name != "" {
							toolCallName = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							toolCallArgs += tc.Function.Arguments
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
					toolCallId = sResp.ContentBlock.Id
					toolCallName = sResp.ContentBlock.Name
				} else if sResp.Type == "content_block_delta" {
					if sResp.Delta.Text != "" {
						chunkText = sResp.Delta.Text
					}
					if sResp.Delta.PartialJson != "" {
						toolCallArgs += sResp.Delta.PartialJson
					}
				}
			} else if aiConfig.Provider == "gemini" {
				var sResp struct {
					Candidates []struct {
						Content struct {
							Parts []map[string]interface{} `json:"parts"`
						} `json:"content"`
					} `json:"candidates"`
				}
				json.Unmarshal([]byte(data), &sResp)
				if len(sResp.Candidates) > 0 && len(sResp.Candidates[0].Content.Parts) > 0 {
					part := sResp.Candidates[0].Content.Parts[0]
					
					if textObj, ok := part["text"]; ok {
						if textStr, ok := textObj.(string); ok && textStr != "" {
							chunkText = textStr
						}
					}
					
					if fcObj, ok := part["functionCall"]; ok && fcObj != nil {
						if fcMap, ok := fcObj.(map[string]interface{}); ok {
							rawGeminiPart = part // save the entire part (including thought_signature)
							if name, ok := fcMap["name"].(string); ok {
								toolCallName = name
							}
							if argsMap, ok := fcMap["args"].(map[string]interface{}); ok {
								argsBytes, _ := json.Marshal(argsMap)
								toolCallArgs = string(argsBytes)
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
		if toolCallName == "" {
			// Append the final assistant message so that history is correct if needed
			if fullAssistantText != "" {
				messages = append(messages, map[string]interface{}{
					"role":    "assistant",
					"content": fullAssistantText,
				})
			}
			break
		}

		// Tool was called, execute it
		statusMsg := fmt.Sprintf("\n\n> ⚙️ Executing tool: `%s`...\n\n", toolCallName)
		chunkBytes, _ := json.Marshal(statusMsg)
		fmt.Fprintf(w, "0:%s\n", string(chunkBytes))
		flusher.Flush()

		toolResult, err := s.ExecuteAITool(ctx, toolCallName, toolCallArgs)
		if err != nil {
			toolResult = fmt.Sprintf("Error executing tool: %v", err)
		}

		// Append tool call and result to messages for the next iteration
		if aiConfig.Provider == "openai" || aiConfig.Provider == "local" {
			if toolCallId == "" {
				toolCallId = "call_1"
			}
			messages = append(messages, map[string]interface{}{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   toolCallId,
						"type": "function",
						"function": map[string]interface{}{
							"name":      toolCallName,
							"arguments": toolCallArgs,
						},
					},
				},
			})
			messages = append(messages, map[string]interface{}{
				"role":         "tool",
				"tool_call_id": toolCallId,
				"name":         toolCallName,
				"content":      toolResult,
			})
		} else if aiConfig.Provider == "anthropic" {
			var args map[string]interface{}
			json.Unmarshal([]byte(toolCallArgs), &args)
			
			contentBlocks := []map[string]interface{}{}
			if fullAssistantText != "" {
				contentBlocks = append(contentBlocks, map[string]interface{}{
					"type": "text",
					"text": fullAssistantText,
				})
			}
			contentBlocks = append(contentBlocks, map[string]interface{}{
				"type":  "tool_use",
				"id":    toolCallId,
				"name":  toolCallName,
				"input": args,
			})
			
			messages = append(messages, map[string]interface{}{
				"role":    "assistant",
				"content": contentBlocks,
			})
			messages = append(messages, map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": toolCallId,
						"content":     toolResult,
					},
				},
			})
		} else if aiConfig.Provider == "gemini" {
			messages = append(messages, map[string]interface{}{
				"role": "model",
				"parts": []map[string]interface{}{
					rawGeminiPart,
				},
			})
			messages = append(messages, map[string]interface{}{
				"role": "user",
				"parts": []map[string]interface{}{
					{
						"functionResponse": map[string]interface{}{
							"name": toolCallName,
							"response": map[string]interface{}{
								"result": toolResult,
							},
						},
					},
				},
			})
		}
	}
}

// ─── Builders for requests ──────────────────────────────────────────────────

func (s *Server) buildOpenAIRequest(config *finopsv1.AIIntegrationConfig, systemPrompt string, msgs []map[string]interface{}) []byte {
	model := config.Model
	if model == "" {
		model = "gpt-4o"
	}

	req := map[string]interface{}{
		"model":  model,
		"stream": true,
	}

	var formattedMsgs []map[string]interface{}
	formattedMsgs = append(formattedMsgs, map[string]interface{}{"role": "system", "content": systemPrompt})
	for _, m := range msgs {
		formattedMsgs = append(formattedMsgs, m)
	}
	req["messages"] = formattedMsgs

	// Add tools
	var tools []map[string]interface{}
	for _, t := range s.GetAITools() {
		tools = append(tools, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
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

func (s *Server) buildAnthropicRequest(config *finopsv1.AIIntegrationConfig, systemPrompt string, msgs []map[string]interface{}) []byte {
	model := config.Model
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}

	req := map[string]interface{}{
		"model":      model,
		"stream":     true,
		"max_tokens": 4096,
		"system":     systemPrompt,
	}

	var formattedMsgs []map[string]interface{}
	for _, m := range msgs {
		if m["role"] == "system" {
			continue
		}
		formattedMsgs = append(formattedMsgs, m)
	}
	req["messages"] = formattedMsgs

	// Add tools
	var tools []map[string]interface{}
	for _, t := range s.GetAITools() {
		tools = append(tools, map[string]interface{}{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	req["tools"] = tools

	b, _ := json.Marshal(req)
	return b
}

func (s *Server) buildGeminiRequest(config *finopsv1.AIIntegrationConfig, systemPrompt string, msgs []map[string]interface{}) []byte {
	var contents []map[string]interface{}
	for _, m := range msgs {
		role := m["role"]
		if role == "system" {
			continue
		}
		if role == "assistant" {
			role = "model"
		}

		var parts []map[string]interface{}

		if contentStr, ok := m["content"].(string); ok && contentStr != "" {
			parts = append(parts, map[string]interface{}{"text": contentStr})
		} else if partsArr, ok := m["parts"].([]map[string]interface{}); ok {
			parts = partsArr
		}

		if len(parts) > 0 {
			contents = append(contents, map[string]interface{}{
				"role":  role,
				"parts": parts,
			})
		}
	}

	req := map[string]interface{}{
		"system_instruction": map[string]interface{}{
			"parts": map[string]interface{}{"text": systemPrompt},
		},
		"contents": contents,
	}

	// Add tools
	var funcDecls []map[string]interface{}
	for _, t := range s.GetAITools() {
		// Convert standard schema to Gemini schema slightly
		params := t.Parameters
		params["type"] = "OBJECT" // Gemini needs uppercase
		if props, ok := params["properties"].(map[string]interface{}); ok {
			for _, p := range props {
				if propMap, ok := p.(map[string]interface{}); ok {
					if propType, ok := propMap["type"].(string); ok {
						propMap["type"] = strings.ToUpper(propType)
					}
				}
			}
		}
		funcDecls = append(funcDecls, map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		})
	}
	req["tools"] = []map[string]interface{}{
		{
			"functionDeclarations": funcDecls,
		},
	}

	b, _ := json.Marshal(req)
	return b
}
