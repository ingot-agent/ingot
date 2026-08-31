package usagedefault

import (
	"context"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/usage"
)

const unicodeEstimateSource = "unicode-estimate-v1"

type profile interface {
	CountInput(context.Context, model.Request) (int64, error)
	Accuracy() usage.Accuracy
	Source() string
}

type unicodeEstimateProfile struct{}

func builtInProfiles() (map[string]profile, error) {
	deepSeekV4, err := newDeepSeekV4Profile()
	if err != nil {
		return nil, err
	}
	return map[string]profile{
		unicodeEstimateSource: unicodeEstimateProfile{},
		deepSeekV4Source:      deepSeekV4,
	}, nil
}

// CountInput estimates an OpenAI-style chat envelope. ASCII text is estimated
// at four bytes per token and non-ASCII runes at one token each. Fixed framing
// values deliberately remain part of this versioned, estimate-only profile.
func (unicodeEstimateProfile) CountInput(ctx context.Context, request model.Request) (int64, error) {
	count := int64(3)
	for _, message := range request.Messages {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var err error
		count, err = addCount(count, 4)
		if err != nil {
			return 0, err
		}
		messageText, ok := content.TextOnly(message.Content)
		if !ok {
			return 0, usage.ErrUnsupportedModel
		}
		for _, value := range []string{string(message.Role), messageText, message.Name, message.ToolCallID} {
			estimated, estimateErr := estimateText(ctx, value)
			if estimateErr != nil {
				return 0, estimateErr
			}
			count, err = addCount(count, estimated)
			if err != nil {
				return 0, err
			}
		}
		for _, call := range message.ToolCalls {
			count, err = addCount(count, 8)
			if err != nil {
				return 0, err
			}
			for _, value := range []string{call.ID, call.Name, string(call.Arguments)} {
				estimated, estimateErr := estimateText(ctx, value)
				if estimateErr != nil {
					return 0, estimateErr
				}
				count, err = addCount(count, estimated)
				if err != nil {
					return 0, err
				}
			}
		}
	}
	for _, definition := range request.Tools {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var err error
		count, err = addCount(count, 12)
		if err != nil {
			return 0, err
		}
		for _, value := range []string{definition.Name, definition.Description, string(definition.InputSchema)} {
			estimated, estimateErr := estimateText(ctx, value)
			if estimateErr != nil {
				return 0, estimateErr
			}
			count, err = addCount(count, estimated)
			if err != nil {
				return 0, err
			}
		}
	}
	return count, nil
}

func (unicodeEstimateProfile) Accuracy() usage.Accuracy { return usage.AccuracyEstimate }
func (unicodeEstimateProfile) Source() string           { return unicodeEstimateSource }

func estimateText(ctx context.Context, value string) (int64, error) {
	var asciiBytes, nonASCII int64
	processed := 0
	for len(value) > 0 {
		if processed%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		r, size := utf8.DecodeRuneInString(value)
		if r <= 0x7f {
			asciiBytes++
		} else {
			nonASCII++
		}
		value = value[size:]
		processed += size
	}
	return (asciiBytes+3)/4 + nonASCII, nil
}

func addCount(left, right int64) (int64, error) {
	if right < 0 || left > math.MaxInt64-right {
		return 0, errors.New("token count overflow")
	}
	return left + right, nil
}
