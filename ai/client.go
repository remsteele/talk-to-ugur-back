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
	"strconv"
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
	formatInstruction := fmt.Sprintf("Respond ONLY with valid JSON and no extra text. The JSON must have keys 'emotion' and 'reply' in that order. 'emotion' must be one of: %s.", emotionList)
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

func (c *Client) StreamReply(ctx context.Context, history []db.ChatMessage, onChunk func(string) error, onEmotion func(string) error) (Reply, error) {
	messages := make([]chatMessage, 0, len(history)+1)

	emotionList := strings.Join(c.emotions, ", ")
	formatInstruction := fmt.Sprintf("Respond ONLY with valid JSON and no extra text. The JSON must have keys 'emotion' and 'reply' in that order. 'emotion' must be one of: %s.", emotionList)
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
	var refusalBuilder strings.Builder
	var rawBuilder strings.Builder
	parser := newJSONStreamParser(onChunk, onEmotion)

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
				rawBuilder.WriteString(choice.Delta.Content)
				if err := parser.Feed(choice.Delta.Content); err != nil {
					return Reply{}, err
				}
			}
			if choice.Delta.Refusal != "" {
				refusalBuilder.WriteString(choice.Delta.Refusal)
			}
		}
	}

	content := strings.TrimSpace(parser.Reply())
	if content == "" {
		rawContent := strings.TrimSpace(rawBuilder.String())
		if rawContent != "" {
			if parsed, ok := parseAIJSON(rawContent); ok && parsed.Reply != "" {
				content = strings.TrimSpace(parsed.Reply)
				if parser.Emotion() == "" {
					parser.SetEmotion(parsed.Emotion)
				}
			}
		}
	}
	if content == "" {
		if strings.TrimSpace(refusalBuilder.String()) != "" {
			return Reply{}, errors.New("openai api refused to answer")
		}
		return Reply{}, errors.New("openai api returned empty content")
	}

	emotion := normalizeEmotion(parser.Emotion(), c.emotions)
	if emotion == "" {
		emotion = fallbackEmotion(c.emotions)
	}

	return Reply{
		Text:    strings.TrimSpace(content),
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

type jsonStreamParser struct {
	onReply        func(string) error
	onEmotion      func(string) error
	replyBuilder   strings.Builder
	replyChunk     strings.Builder
	currentKey     strings.Builder
	currentValue   strings.Builder
	currentKeyName string
	emotion        string
	inString       bool
	expectValue    bool
	mode           string // "key" or "value"
	escape         bool
	unicodeLeft    int
	unicodeBuf     strings.Builder
}

func newJSONStreamParser(onReply func(string) error, onEmotion func(string) error) *jsonStreamParser {
	return &jsonStreamParser{
		onReply:   onReply,
		onEmotion: onEmotion,
	}
}

func (p *jsonStreamParser) Feed(text string) error {
	for _, r := range text {
		if p.unicodeLeft > 0 {
			p.unicodeBuf.WriteRune(r)
			p.unicodeLeft--
			if p.unicodeLeft == 0 {
				decoded, ok := decodeUnicode(p.unicodeBuf.String())
				p.unicodeBuf.Reset()
				if ok {
					if err := p.handleRune(decoded); err != nil {
						return err
					}
				}
			}
			continue
		}

		if p.inString {
			if p.escape {
				p.escape = false
				switch r {
				case '"', '\\', '/':
					if err := p.handleRune(r); err != nil {
						return err
					}
				case 'b':
					if err := p.handleRune('\b'); err != nil {
						return err
					}
				case 'f':
					if err := p.handleRune('\f'); err != nil {
						return err
					}
				case 'n':
					if err := p.handleRune('\n'); err != nil {
						return err
					}
				case 'r':
					if err := p.handleRune('\r'); err != nil {
						return err
					}
				case 't':
					if err := p.handleRune('\t'); err != nil {
						return err
					}
				case 'u':
					p.unicodeLeft = 4
				default:
					if err := p.handleRune(r); err != nil {
						return err
					}
				}
				continue
			}

			if r == '\\' {
				p.escape = true
				continue
			}
			if r == '"' {
				p.inString = false
				if p.mode == "key" {
					p.currentKeyName = p.currentKey.String()
					p.currentKey.Reset()
					p.expectValue = true
				} else if p.mode == "value" {
					if p.currentKeyName == "emotion" && p.emotion == "" {
						p.emotion = p.currentValue.String()
						if p.onEmotion != nil {
							if err := p.onEmotion(p.emotion); err != nil {
								return err
							}
						}
					}
					p.currentValue.Reset()
					p.expectValue = false
				}
				p.mode = ""
				continue
			}
			if err := p.handleRune(r); err != nil {
				return err
			}
			continue
		}

		switch r {
		case '"':
			p.inString = true
			if p.expectValue {
				p.mode = "value"
			} else {
				p.mode = "key"
			}
		case ':':
		case ',':
			p.expectValue = false
			p.mode = ""
		case '{', '}', ' ', '\n', '\r', '\t':
			continue
		}
	}

	if p.replyChunk.Len() > 0 {
		if p.onReply != nil {
			if err := p.onReply(p.replyChunk.String()); err != nil {
				return err
			}
		}
		p.replyChunk.Reset()
	}

	return nil
}

func (p *jsonStreamParser) handleRune(r rune) error {
	if p.mode == "key" {
		p.currentKey.WriteRune(r)
		return nil
	}
	if p.mode == "value" {
		if p.currentKeyName == "reply" {
			p.replyBuilder.WriteRune(r)
			p.replyChunk.WriteRune(r)
			return nil
		}
		if p.currentKeyName == "emotion" && p.emotion == "" {
			p.currentValue.WriteRune(r)
		}
	}
	return nil
}

func (p *jsonStreamParser) Reply() string {
	return p.replyBuilder.String()
}

func (p *jsonStreamParser) Emotion() string {
	return p.emotion
}

func (p *jsonStreamParser) SetEmotion(emotion string) {
	if p.emotion != "" {
		return
	}
	p.emotion = emotion
	if p.onEmotion != nil && emotion != "" {
		_ = p.onEmotion(emotion)
	}
}

func decodeUnicode(hexStr string) (rune, bool) {
	if len(hexStr) != 4 {
		return 0, false
	}
	val, err := strconv.ParseInt(hexStr, 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(val), true
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
