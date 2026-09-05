// Package appcomponent provides the HTTP application component of the
// app.backend composite plugin.
package appcomponent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/invocation"
	"github.com/ingot-agent/ingot-abi/lifecycle"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/session"
)

// Dependencies contains capabilities consumed by the Web application.
type Dependencies struct {
	Backend      appbackend.Runtime
	Agent        ingotabi.Optional[agent.Runtime]
	Streaming    ingotabi.Optional[agent.StreamingRuntime]
	History      agent.History
	Store        session.Store
	Sessions     session.Manager
	SessionQuery session.Query
	Assets       ingotabi.Optional[asset.Store]
	Operations   []operation.Operation
	Invocation   invocation.Invocation
	Lifecycle    lifecycle.Controller
}

// Exports is empty because the HTTP application is a graph leaf.
type Exports struct{}

type application struct {
	config               appbackend.NormalizedBackendConfig
	backend              appbackend.Runtime
	agent                agentController
	sessions             sessionController
	turns                *turnRegistry
	operations           *operationController
	operationInvocations *operationRegistry
	assets               asset.Store
	server               *http.Server
	listener             net.Listener
	serveDone            chan struct{}
	serveErr             error
	sessionMu            sync.Mutex
}

// New validates dependencies, starts the HTTP server, and returns promptly.
func New(ctx context.Context, cfg appbackend.Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.Backend) || isNil(deps.Invocation) || isNil(deps.Lifecycle) {
		return Exports{}, nil, fmt.Errorf("construct app.backend app: %w", appbackend.ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if deps.Invocation.Mode() != invocation.ModeRun && deps.Invocation.Mode() != invocation.ModeCheck {
		return Exports{}, nil, fmt.Errorf("invalid invocation mode: %w", appbackend.ErrInvalidConfig)
	}
	normalized, err := cfg.Normalize()
	if err != nil {
		return Exports{}, nil, err
	}
	agentController, err := newAgentController(deps.Agent, deps.Streaming, deps.History)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct app.backend app: %v: %w", err, appbackend.ErrInvalidConfig)
	}
	sessionController, err := newSessionController(deps.Store, deps.Sessions, deps.SessionQuery)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct app.backend app: %v: %w", err, appbackend.ErrInvalidConfig)
	}
	if isNil(deps.Backend.Events()) || isNil(deps.Backend.Interactions()) {
		return Exports{}, nil, fmt.Errorf("backend events and interactions are required: %w", appbackend.ErrInvalidConfig)
	}
	operations, err := newOperationController(deps.Operations)
	if err != nil {
		return Exports{}, nil, err
	}
	if deps.Assets.Valid && isNil(deps.Assets.Value) {
		return Exports{}, nil, fmt.Errorf("nil asset store: %w", appbackend.ErrInvalidConfig)
	}
	if deps.Invocation.Mode() == invocation.ModeCheck {
		return Exports{}, nil, nil
	}
	listener, err := net.Listen("tcp", normalized.Address)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("listen on %s: %w", normalized.Address, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	instance := &application{
		config: normalized, backend: deps.Backend, agent: agentController, sessions: sessionController,
		operations: operations,
		listener:   listener, serveDone: make(chan struct{}),
	}
	instance.turns = newTurnRegistry(runCtx, agentController, deps.Backend.Events())
	instance.operationInvocations = newOperationRegistry(runCtx, operations, deps.Backend.Interactions(), deps.Backend.Events(), normalized.OperationRetention)
	if deps.Assets.Valid {
		instance.assets = deps.Assets.Value
	}
	instance.server = &http.Server{
		Handler:           instance.routes(),
		BaseContext:       func(net.Listener) context.Context { return runCtx },
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go instance.serve(deps.Lifecycle)
	cleanup := ingotabi.Cleanup(func(cleanupCtx context.Context) error {
		cancel()
		return instance.shutdown(cleanupCtx)
	})
	return Exports{}, cleanup, nil
}

func (a *application) serve(process lifecycle.Controller) {
	defer close(a.serveDone)
	a.serveErr = a.server.Serve(a.listener)
	if errors.Is(a.serveErr, http.ErrServerClosed) {
		a.serveErr = nil
	}
	if a.serveErr != nil {
		process.RequestShutdown(a.serveErr)
	}
}

func (a *application) shutdown(ctx context.Context) error {
	a.turns.stop()
	a.operationInvocations.stop()
	if ctx == nil {
		_ = a.server.Close()
		return context.Canceled
	}
	shutdownErr := a.server.Shutdown(ctx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, a.server.Close())
	}
	select {
	case <-a.serveDone:
		shutdownErr = errors.Join(shutdownErr, a.serveErr)
	case <-ctx.Done():
		return errors.Join(shutdownErr, ctx.Err())
	}
	select {
	case <-a.turns.done:
	case <-ctx.Done():
		return errors.Join(shutdownErr, ctx.Err())
	}
	select {
	case <-a.operationInvocations.done:
		return shutdownErr
	case <-ctx.Done():
		return errors.Join(shutdownErr, ctx.Err())
	}
}
