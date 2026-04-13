# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**ai-customer** is a Go service that acts as a WeChat Work (企业微信) group chatbot backed by AI. It receives webhook callbacks from WeChat Work, processes @robot mentions in group chats, and uses OpenAI function calling to answer questions by searching a knowledge-hub service.

## Build & Run

```bash
# Install dependencies
go mod download

# Generate Wire dependency injection code (required after changing wire.go)
go generate ./cmd/server/

# Run the server
go run ./cmd/server/

# Build binary
go build -o ai-customer ./cmd/server/
```

**Configuration:** Copy `configs/config.example.yaml` to `configs/config.yaml` and fill in secrets. Viper supports `${ENV_VAR}` interpolation.

## Architecture

```
WeChat Work Callback → callback.Handler (verify signature + decrypt)
  → dispatcher.Dispatcher (route by event type)
    → message.Handler (filter @robot mentions, manage conversations)
      → agent.Service (OpenAI function calling loop)
        → Tools: search_knowledge, read_document, check_feature_tag, ask_clarification
          → khclient (knowledge-hub REST API)
      → wecom.Client (send reply back to group)
```

**Key design decisions:**
- Conversations are group-scoped — each WeChat group maintains its own conversation context
- The agent uses iterative function calling (up to `max_iterations`) with 4 tools
- Groups map to customers with feature tags (不同客户开通的功能不同), controlling which features are available
- When questions are vague, the agent asks clarifying questions instead of guessing

## Module Layout

- `cmd/server/` — Entry point + Google Wire DI setup
- `internal/agent/` — AI agent engine: OpenAI function calling loop (`service.go`) and tool definitions (`tools.go`)
- `internal/callback/` — WeChat Work webhook handler with crypto verification
- `internal/dispatcher/` — Event routing by type
- `internal/message/` — Group message processing (filters @robot mentions)
- `internal/store/` — Repository layer (GORM-based data access)
- `internal/model/` — Database models and event types
- `internal/khclient/` — HTTP client for knowledge-hub service
- `internal/wecom/` — WeChat Work API client
- `pkg/crypto/` — Signature verification + AES-CBC decryption
- `pkg/logger/` — Structured JSON logging (slog)

## Tech Stack

- **Go 1.24.1**, Gin (HTTP), GORM (ORM), PostgreSQL
- **Google Wire** for compile-time dependency injection
- **Viper** for YAML config with env var support
- **OpenAI API** for function calling agent

## Conventions

- Table names use singular form (e.g., `enterprise_group`, not `enterprise_groups`)
- Database schema is auto-migrated via GORM on startup
- No test files exist yet
