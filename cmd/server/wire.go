//go:build wireinject
// +build wireinject

package main

import (
	"git.pinquest.cn/ai-customer/internal/agent"
	"git.pinquest.cn/ai-customer/internal/callback"
	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/internal/data"
	"git.pinquest.cn/ai-customer/internal/dispatcher"
	"git.pinquest.cn/ai-customer/internal/khclient"
	"git.pinquest.cn/ai-customer/internal/message"
	"git.pinquest.cn/ai-customer/internal/router"
	"git.pinquest.cn/ai-customer/internal/store"
	"git.pinquest.cn/ai-customer/internal/wecom"
	"github.com/gin-gonic/gin"
	"github.com/google/wire"
)

func InitializeServer(cfg *config.Config) (*gin.Engine, error) {
	wire.Build(
		// Database
		data.NewDB,

		// Stores
		store.NewGroupStore,
		store.NewConversationStore,
		store.NewMessageStore,
		store.NewRobotStore,
		store.NewGroupMemberStore,

		// External clients
		ProvideWecomClient,
		ProvideKHClient,

		// Agent
		agent.NewToolExecutor,
		ProvideAgentService,

		// Message handler
		ProvideMessageHandler,

		// Dispatcher
		ProvideDispatcher,

		// Callback handler
		ProvideCallbackHandler,

		// Router
		router.SetupRouter,
	)
	return nil, nil
}

// ---- Wire providers ----

func ProvideWecomClient(cfg *config.Config) *wecom.Client {
	return wecom.NewClient(cfg.Wecom.APIHost, cfg.Wecom.AppKey, cfg.Wecom.AppSecret)
}

func ProvideKHClient(cfg *config.Config) *khclient.Client {
	return khclient.NewClient(cfg.KnowledgeHub.Host, cfg.KnowledgeHub.APIKey, cfg.KnowledgeHub.Timeout)
}

func ProvideAgentService(cfg *config.Config, executor *agent.ToolExecutor, kh *khclient.Client, gs *store.GroupStore, msgStore *store.MessageStore) *agent.Service {
	return agent.NewService(cfg.Agent, executor, kh, gs, msgStore)
}

func ProvideMessageHandler(
	agentSvc *agent.Service,
	cfg *config.Config,
	wc *wecom.Client,
	gs *store.GroupStore,
	gms *store.GroupMemberStore,
	rs *store.RobotStore,
	cs *store.ConversationStore,
	ms *store.MessageStore,
) *message.Handler {
	return message.NewHandler(agentSvc, cfg.Agent, wc, gs, gms, rs, cs, ms)
}

func ProvideDispatcher(mh *message.Handler, rs *store.RobotStore, gs *store.GroupStore, gms *store.GroupMemberStore, wc *wecom.Client) *dispatcher.Dispatcher {
	return dispatcher.NewDispatcher(mh, rs, gs, gms, wc)
}

func ProvideCallbackHandler(cfg *config.Config, d *dispatcher.Dispatcher) *callback.Handler {
	return callback.NewHandler(cfg.Wecom, d)
}
