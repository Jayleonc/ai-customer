package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server       ServerConfig       `mapstructure:"server"`
	Log          LogConfig          `mapstructure:"log"`
	Database     DatabaseConfig     `mapstructure:"database"`
	Wecom        WecomConfig        `mapstructure:"wecom"`
	KnowledgeHub KnowledgeHubConfig `mapstructure:"knowledge_hub"`
	Agent        AgentConfig        `mapstructure:"agent"`
}

type ServerConfig struct {
	Port string `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type DatabaseConfig struct {
	DSN string `mapstructure:"dsn"`
}

// WecomConfig holds enterprise WeChat platform configuration.
type WecomConfig struct {
	APIHost   string         `mapstructure:"api_host"`
	AppKey    string         `mapstructure:"app_key"`
	AppSecret string         `mapstructure:"app_secret"`
	Callback  CallbackConfig `mapstructure:"callback"`
}

type CallbackConfig struct {
	Token  string `mapstructure:"token"`
	AESKey string `mapstructure:"aes_key"`
}

// KnowledgeHubConfig holds knowledge-hub API configuration.
type KnowledgeHubConfig struct {
	Host    string `mapstructure:"host"`
	APIKey  string `mapstructure:"api_key"`
	Timeout int    `mapstructure:"timeout"` // seconds
}

// AgentConfig holds AI agent configuration.
type AgentConfig struct {
	BaseURL        string  `mapstructure:"base_url"`
	APIKey         string  `mapstructure:"api_key"`
	Model          string  `mapstructure:"model"`
	Temperature    float64 `mapstructure:"temperature"`
	HistoryLimit   int     `mapstructure:"history_limit"`
	ReplyMaxLength int     `mapstructure:"reply_max_length"`

	// 图片理解配置：客户发图时，先把图片观察成可检索的客服上下文
	ImageUnderstandingMode string `mapstructure:"image_understanding_mode"` // observe / disabled
	VisionProvider         string `mapstructure:"vision_provider"`
	VisionBaseURL          string `mapstructure:"vision_base_url"`
	VisionAPIKey           string `mapstructure:"vision_api_key"`
	VisionModel            string `mapstructure:"vision_model"`
	VisionDetail           string `mapstructure:"vision_detail"`
	VisionTimeoutSeconds   int    `mapstructure:"vision_timeout_seconds"`
	VisionImageMaxBytes    int64  `mapstructure:"vision_image_max_bytes"`

	// 检索工具输出配置
	RetrievalMaxEvidence      int `mapstructure:"retrieval_max_evidence"`       // search_knowledge 最大证据数（默认 8）
	RetrievalEvidenceMaxChars int `mapstructure:"retrieval_evidence_max_chars"` // 单条证据最大字数；0 表示不做单条硬截断
	RetrievalContextBudget    int `mapstructure:"retrieval_context_budget"`     // search_knowledge 工具输出总字数预算（默认 6000）
	ReadDocumentMaxChars      int `mapstructure:"read_document_max_chars"`      // read_document 最大返回字数（默认 10000）

	// Runtime 配置
	TokenBudget        int `mapstructure:"token_budget"`         // 单次 LLM 调用最大输入 token 数（默认 10000）
	ToolTimeoutSeconds int `mapstructure:"tool_timeout_seconds"` // 工具调用超时秒数（默认 60）
}

func LoadConfig(configPath ...string) (*Config, error) {
	v := viper.New()

	path := "./configs"
	if len(configPath) > 0 && configPath[0] != "" {
		path = configPath[0]
	}

	v.AddConfigPath(path)
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			v.AddConfigPath("../configs")
			if err := v.ReadInConfig(); err != nil {
				return nil, fmt.Errorf("config file not found in %s or ../configs: %w", path, err)
			}
		} else {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Defaults
	if cfg.Server.Port == "" {
		cfg.Server.Port = "8080"
	}
	if cfg.Server.Mode == "" {
		cfg.Server.Mode = "debug"
	}
	if cfg.KnowledgeHub.Timeout <= 0 {
		cfg.KnowledgeHub.Timeout = 30
	}
	if cfg.Agent.Model == "" {
		cfg.Agent.Model = "gpt-4o-mini"
	}
	if cfg.Agent.Temperature == 0 {
		cfg.Agent.Temperature = 0.3
	}
	if cfg.Agent.HistoryLimit <= 0 {
		cfg.Agent.HistoryLimit = 20
	}
	if cfg.Agent.ImageUnderstandingMode == "" {
		cfg.Agent.ImageUnderstandingMode = "observe"
	}
	if cfg.Agent.VisionProvider == "" {
		cfg.Agent.VisionProvider = "openai"
	}
	if cfg.Agent.VisionBaseURL == "" {
		cfg.Agent.VisionBaseURL = cfg.Agent.BaseURL
	}
	if cfg.Agent.VisionAPIKey == "" {
		cfg.Agent.VisionAPIKey = cfg.Agent.APIKey
	}
	if cfg.Agent.VisionModel == "" {
		cfg.Agent.VisionModel = cfg.Agent.Model
	}
	if cfg.Agent.VisionDetail == "" {
		cfg.Agent.VisionDetail = "low"
	}
	if cfg.Agent.VisionTimeoutSeconds <= 0 {
		cfg.Agent.VisionTimeoutSeconds = 60
	}
	if cfg.Agent.VisionImageMaxBytes <= 0 {
		cfg.Agent.VisionImageMaxBytes = 5 * 1024 * 1024
	}
	if cfg.Agent.TokenBudget <= 0 {
		cfg.Agent.TokenBudget = 10000
	}
	if cfg.Agent.ToolTimeoutSeconds <= 0 {
		cfg.Agent.ToolTimeoutSeconds = 60
	}
	if cfg.Agent.RetrievalMaxEvidence <= 0 {
		cfg.Agent.RetrievalMaxEvidence = 8
	}
	if cfg.Agent.RetrievalContextBudget <= 0 {
		cfg.Agent.RetrievalContextBudget = 6000
	}
	if cfg.Agent.ReadDocumentMaxChars <= 0 {
		cfg.Agent.ReadDocumentMaxChars = 10000
	}

	fmt.Printf("Configuration loaded from %s\n", v.ConfigFileUsed())
	return &cfg, nil
}
