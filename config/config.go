package config

import (
	"context"

	"github.com/sethvargo/go-envconfig"
)

type Config struct {
	HTTPListenAddr     string   `env:"HTTP_LISTEN_ADDR, default=:8000"`
	PostgresConnString string   `env:"POSTGRES_CONN_STR, default=postgres://postgres:postgres@localhost:5432/talk_to_ugur?sslmode=disable"`
	AllowedCorsOrigins []string `env:"ALLOWED_CORS_ORIGINS, default=http://localhost:5173"`

	DeepSeekAPIKey      string  `env:"DEEPSEEK_API_KEY, required"`
	DeepSeekBaseURL     string  `env:"DEEPSEEK_BASE_URL, default=https://api.deepseek.com"`
	DeepSeekModel       string  `env:"DEEPSEEK_MODEL, default=deepseek-chat"`
	DeepSeekTemperature float64 `env:"DEEPSEEK_TEMPERATURE, default=0.7"`
	AIMaxHistory        int     `env:"AI_MAX_HISTORY, default=20"`

	AISystemPrompt     string   `env:"AI_SYSTEM_PROMPT, default=You are Ugur. You are chatting with a visitor on your personal website. Reply in the first person as Ugur. Be concise, friendly, and natural."`
	AISystemPromptPath string   `env:"AI_SYSTEM_PROMPT_PATH, default=./prompts/system.txt"`
	AIEmotions         []string `env:"AI_EMOTIONS, default=neutral,happy,sad,angry,confused,amused,thoughtful,excited"`

	RateLimitEnabled       bool `env:"RATE_LIMIT_ENABLED, default=true"`
	RateLimitRequests      int  `env:"RATE_LIMIT_REQUESTS, default=60"`
	RateLimitWindowSeconds int  `env:"RATE_LIMIT_WINDOW_SECONDS, default=60"`
	RateLimitBurst         int  `env:"RATE_LIMIT_BURST, default=10"`
	RateLimitMaxStrikes    int  `env:"RATE_LIMIT_MAX_STRIKES, default=5"`
	RateLimitBlockSeconds  int  `env:"RATE_LIMIT_BLOCK_SECONDS, default=600"`
}

func LoadConfig(ctx context.Context) (*Config, error) {
	cfg := &Config{}
	if err := envconfig.Process(ctx, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
