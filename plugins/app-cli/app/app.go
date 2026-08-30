// Package appcomponent provides the session-aware background CLI component of
// the app.cli composite plugin.
package appcomponent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	appcli "github.com/ingot-agent/app-cli"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/invocation"
	"github.com/ingot-agent/ingot-abi/lifecycle"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

// ErrInvalidConfig indicates invalid app-loop configuration or dependencies.
var ErrInvalidConfig = errors.New("invalid app.cli app config")

// Dependencies contains the capabilities consumed by the CLI loop.
type Dependencies struct {
	Agent       agent.Runtime
	History     agent.History
	Model       model.Runtime
	Interaction interaction.Channel
	Frontend    appcli.Frontend
	Store       session.MutableStore
	// Invocation is the runtime invocation metadata injected by the
	// generated runtime.
	Invocation invocation.Invocation
	// Lifecycle is the runtime shutdown request interface injected by the
	// generated runtime.
	Lifecycle lifecycle.Controller
}

// Exports is empty because app is a graph leaf.
type Exports struct{}

type application struct {
	agent            agent.Runtime
	history          agent.History
	model            model.Runtime
	interaction      interaction.Channel
	frontend         appcli.Frontend
	store            session.MutableStore
	invocationData   invocation.Invocation
	lifecycle        lifecycle.Controller
	inputPrompt      string
	initialTitle     string
	titleProvider    string
	titleModel       string
	showBanner       bool
	now              func() time.Time
	current          session.ID
	autoTitlePending bool
}

// New starts one instance-owned CLI loop and returns promptly.
func New(ctx context.Context, cfg appcli.Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.Agent) || isNil(deps.History) || isNil(deps.Model) || isNil(deps.Interaction) || isNil(deps.Frontend) || isNil(deps.Store) || isNil(deps.Invocation) || isNil(deps.Lifecycle) {
		return Exports{}, nil, fmt.Errorf("construct app.cli app: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if deps.Invocation.Mode() == invocation.ModeCheck {
		return Exports{}, nil, nil
	}
	if !utf8.ValidString(cfg.App.InitialSessionTitle) || !utf8.ValidString(cfg.App.TitleProvider) || !utf8.ValidString(cfg.App.TitleModel) || !utf8.ValidString(cfg.Interaction.InputPrompt) {
		return Exports{}, nil, fmt.Errorf("configured text must be valid UTF-8: %w", ErrInvalidConfig)
	}
	inputPrompt := cfg.Interaction.InputPrompt
	if inputPrompt == "" {
		inputPrompt = "> "
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	instance := &application{
		agent: deps.Agent, history: deps.History, model: deps.Model, interaction: deps.Interaction, frontend: deps.Frontend, store: deps.Store,
		invocationData: deps.Invocation, lifecycle: deps.Lifecycle,
		inputPrompt: inputPrompt, initialTitle: cfg.App.InitialSessionTitle,
		titleProvider: cfg.App.TitleProvider, titleModel: cfg.App.TitleModel,
		showBanner: cfg.App.ShowBanner, now: time.Now,
	}
	go func() {
		err := instance.loop(runCtx)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			instance.lifecycle.RequestShutdown(err)
		}
		done <- err
	}()
	cleanup := ingotabi.Cleanup(func(cleanupCtx context.Context) error {
		cancel()
		if cleanupCtx == nil {
			return context.Canceled
		}
		select {
		case err := <-done:
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		case <-cleanupCtx.Done():
			return cleanupCtx.Err()
		}
	})
	return Exports{}, cleanup, nil
}

func (a *application) loop(ctx context.Context) error {
	if a.showBanner {
		if err := a.interaction.Render(ctx, interaction.StatusEvent{Text: "ingot CLI"}); err != nil {
			return err
		}
	}
	if err := a.syncSession(ctx); err != nil {
		return err
	}
	for {
		line, err := a.frontend.ReadLine(ctx, a.inputPrompt)
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				a.lifecycle.RequestShutdown(nil)
				return nil
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				return err
			case errors.Is(err, appcli.ErrInputLimit), errors.Is(err, appcli.ErrInvalidInput):
				if renderErr := a.interaction.Render(ctx, interaction.ErrorEvent{Err: err}); renderErr != nil {
					return renderErr
				}
				continue
			default:
				return fmt.Errorf("read terminal input: %w", err)
			}
		}
		commandLine := strings.TrimSpace(line)
		if commandLine == "" {
			continue
		}
		if strings.HasPrefix(commandLine, "/") {
			exit, commandErr := a.command(ctx, commandLine)
			if commandErr != nil {
				if errors.Is(commandErr, context.Canceled) || errors.Is(commandErr, context.DeadlineExceeded) {
					return commandErr
				}
				if err := a.interaction.Render(ctx, interaction.ErrorEvent{Err: commandErr}); err != nil {
					return err
				}
			}
			if exit {
				a.lifecycle.RequestShutdown(nil)
				return nil
			}
			continue
		}
		if a.current == "" {
			title := temporarySessionTitle(line, a.initialTitle)
			if err := a.createSession(ctx, title); err != nil {
				if renderErr := a.interaction.Render(ctx, interaction.ErrorEvent{Err: err}); renderErr != nil {
					return renderErr
				}
				continue
			}
			a.autoTitlePending = true
		}
		exit, err := a.runTurn(ctx, line)
		if err != nil {
			return err
		}
		if exit {
			a.lifecycle.RequestShutdown(nil)
			return nil
		}
	}
}

func (a *application) runTurn(ctx context.Context, input string) (bool, error) {
	shouldGenerateTitle := a.autoTitlePending
	a.autoTitlePending = false
	if err := a.frontend.StartTurn(ctx, input); err != nil {
		return false, err
	}
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		result agent.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := a.agent.Run(turnCtx, agent.Turn{SessionID: a.current, Input: input})
		done <- outcome{result: result, err: err}
	}()
	state := appcli.TurnCompleted
	var turnErr error
	var result agent.Result
	interrupts := a.frontend.Interrupts()
	for {
		select {
		case completed := <-done:
			result, turnErr = completed.result, completed.err
			if turnErr != nil {
				if ctx.Err() != nil {
					return false, ctx.Err()
				}
				state = appcli.TurnFailed
			}
		case interrupt, ok := <-interrupts:
			if !ok {
				interrupts = nil
				continue
			}
			cancel()
			completed := <-done
			turnErr = completed.err
			state = appcli.TurnCanceled
			if finishErr := a.frontend.FinishTurn(ctx, state); finishErr != nil {
				return false, finishErr
			}
			if interrupt.Kind == appcli.InterruptExit {
				return true, nil
			}
			if err := a.syncSession(ctx); err != nil {
				return false, err
			}
			if err := a.interaction.Render(ctx, interaction.StatusEvent{Text: "turn canceled"}); err != nil {
				return false, err
			}
			return false, nil
		case <-ctx.Done():
			cancel()
			<-done
			return false, ctx.Err()
		}
		break
	}
	if err := a.frontend.FinishTurn(ctx, state); err != nil {
		return false, err
	}
	if turnErr == nil && shouldGenerateTitle {
		a.generateAndRenameTitle(ctx, input, result.Output)
	}
	if err := a.syncSession(ctx); err != nil {
		return false, err
	}
	if turnErr != nil {
		if err := a.interaction.Render(ctx, interaction.ErrorEvent{Err: fmt.Errorf("session %q: %w", a.current, turnErr)}); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (a *application) command(ctx context.Context, line string) (bool, error) {
	command, argument, _ := strings.Cut(line, " ")
	argument = strings.TrimSpace(argument)
	switch command {
	case "/exit":
		if argument != "" {
			return false, errors.New("usage: /exit")
		}
		return true, nil
	case "/new":
		a.autoTitlePending = false
		if argument == "" {
			a.current = ""
			if err := a.syncSession(ctx); err != nil {
				return false, err
			}
			return false, a.interaction.Render(ctx, interaction.StatusEvent{Text: "new session: send a message to start"})
		}
		title, err := manualSessionTitle(argument)
		if err != nil {
			return false, err
		}
		return false, a.createSession(ctx, title)
	case "/rename":
		if a.current == "" {
			return false, errors.New("no active session")
		}
		if argument == "" {
			return false, errors.New("usage: /rename <title>")
		}
		title, err := manualSessionTitle(argument)
		if err != nil {
			return false, err
		}
		if err := a.store.Rename(ctx, a.current, title); err != nil {
			return false, fmt.Errorf("rename session %q: %w", a.current, err)
		}
		a.autoTitlePending = false
		if err := a.syncSession(ctx); err != nil {
			return false, err
		}
		return false, a.interaction.Render(ctx, interaction.StatusEvent{Text: "renamed session to " + title})
	case "/use":
		if argument == "" {
			return false, errors.New("usage: /use <id>")
		}
		id := session.ID(argument)
		if _, err := a.history.Load(ctx, id); err != nil {
			return false, fmt.Errorf("load session %q: %w", id, err)
		}
		a.current = id
		a.autoTitlePending = false
		if err := a.syncSession(ctx); err != nil {
			return false, err
		}
		return false, a.interaction.Render(ctx, interaction.StatusEvent{Text: "using session " + argument})
	case "/list":
		if argument != "" {
			return false, errors.New("usage: /list")
		}
		items, err := a.store.List(ctx, session.Query{Limit: 100})
		if err != nil {
			return false, fmt.Errorf("list sessions: %w", err)
		}
		if len(items) == 0 {
			return false, a.interaction.Render(ctx, interaction.StatusEvent{Text: "no sessions"})
		}
		for _, item := range items {
			marker := " "
			if item.ID == a.current {
				marker = "*"
			}
			if err := a.interaction.Render(ctx, interaction.StatusEvent{Text: fmt.Sprintf("%s %s  %s", marker, item.ID, item.Title)}); err != nil {
				return false, err
			}
		}
		return false, nil
	case "/help":
		return false, a.interaction.Render(ctx, interaction.StatusEvent{Text: "/new [title]  /rename <title>  /use <id>  /list  /help  /exit"})
	default:
		return false, fmt.Errorf("unknown command %q", command)
	}
}

func (a *application) createSession(ctx context.Context, title string) error {
	if title == "" {
		title = a.initialTitle
	}
	if title == "" {
		title = "New Session"
	}
	id, err := a.store.Create(ctx, session.Metadata{Title: title, CreatedAt: a.now().UTC()})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	a.current = id
	if err := a.syncSession(ctx); err != nil {
		return err
	}
	return a.interaction.Render(ctx, interaction.StatusEvent{Text: "using session " + string(id)})
}

func (a *application) syncSession(ctx context.Context) error {
	summaries, err := a.store.List(ctx, session.Query{Limit: 100})
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	var messages []model.Message
	if a.current != "" {
		messages, err = a.history.Load(ctx, a.current)
		if err != nil {
			return fmt.Errorf("load session %q history: %w", a.current, err)
		}
	}
	return a.frontend.Sync(ctx, appcli.SessionView{Current: a.current, Sessions: summaries, Messages: messages})
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
