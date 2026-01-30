package ai

import (
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
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

type aiJSON struct {
	Reply   string `json:"reply"`
	Emotion string `json:"emotion"`
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		baseURL:      strings.TrimRight(cfg.DeepSeekBaseURL, "/"),
		apiKey:       cfg.DeepSeekAPIKey,
		model:        cfg.DeepSeekModel,
		temperature:  cfg.DeepSeekTemperature,
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
		Model:       c.model,
		Messages:    messages,
		Temperature: c.temperature,
		Stream:      false,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return Reply{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Reply{}, err
	}
	if c.apiKey == "" {
		return Reply{}, errors.New("missing DEEPSEEK_API_KEY")
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return Reply{}, fmt.Errorf("deepseek api error: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatResponse
	if err = json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Reply{}, err
	}

	if len(parsed.Choices) == 0 {
		return Reply{}, errors.New("deepseek api returned no choices")
	}

	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return Reply{}, errors.New("deepseek api returned empty content")
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
