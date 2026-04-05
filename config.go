package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const defaultSystemPrompt = "你是一个负责整理 QQ 聊天记录的助手。请基于给出的聊天记录用中文输出准确、克制的总结，不要编造未出现的信息。优先提炼主题、结论、待办、时间地点人物，以及仍未解决的问题。若内容杂乱，请分段总结。总长度控制在 400 字以内。"

type Config struct {
	Server ServerConfig `json:"server"`
	Log    LogConfig    `json:"log"`
	OpenAI OpenAIConfig `json:"openai"`
	Bot    BotConfig    `json:"bot"`
}

type ServerConfig struct {
	Listen                   string `json:"listen"`
	WSPath                   string `json:"ws_path"`
	AccessToken              string `json:"access_token"`
	ReadHeaderTimeoutSeconds int    `json:"read_header_timeout_seconds"`
}

type LogConfig struct {
	Dir        string `json:"dir"`
	FilePrefix string `json:"file_prefix"`
}

type OpenAIConfig struct {
	BaseURL               string  `json:"base_url"`
	APIKey                string  `json:"api_key"`
	Model                 string  `json:"model"`
	RequestTimeoutSeconds int     `json:"request_timeout_seconds"`
	Temperature           float64 `json:"temperature"`
	SystemPrompt          string  `json:"system_prompt"`
}

type BotConfig struct {
	ProcessTimeoutSeconds int `json:"process_timeout_seconds"`
	MaxForwardDepth       int `json:"max_forward_depth"`
	SummaryInputLimit     int `json:"summary_input_limit"`
	MaxImages             int `json:"max_images"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config

	content, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(content, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.Server.Listen) == "" {
		c.Server.Listen = ":8080"
	}
	if strings.TrimSpace(c.Server.WSPath) == "" {
		c.Server.WSPath = "/ws"
	}
	if c.Server.ReadHeaderTimeoutSeconds <= 0 {
		c.Server.ReadHeaderTimeoutSeconds = 10
	}
	if strings.TrimSpace(c.Log.Dir) == "" {
		c.Log.Dir = "logs"
	}
	if strings.TrimSpace(c.Log.FilePrefix) == "" {
		c.Log.FilePrefix = "qq-summary-bot"
	}

	if c.OpenAI.RequestTimeoutSeconds <= 0 {
		c.OpenAI.RequestTimeoutSeconds = 60
	}
	if c.OpenAI.Temperature == 0 {
		c.OpenAI.Temperature = 0.2
	}
	if strings.TrimSpace(c.OpenAI.SystemPrompt) == "" {
		c.OpenAI.SystemPrompt = defaultSystemPrompt
	}

	if c.Bot.ProcessTimeoutSeconds <= 0 {
		c.Bot.ProcessTimeoutSeconds = 120
	}
	if c.Bot.MaxForwardDepth <= 0 {
		c.Bot.MaxForwardDepth = 8
	}
	if c.Bot.SummaryInputLimit <= 0 {
		c.Bot.SummaryInputLimit = 12000
	}
	if c.Bot.MaxImages <= 0 {
		c.Bot.MaxImages = 12
	}
}

func (c Config) validate() error {
	switch {
	case strings.TrimSpace(c.OpenAI.BaseURL) == "":
		return errors.New("openai.base_url is required")
	case strings.TrimSpace(c.OpenAI.APIKey) == "":
		return errors.New("openai.api_key is required")
	case strings.TrimSpace(c.OpenAI.Model) == "":
		return errors.New("openai.model is required")
	}

	return nil
}
