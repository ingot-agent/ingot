package usagedefault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/usage"
)

func (c *counter) CountInput(ctx context.Context, request usage.CountRequest) (usage.CountResult, error) {
	if ctx == nil {
		return usage.CountResult{}, fmt.Errorf("count model input: nil context: %w", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return usage.CountResult{}, err
	}
	owned := cloneRequest(request.Invocation)
	if err := validateRequest(owned, false); err != nil {
		return usage.CountResult{}, err
	}
	resolved, err := c.resolver.ResolveRequest(ctx, owned)
	if err != nil {
		return usage.CountResult{}, fmt.Errorf("resolve model request: %w", err)
	}
	resolved = cloneRequest(resolved)
	if err := validateRequest(resolved, true); err != nil {
		return usage.CountResult{}, err
	}
	selected, routeIndex, ok := selectProfile(c.routes, resolved.Provider, resolved.Model)
	if !ok {
		return usage.CountResult{}, fmt.Errorf("provider %q model %q has no matching route: %w", resolved.Provider, resolved.Model, ErrUnsupportedModel)
	}
	key, err := requestCacheKey(selected.Source(), resolved)
	if err != nil {
		return usage.CountResult{}, fmt.Errorf("build count cache key for route %d: %w: %w", routeIndex, ErrCountFailed, err)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return usage.CountResult{}, ErrClosed
	}
	if element, hit := c.cache[key]; hit {
		c.recent.MoveToFront(element)
		result := element.Value.(cacheEntry).result
		c.mu.Unlock()
		return result, nil
	}
	if pending, found := c.inflight[key]; found {
		c.mu.Unlock()
		select {
		case <-pending.done:
			return pending.result, pending.err
		case <-ctx.Done():
			return usage.CountResult{}, ctx.Err()
		}
	}
	pending := &flight{done: make(chan struct{})}
	c.inflight[key] = pending
	c.mu.Unlock()

	count, countErr := selected.CountInput(ctx, cloneRequest(resolved))
	result := usage.CountResult{}
	if countErr != nil {
		if errors.Is(countErr, context.Canceled) || errors.Is(countErr, context.DeadlineExceeded) {
			countErr = fmt.Errorf("profile %q: %w", selected.Source(), countErr)
		} else {
			countErr = fmt.Errorf("profile %q: %w: %w", selected.Source(), ErrCountFailed, countErr)
		}
	} else if count < 0 {
		countErr = fmt.Errorf("profile %q returned a negative count: %w", selected.Source(), ErrCountFailed)
	} else {
		result = usage.CountResult{
			InputTokens: count,
			Accuracy:    selected.Accuracy(),
			Source:      selected.Source(),
			Provider:    resolved.Provider,
			Model:       resolved.Model,
		}
	}

	c.mu.Lock()
	delete(c.inflight, key)
	pending.result = result
	pending.err = countErr
	if countErr == nil && !c.closed {
		c.addCache(key, result)
	}
	close(pending.done)
	c.mu.Unlock()
	return result, countErr
}

func (c *counter) addCache(key string, result usage.CountResult) {
	element := c.recent.PushFront(cacheEntry{key: key, result: result})
	c.cache[key] = element
	if c.recent.Len() <= c.capacity {
		return
	}
	oldest := c.recent.Back()
	delete(c.cache, oldest.Value.(cacheEntry).key)
	c.recent.Remove(oldest)
}

func requestCacheKey(source string, request model.Request) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(source))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(encoded)
	return hex.EncodeToString(digest.Sum(nil)), nil
}
