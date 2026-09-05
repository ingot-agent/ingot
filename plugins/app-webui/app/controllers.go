package appcomponent

import (
	"context"
	"fmt"
	"reflect"

	appbackend "github.com/ingot-agent/app-webui"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

type agentController interface {
	Capabilities() appbackend.AgentCapabilities
	Run(context.Context, agent.Turn) (agent.Execution, error)
	Stream(context.Context, agent.Turn, agent.StreamHandler) (agent.Execution, error)
	History(context.Context, session.ID) ([]model.Message, error)
}

type defaultAgentController struct {
	runtime   agent.Runtime
	streaming agent.StreamingRuntime
	history   agent.History
}

func newAgentController(runtime ingotabi.Optional[agent.Runtime], streaming ingotabi.Optional[agent.StreamingRuntime], history agent.History) (agentController, error) {
	if isNil(history) || (!runtime.Valid && !streaming.Valid) || (runtime.Valid && isNil(runtime.Value)) || (streaming.Valid && isNil(streaming.Value)) {
		return nil, fmt.Errorf("history and at least one agent execution capability are required: %w", appbackend.ErrInvalidConfig)
	}
	c := &defaultAgentController{history: history}
	if runtime.Valid {
		c.runtime = runtime.Value
	}
	if streaming.Valid {
		c.streaming = streaming.Value
	}
	return c, nil
}

func (c *defaultAgentController) Capabilities() appbackend.AgentCapabilities {
	return appbackend.AgentCapabilities{Run: c.runtime != nil, Stream: c.streaming != nil}
}

func (c *defaultAgentController) Run(ctx context.Context, turn agent.Turn) (agent.Execution, error) {
	if c.runtime == nil {
		return agent.Execution{}, appbackend.ErrCapabilityUnavailable
	}
	return c.runtime.Run(ctx, turn)
}

func (c *defaultAgentController) Stream(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (agent.Execution, error) {
	if c.streaming == nil {
		return agent.Execution{}, appbackend.ErrCapabilityUnavailable
	}
	return c.streaming.Stream(ctx, turn, handler)
}

func (c *defaultAgentController) History(ctx context.Context, id session.ID) ([]model.Message, error) {
	return c.history.Load(ctx, id)
}

type sessionController interface {
	Create(context.Context, string) (appbackend.Session, error)
	Get(context.Context, session.ID) (appbackend.Session, error)
	List(context.Context) ([]appbackend.Session, error)
	Rename(context.Context, session.ID, string) (appbackend.Session, error)
	Archive(context.Context, session.ID) (appbackend.Session, error)
	Restore(context.Context, session.ID) (appbackend.Session, error)
	Delete(context.Context, session.ID) error
	Fork(context.Context, session.ID, string) (appbackend.Session, error)
}

type defaultSessionController struct {
	store   session.Store
	manager session.Manager
	query   session.Query
}

func newSessionController(store session.Store, manager session.Manager, query session.Query) (sessionController, error) {
	if isNil(store) || isNil(manager) || isNil(query) {
		return nil, fmt.Errorf("session store, manager and query are required: %w", appbackend.ErrInvalidConfig)
	}
	return &defaultSessionController{store: store, manager: manager, query: query}, nil
}

func projectSession(value session.Metadata, err error) (appbackend.Session, error) {
	if err != nil {
		return appbackend.Session{}, err
	}
	result := appbackend.Session{ID: string(value.ID), Title: value.Title, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
	if value.ArchivedAt != nil {
		archived := *value.ArchivedAt
		result.ArchivedAt = &archived
	}
	return result, nil
}

func (c *defaultSessionController) Create(ctx context.Context, title string) (appbackend.Session, error) {
	return projectSession(c.store.Create(ctx, session.CreateRequest{Title: title}))
}
func (c *defaultSessionController) Get(ctx context.Context, id session.ID) (appbackend.Session, error) {
	return projectSession(c.manager.Get(ctx, id))
}
func (c *defaultSessionController) List(ctx context.Context) ([]appbackend.Session, error) {
	items, err := c.query.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]appbackend.Session, len(items))
	for i, item := range items {
		result[i], _ = projectSession(item, nil)
	}
	return result, nil
}
func (c *defaultSessionController) Rename(ctx context.Context, id session.ID, title string) (appbackend.Session, error) {
	return projectSession(c.manager.Rename(ctx, id, title))
}
func (c *defaultSessionController) Archive(ctx context.Context, id session.ID) (appbackend.Session, error) {
	return projectSession(c.manager.Archive(ctx, id))
}
func (c *defaultSessionController) Restore(ctx context.Context, id session.ID) (appbackend.Session, error) {
	return projectSession(c.manager.Restore(ctx, id))
}
func (c *defaultSessionController) Delete(ctx context.Context, id session.ID) error {
	return c.manager.Delete(ctx, id)
}
func (c *defaultSessionController) Fork(ctx context.Context, id session.ID, title string) (appbackend.Session, error) {
	return projectSession(c.manager.Fork(ctx, id, session.ForkRequest{Title: title}))
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
