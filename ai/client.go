package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"talk-to-ugur-back/config"
	"talk-to-ugur-back/models/db"
)

type Client struct {
	baseURL      string
	apiKey       string
	model        string
	temperature  float64
	systemPrompt string
	promptPath   string
	emotions     []string
	httpClient   *http.Client
}

type Reply struct {
	Text    string
	Emotion string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Refusal string `json:"refusal,omitempty"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature,omitempty"`
	Stream         bool           `json:"stream"`
	ResponseFormat responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

type chatStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			Refusal string `json:"refusal,omitempty"`
		} `json:"delta"`
	} `json:"choices"`
}
type responseFormat struct {
	Type       string      `json:"type"`
	JsonSchema *jsonSchema `json:"json_schema,omitempty"`
}

type jsonSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"schema"`
	Strict      bool           `json:"strict"`
}

type aiJSON struct {
	Reply   string `json:"reply"`
	Emotion string `json:"emotion"`
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		baseURL:      strings.TrimRight(cfg.OpenAIBaseURL, "/"),
		apiKey:       cfg.OpenAIAPIKey,
		model:        cfg.OpenAIModel,
		temperature:  cfg.OpenAITemperature,
		systemPrompt: cfg.AISystemPrompt,
		promptPath:   cfg.AISystemPromptPath,
		emotions:     cfg.AIEmotions,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) GenerateReply(ctx context.Context, history []db.ChatMessage) (Reply, error) {
	messages := make([]chatMessage, 0, len(history)+1)

	emotionList := strings.Join(c.emotions, ", ")
	formatInstruction := fmt.Sprintf("Respond ONLY with valid JSON and no extra text. The JSON must have keys 'reply' and 'emotion'. 'emotion' must be one of: %s.", emotionList)
	systemPrompt := strings.TrimSpace(c.systemPrompt)
	if prompt := c.loadPromptFromFile(); prompt != "" {
		systemPrompt = prompt
	}
	messages = append(messages, chatMessage{
		Role:    "system",
		Content: strings.TrimSpace(systemPrompt + "\n\n" + formatInstruction),
	})

	for _, msg := range history {
		role := msg.Role
		switch role {
		case "user", "assistant":
			messages = append(messages, chatMessage{Role: role, Content: msg.Content})
		}
	}

	reqBody := chatRequest{
		Model:          c.model,
		Messages:       messages,
		Temperature:    c.temperature,
		Stream:         false,
		ResponseFormat: c.buildStructuredFormat(),
	}

	parsed, err := c.doChatRequest(ctx, reqBody)
	if err != nil {
		if apiErr := (*apiError)(nil); errors.As(err, &apiErr) && shouldRetryWithoutTemperature(apiErr.status, apiErr.body) {
			reqBody.Temperature = 0
			parsed, err = c.doChatRequest(ctx, reqBody)
		}
		if err != nil {
			if apiErr := (*apiError)(nil); errors.As(err, &apiErr) && shouldFallbackToJSONMode(apiErr.status, apiErr.body) {
				reqBody.ResponseFormat = responseFormat{Type: "json_object"}
				parsed, err = c.doChatRequest(ctx, reqBody)
			}
		}
		if err != nil {
			if apiErr := (*apiError)(nil); errors.As(err, &apiErr) && shouldRetryWithoutTemperature(apiErr.status, apiErr.body) && reqBody.Temperature != 0 {
				reqBody.Temperature = 0
				parsed, err = c.doChatRequest(ctx, reqBody)
			}
		}
		if err != nil {
			return Reply{}, err
		}
	}

	if len(parsed.Choices) == 0 {
		return Reply{}, errors.New("openai api returned no choices")
	}

	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		if strings.TrimSpace(parsed.Choices[0].Message.Refusal) != "" {
			return Reply{}, errors.New("openai api refused to answer")
		}
		return Reply{}, errors.New("openai api returned empty content")
	}

	aiPayload, ok := parseAIJSON(content)
	if !ok || aiPayload.Reply == "" {
		return Reply{
			Text:    content,
			Emotion: fallbackEmotion(c.emotions),
		}, nil
	}

	emotion := normalizeEmotion(aiPayload.Emotion, c.emotions)
	if emotion == "" {
		emotion = fallbackEmotion(c.emotions)
	}

	return Reply{
		Text:    strings.TrimSpace(aiPayload.Reply),
		Emotion: emotion,
	}, nil
}

func (c *Client) StreamReply(ctx context.Context, history []db.ChatMessage, onChunk func(string) error) (Reply, error) {
	messages := make([]chatMessage, 0, len(history)+1)

	emotionList := strings.Join(c.emotions, ", ")
	formatInstruction := fmt.Sprintf("Respond ONLY with valid JSON and no extra text. The JSON must have keys 'reply' and 'emotion'. 'emotion' must be one of: %s.", emotionList)
	systemPrompt := strings.TrimSpace(c.systemPrompt)
	if prompt := c.loadPromptFromFile(); prompt != "" {
		systemPrompt = prompt
	}
	messages = append(messages, chatMessage{
		Role:    "system",
		Content: strings.TrimSpace(systemPrompt + "\n\n" + formatInstruction),
	})

	for _, msg := range history {
		role := msg.Role
		switch role {
		case "user", "assistant":
			messages = append(messages, chatMessage{Role: role, Content: msg.Content})
		}
	}

	reqBody := chatRequest{
		Model:          c.model,
		Messages:       messages,
		Temperature:    c.temperature,
		Stream:         true,
		ResponseFormat: c.buildStructuredFormat(),
	}

	body, err := c.doChatStreamRequestWithFallback(ctx, reqBody)
	if err != nil {
		return Reply{}, err
	}
	defer body.Close()

	reader := bufio.NewReader(body)
	var contentBuilder strings.Builder
	var refusalBuilder strings.Builder

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Reply{}, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk chatStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				contentBuilder.WriteString(choice.Delta.Content)
				if onChunk != nil {
					if err := onChunk(choice.Delta.Content); err != nil {
						return Reply{}, err
					}
				}
			}
			if choice.Delta.Refusal != "" {
				refusalBuilder.WriteString(choice.Delta.Refusal)
			}
		}
	}

	content := strings.TrimSpace(contentBuilder.String())
	if content == "" {
		if strings.TrimSpace(refusalBuilder.String()) != "" {
			return Reply{}, errors.New("openai api refused to answer")
		}
		return Reply{}, errors.New("openai api returned empty content")
	}

	aiPayload, ok := parseAIJSON(content)
	if !ok || aiPayload.Reply == "" {
		return Reply{
			Text:    content,
			Emotion: fallbackEmotion(c.emotions),
		}, nil
	}

	emotion := normalizeEmotion(aiPayload.Emotion, c.emotions)
	if emotion == "" {
		emotion = fallbackEmotion(c.emotions)
	}

	return Reply{
		Text:    strings.TrimSpace(aiPayload.Reply),
		Emotion: emotion,
	}, nil
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("openai api error: status=%d body=%s", e.status, e.body)
}

func (c *Client) doChatRequest(ctx context.Context, reqBody chatRequest) (chatResponse, error) {
	if c.apiKey == "" {
		return chatResponse{}, errors.New("missing OPENAI_API_KEY")
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return chatResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return chatResponse{}, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return chatResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return chatResponse{}, &apiError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}

	var parsed chatResponse
	if err = json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return chatResponse{}, err
	}
	return parsed, nil
}

func (c *Client) doChatStreamRequestWithFallback(ctx context.Context, reqBody chatRequest) (io.ReadCloser, error) {
	body, err := c.doChatStreamRequest(ctx, reqBody)
	if err != nil {
		if apiErr := (*apiError)(nil); errors.As(err, &apiErr) && shouldRetryWithoutTemperature(apiErr.status, apiErr.body) {
			reqBody.Temperature = 0
			body, err = c.doChatStreamRequest(ctx, reqBody)
		}
		if err != nil {
			if apiErr := (*apiError)(nil); errors.As(err, &apiErr) && shouldFallbackToJSONMode(apiErr.status, apiErr.body) {
				reqBody.ResponseFormat = responseFormat{Type: "json_object"}
				body, err = c.doChatStreamRequest(ctx, reqBody)
			}
		}
		if err != nil {
			if apiErr := (*apiError)(nil); errors.As(err, &apiErr) && shouldRetryWithoutTemperature(apiErr.status, apiErr.body) && reqBody.Temperature != 0 {
				reqBody.Temperature = 0
				body, err = c.doChatStreamRequest(ctx, reqBody)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func (c *Client) doChatStreamRequest(ctx context.Context, reqBody chatRequest) (io.ReadCloser, error) {
	if c.apiKey == "" {
		return nil, errors.New("missing OPENAI_API_KEY")
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, &apiError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}

	return resp.Body, nil
}

func (c *Client) buildStructuredFormat() responseFormat {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"reply": map[string]any{
				"type": "string",
			},
			"emotion": c.buildEmotionSchema(),
		},
		"required": []string{"reply", "emotion"},
	}

	return responseFormat{
		Type: "json_schema",
		JsonSchema: &jsonSchema{
			Name:        "chat_reply",
			Description: "Structured response for chat reply and emotion.",
			Schema:      schema,
			Strict:      true,
		},
	}
}

func (c *Client) buildEmotionSchema() map[string]any {
	prop := map[string]any{
		"type": "string",
	}
	if len(c.emotions) > 0 {
		prop["enum"] = c.emotions
	}
	return prop
}

func shouldFallbackToJSONMode(status int, body string) bool {
	if status != http.StatusBadRequest {
		return false
	}
	lowered := strings.ToLower(body)
	return strings.Contains(lowered, "response_format") ||
		strings.Contains(lowered, "json_schema") ||
		strings.Contains(lowered, "structured outputs") ||
		strings.Contains(lowered, "not supported")
}

func shouldRetryWithoutTemperature(status int, body string) bool {
	if status != http.StatusBadRequest {
		return false
	}
	lowered := strings.ToLower(body)
	return strings.Contains(lowered, "temperature") &&
		(strings.Contains(lowered, "unsupported") || strings.Contains(lowered, "unsupported_value"))
}

func parseAIJSON(content string) (aiJSON, bool) {
	var parsed aiJSON
	if err := json.Unmarshal([]byte(content), &parsed); err == nil {
		return parsed, true
	}

	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end <= start {
		return aiJSON{}, false
	}

	if err := json.Unmarshal([]byte(content[start:end+1]), &parsed); err != nil {
		return aiJSON{}, false
	}

	return parsed, true
}

func normalizeEmotion(emotion string, allowed []string) string {
	emotion = strings.ToLower(strings.TrimSpace(emotion))
	for _, e := range allowed {
		if emotion == strings.ToLower(strings.TrimSpace(e)) {
			return emotion
		}
	}
	return ""
}

func fallbackEmotion(allowed []string) string {
	for _, e := range allowed {
		if strings.TrimSpace(e) != "" {
			return strings.ToLower(strings.TrimSpace(e))
		}
	}
	return "neutral"
}

func (c *Client) loadPromptFromFile() string {
	path := strings.TrimSpace(c.promptPath)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
