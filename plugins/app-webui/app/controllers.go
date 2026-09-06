package appcomponent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/workspace"
)

const sessionCompensationTimeout = 5 * time.Second

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
	Create(context.Context, string, workspace.Binding) (appbackend.Session, error)
	AssignWorkspace(context.Context, session.ID, workspace.Binding) (appbackend.Session, error)
	Get(context.Context, session.ID) (appbackend.Session, error)
	List(context.Context) ([]appbackend.Session, error)
	Rename(context.Context, session.ID, string) (appbackend.Session, error)
	Archive(context.Context, session.ID) (appbackend.Session, error)
	Restore(context.Context, session.ID) (appbackend.Session, error)
	Delete(context.Context, session.ID) error
	Fork(context.Context, session.ID, string) (appbackend.Session, error)
}

type defaultSessionController struct {
	store             session.Store
	manager           session.Manager
	query             session.Query
	workspaces        workspace.Manager
	workspaceResolver workspace.Resolver
}

func newSessionController(store session.Store, manager session.Manager, query session.Query, workspaces workspace.Manager, workspaceResolver workspace.Resolver) (sessionController, error) {
	if isNil(store) || isNil(manager) || isNil(query) || isNil(workspaces) || isNil(workspaceResolver) {
		return nil, fmt.Errorf("session store, manager, query and workspace capabilities are required: %w", appbackend.ErrInvalidConfig)
	}
	return &defaultSessionController{store: store, manager: manager, query: query, workspaces: workspaces, workspaceResolver: workspaceResolver}, nil
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

// project combines Session metadata with its Workspace Binding. A missing
// binding is a supported migration state that the Application projects as an
// empty Workspace so it can guide the user through the one-time assignment.
// All other Resolver failures remain authoritative and are returned.
func (c *defaultSessionController) project(ctx context.Context, metadata session.Metadata, metadataErr error) (appbackend.Session, error) {
	item, err := projectSession(metadata, metadataErr)
	if err != nil {
		return appbackend.Session{}, err
	}
	binding, err := c.workspaceResolver.Resolve(ctx, execution.Scope{SessionID: metadata.ID})
	if errors.Is(err, workspace.ErrNotAssigned) {
		return item, nil
	}
	if err != nil {
		return appbackend.Session{}, fmt.Errorf("resolve workspace for session %q: %w", metadata.ID, err)
	}
	item.Workspace = binding.Root
	return item, nil
}

func (c *defaultSessionController) Create(ctx context.Context, title string, binding workspace.Binding) (appbackend.Session, error) {
	metadata, err := c.store.Create(ctx, session.CreateRequest{Title: title})
	if err != nil {
		return appbackend.Session{}, err
	}
	if err := c.workspaces.Assign(ctx, metadata.ID, binding); err != nil {
		// Application-level compensation: the session must not remain alive
		// without its Workspace Binding. This is not a distributed transaction;
		// it removes the newly created session when the assignment failed. The
		// cleanup has its own bounded lifetime because the request Context may be
		// the reason assignment failed.
		base := context.Background()
		if ctx != nil {
			base = context.WithoutCancel(ctx)
		}
		cleanupCtx, cancel := context.WithTimeout(base, sessionCompensationTimeout)
		defer cancel()
		assignErr := fmt.Errorf("assign workspace to session %q: %w", metadata.ID, err)
		if deleteErr := c.manager.Delete(cleanupCtx, metadata.ID); deleteErr != nil {
			return appbackend.Session{}, errors.Join(assignErr, fmt.Errorf("compensate session %q creation: %w", metadata.ID, deleteErr))
		}
		return appbackend.Session{}, assignErr
	}
	item, _ := projectSession(metadata, nil)
	item.Workspace = binding.Root
	return item, nil
}

func (c *defaultSessionController) AssignWorkspace(ctx context.Context, id session.ID, binding workspace.Binding) (appbackend.Session, error) {
	metadata, err := c.manager.Get(ctx, id)
	if err != nil {
		return appbackend.Session{}, err
	}
	if err := c.workspaces.Assign(ctx, id, binding); err != nil {
		return appbackend.Session{}, err
	}
	item, _ := projectSession(metadata, nil)
	item.Workspace = binding.Root
	return item, nil
}

func (c *defaultSessionController) Get(ctx context.Context, id session.ID) (appbackend.Session, error) {
	metadata, err := c.manager.Get(ctx, id)
	return c.project(ctx, metadata, err)
}
func (c *defaultSessionController) List(ctx context.Context) ([]appbackend.Session, error) {
	items, err := c.query.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]appbackend.Session, len(items))
	for i, metadata := range items {
		result[i], err = c.project(ctx, metadata, nil)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (c *defaultSessionController) Rename(ctx context.Context, id session.ID, title string) (appbackend.Session, error) {
	metadata, err := c.manager.Rename(ctx, id, title)
	return c.project(ctx, metadata, err)
}
func (c *defaultSessionController) Archive(ctx context.Context, id session.ID) (appbackend.Session, error) {
	metadata, err := c.manager.Archive(ctx, id)
	return c.project(ctx, metadata, err)
}
func (c *defaultSessionController) Restore(ctx context.Context, id session.ID) (appbackend.Session, error) {
	metadata, err := c.manager.Restore(ctx, id)
	return c.project(ctx, metadata, err)
}
func (c *defaultSessionController) Delete(ctx context.Context, id session.ID) error {
	return c.manager.Delete(ctx, id)
}
func (c *defaultSessionController) Fork(ctx context.Context, id session.ID, title string) (appbackend.Session, error) {
	metadata, err := c.manager.Fork(ctx, id, session.ForkRequest{Title: title})
	if err != nil {
		return appbackend.Session{}, err
	}
	// The fork target inherits the source Workspace Binding from the
	// persistence implementation when the source is bound. An unbound legacy
	// source remains visible as unbound so the UI can request first assignment.
	return c.project(ctx, metadata, nil)
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
