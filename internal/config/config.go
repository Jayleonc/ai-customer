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

	// 预检索配置
	PreSearchStrategy         string  `mapstructure:"pre_search_strategy"`           // 预检索策略：semantic / keyword / hybrid（默认 hybrid）
	PreSearchTopK             int     `mapstructure:"pre_search_top_k"`              // 预检索返回数量（默认 20）
	PreSearchScoreThreshold   float64 `mapstructure:"pre_search_score_threshold"`    // 预检索分数阈值（默认 0.3）
	PreSearchMaxSnippets      int     `mapstructure:"pre_search_max_snippets"`       // 注入 context 的最大片段数（默认 5）
	PreSearchMaxSnippetLength int     `mapstructure:"pre_search_max_snippet_length"` // 每个片段最大字数（默认 300）

	// Query Rewrite 配置
	QueryRewriteMode  string `mapstructure:"query_rewrite_mode"`  // 重写模式：llm（默认）/ disabled（关闭）
	QueryRewriteModel string `mapstructure:"query_rewrite_model"` // LLM 重写使用的模型（空则复用主模型）

	// Runtime 配置
	TokenBudget        int `mapstructure:"token_budget"`         // 单次 LLM 调用最大输入 token 数（默认 6000）
	ToolTimeoutSeconds int `mapstructure:"tool_timeout_seconds"` // 工具调用超时秒数（默认 30）
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
	if cfg.Agent.ReplyMaxLength <= 0 {
		cfg.Agent.ReplyMaxLength = 800
	}
	if cfg.Agent.TokenBudget <= 0 {
		cfg.Agent.TokenBudget = 6000
	}
	if cfg.Agent.ToolTimeoutSeconds <= 0 {
		cfg.Agent.ToolTimeoutSeconds = 30
	}

	fmt.Printf("Configuration loaded from %s\n", v.ConfigFileUsed())
	return &cfg, nil
}
