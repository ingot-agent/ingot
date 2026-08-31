package usagedefault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

const (
	deepSeekV4Source         = "deepseek-v4-flash-api-default-thinking-v1"
	deepSeekHighEffortTokens = int64(79)
	deepSeekBOSToken         = "<｜begin▁of▁sentence｜>"
	deepSeekEOSToken         = "<｜end▁of▁sentence｜>"
	deepSeekUserToken        = "<｜User｜>"
	deepSeekAssistantToken   = "<｜Assistant｜>"
	deepSeekThinkingStart    = "<think>"
	deepSeekThinkingEnd      = "</think>"
	deepSeekDSMLToken        = "｜DSML｜"
	deepSeekToolCallsBlock   = "tool_calls"
	deepSeekToolResultStart  = "<tool_result>"
	deepSeekToolResultEnd    = "</tool_result>"
)

const deepSeekToolsTemplate = "## Tools\n\n" +
	"You have access to a set of tools to help answer the user's question. You can invoke tools by writing a \"<｜DSML｜tool_calls>\" block like the following:\n\n" +
	"<｜DSML｜tool_calls>\n" +
	"<｜DSML｜invoke name=\"$TOOL_NAME\">\n" +
	"<｜DSML｜parameter name=\"$PARAMETER_NAME\" string=\"true|false\">$PARAMETER_VALUE</｜DSML｜parameter>\n" +
	"...\n" +
	"</｜DSML｜invoke>\n" +
	"<｜DSML｜invoke name=\"$TOOL_NAME2\">\n" +
	"...\n" +
	"</｜DSML｜invoke>\n" +
	"</｜DSML｜tool_calls>\n\n" +
	"String parameters should be specified as is and set `string=\"true\"`. For all other types (numbers, booleans, arrays, objects), pass the value in JSON format and set `string=\"false\"`.\n\n" +
	"If thinking_mode is enabled (triggered by <think>), you MUST output your complete reasoning inside <think>...</think> BEFORE any tool calls or final response.\n\n" +
	"Otherwise, output directly after </think> with tool calls or final response.\n\n" +
	"### Available Tool Schemas\n\n%s\n\n" +
	"You MUST strictly follow the above defined tool name and parameter schemas to invoke tool calls."

type deepSeekV4Profile struct {
	tokenizer *deepSeekTokenizer
}

func newDeepSeekV4Profile() (*deepSeekV4Profile, error) {
	tokenizer, err := newDeepSeekV4Tokenizer()
	if err != nil {
		return nil, err
	}
	return &deepSeekV4Profile{tokenizer: tokenizer}, nil
}

func (p *deepSeekV4Profile) CountInput(ctx context.Context, request model.Request) (int64, error) {
	prompt, err := renderDeepSeekV4Prompt(ctx, request)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count, err := p.tokenizer.count(prompt)
	if err != nil {
		return 0, fmt.Errorf("tokenize DeepSeek V4 prompt: %w", err)
	}
	// The hosted V4 Flash API enables thinking with high effort by default.
	// Its server-side high-effort prefix is not present in encoding_dsv4.py;
	// live golden vectors currently show a stable 79-token addition for plain,
	// long, and tool-calling requests. Keep the profile estimate-only because
	// this provider-owned prefix may change without a tokenizer revision.
	count, err = addCount(count, deepSeekHighEffortTokens)
	if err != nil {
		return 0, fmt.Errorf("add DeepSeek V4 high-effort framing: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

// Accuracy remains estimate because the hosted API's default high-effort
// prefix is provider-owned and can change independently of the open assets.
func (*deepSeekV4Profile) Accuracy() usage.Accuracy { return usage.AccuracyEstimate }
func (*deepSeekV4Profile) Source() string           { return deepSeekV4Source }

type deepSeekMessage struct {
	role      model.Role
	content   string
	toolCalls []tool.Call
	blocks    []deepSeekUserBlock
}

type deepSeekUserBlock struct {
	toolResult bool
	toolCallID string
	content    string
}

func renderDeepSeekV4Prompt(ctx context.Context, request model.Request) (string, error) {
	messages, err := mergeDeepSeekToolMessages(request.Messages)
	if err != nil {
		return "", err
	}
	firstSystem := -1
	for i, message := range messages {
		if message.role == model.RoleSystem {
			firstSystem = i
			break
		}
	}
	if len(request.Tools) != 0 && firstSystem == -1 {
		messages = append([]deepSeekMessage{{role: model.RoleSystem}}, messages...)
		firstSystem = 0
	}
	sortDeepSeekToolResults(messages)
	lastUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].role == model.RoleUser {
			lastUser = i
			break
		}
	}
	dropThinking := len(request.Tools) == 0
	var prompt strings.Builder
	prompt.WriteString(deepSeekBOSToken)
	for i, message := range messages {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		switch message.role {
		case model.RoleSystem:
			prompt.WriteString(message.content)
			if i == firstSystem && len(request.Tools) != 0 {
				tools, err := renderDeepSeekTools(request.Tools)
				if err != nil {
					return "", err
				}
				prompt.WriteString("\n\n")
				prompt.WriteString(tools)
			}
		case model.RoleUser:
			prompt.WriteString(deepSeekUserToken)
			for blockIndex, block := range message.blocks {
				if blockIndex != 0 {
					prompt.WriteString("\n\n")
				}
				if block.toolResult {
					prompt.WriteString(deepSeekToolResultStart)
					prompt.WriteString(block.content)
					prompt.WriteString(deepSeekToolResultEnd)
				} else {
					prompt.WriteString(block.content)
				}
			}
		case model.RoleAssistant:
			if !dropThinking || i > lastUser {
				prompt.WriteString(deepSeekThinkingEnd)
			}
			prompt.WriteString(message.content)
			if len(message.toolCalls) != 0 {
				calls, err := renderDeepSeekToolCalls(message.toolCalls)
				if err != nil {
					return "", err
				}
				prompt.WriteString("\n\n")
				prompt.WriteString(calls)
			}
			prompt.WriteString(deepSeekEOSToken)
		default:
			return "", fmt.Errorf("unsupported DeepSeek V4 message role %q", message.role)
		}

		if i+1 < len(messages) && messages[i+1].role != model.RoleAssistant {
			continue
		}
		if message.role == model.RoleUser {
			prompt.WriteString(deepSeekAssistantToken)
			if !dropThinking || i >= lastUser {
				prompt.WriteString(deepSeekThinkingStart)
			} else {
				prompt.WriteString(deepSeekThinkingEnd)
			}
		}
	}
	return prompt.String(), nil
}

func mergeDeepSeekToolMessages(messages []model.Message) ([]deepSeekMessage, error) {
	merged := make([]deepSeekMessage, 0, len(messages))
	for _, message := range messages {
		messageText, ok := content.TextOnly(message.Content)
		if !ok {
			return nil, usage.ErrUnsupportedModel
		}
		switch message.Role {
		case model.RoleTool:
			block := deepSeekUserBlock{toolResult: true, toolCallID: message.ToolCallID, content: messageText}
			if len(merged) != 0 && merged[len(merged)-1].role == model.RoleUser {
				merged[len(merged)-1].blocks = append(merged[len(merged)-1].blocks, block)
			} else {
				merged = append(merged, deepSeekMessage{role: model.RoleUser, blocks: []deepSeekUserBlock{block}})
			}
		case model.RoleUser:
			block := deepSeekUserBlock{content: messageText}
			if len(merged) != 0 && merged[len(merged)-1].role == model.RoleUser {
				merged[len(merged)-1].blocks = append(merged[len(merged)-1].blocks, block)
			} else {
				merged = append(merged, deepSeekMessage{role: model.RoleUser, blocks: []deepSeekUserBlock{block}})
			}
		default:
			merged = append(merged, deepSeekMessage{role: message.Role, content: messageText, toolCalls: message.ToolCalls})
		}
	}
	return merged, nil
}

func sortDeepSeekToolResults(messages []deepSeekMessage) {
	order := map[string]int{}
	for i := range messages {
		message := &messages[i]
		if message.role == model.RoleAssistant && len(message.toolCalls) != 0 {
			order = make(map[string]int, len(message.toolCalls))
			for index, call := range message.toolCalls {
				order[call.ID] = index
			}
			continue
		}
		if message.role != model.RoleUser || len(message.blocks) < 2 || len(order) == 0 {
			continue
		}
		toolBlocks := make([]deepSeekUserBlock, 0, len(message.blocks))
		for _, block := range message.blocks {
			if block.toolResult {
				toolBlocks = append(toolBlocks, block)
			}
		}
		if len(toolBlocks) < 2 {
			continue
		}
		sort.SliceStable(toolBlocks, func(left, right int) bool {
			return order[toolBlocks[left].toolCallID] < order[toolBlocks[right].toolCallID]
		})
		next := 0
		for blockIndex := range message.blocks {
			if message.blocks[blockIndex].toolResult {
				message.blocks[blockIndex] = toolBlocks[next]
				next++
			}
		}
	}
}

func renderDeepSeekTools(definitions []tool.Definition) (string, error) {
	schemas := make([]string, len(definitions))
	for i, definition := range definitions {
		parameters, err := parseOrderedJSON(definition.InputSchema)
		if err != nil || parameters.kind != jsonObject {
			return "", fmt.Errorf("tool %q parameters are not a JSON object", definition.Name)
		}
		fields := []orderedJSONField{{name: "name", value: orderedJSONString(definition.Name)}}
		if definition.Description != "" {
			fields = append(fields, orderedJSONField{name: "description", value: orderedJSONString(definition.Description)})
		}
		fields = append(fields, orderedJSONField{name: "parameters", value: parameters})
		schemas[i] = renderPythonJSON(orderedJSONValue{kind: jsonObject, object: fields})
	}
	return fmt.Sprintf(deepSeekToolsTemplate, strings.Join(schemas, "\n")), nil
}

func renderDeepSeekToolCalls(calls []tool.Call) (string, error) {
	rendered := make([]string, len(calls))
	for i, call := range calls {
		arguments, err := parseOrderedJSON(call.Arguments)
		if err != nil || arguments.kind != jsonObject {
			return "", fmt.Errorf("tool call %q arguments are not a JSON object", call.Name)
		}
		parameters := make([]string, 0, len(arguments.object))
		for _, field := range arguments.object {
			isString := field.value.kind == jsonString
			value := renderPythonJSON(field.value)
			if isString {
				value = field.value.text
			}
			parameters = append(parameters, fmt.Sprintf(
				"<%sparameter name=\"%s\" string=\"%t\">%s</%sparameter>",
				deepSeekDSMLToken, field.name, isString, value, deepSeekDSMLToken,
			))
		}
		rendered[i] = fmt.Sprintf("<%sinvoke name=\"%s\">\n%s\n</%sinvoke>",
			deepSeekDSMLToken, call.Name, strings.Join(parameters, "\n"), deepSeekDSMLToken)
	}
	return fmt.Sprintf("<%s%s>\n%s\n</%s%s>",
		deepSeekDSMLToken, deepSeekToolCallsBlock, strings.Join(rendered, "\n"), deepSeekDSMLToken, deepSeekToolCallsBlock), nil
}

type orderedJSONKind uint8

const (
	jsonNull orderedJSONKind = iota
	jsonBoolean
	jsonNumber
	jsonString
	jsonArray
	jsonObject
)

type orderedJSONField struct {
	name  string
	value orderedJSONValue
}

type orderedJSONValue struct {
	kind    orderedJSONKind
	text    string
	boolean bool
	array   []orderedJSONValue
	object  []orderedJSONField
}

func orderedJSONString(value string) orderedJSONValue {
	return orderedJSONValue{kind: jsonString, text: value}
}

func parseOrderedJSON(raw json.RawMessage) (orderedJSONValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readOrderedJSON(decoder)
	if err != nil {
		return orderedJSONValue{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return orderedJSONValue{}, errors.New("JSON contains trailing data")
	}
	return value, nil
}

func readOrderedJSON(decoder *json.Decoder) (orderedJSONValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return orderedJSONValue{}, err
	}
	switch value := token.(type) {
	case nil:
		return orderedJSONValue{kind: jsonNull}, nil
	case bool:
		return orderedJSONValue{kind: jsonBoolean, boolean: value}, nil
	case json.Number:
		return orderedJSONValue{kind: jsonNumber, text: value.String()}, nil
	case string:
		return orderedJSONString(value), nil
	case json.Delim:
		switch value {
		case '[':
			result := orderedJSONValue{kind: jsonArray}
			for decoder.More() {
				item, err := readOrderedJSON(decoder)
				if err != nil {
					return orderedJSONValue{}, err
				}
				result.array = append(result.array, item)
			}
			if _, err := decoder.Token(); err != nil {
				return orderedJSONValue{}, err
			}
			return result, nil
		case '{':
			result := orderedJSONValue{kind: jsonObject}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return orderedJSONValue{}, err
				}
				name, ok := nameToken.(string)
				if !ok {
					return orderedJSONValue{}, errors.New("JSON object key is not a string")
				}
				fieldValue, err := readOrderedJSON(decoder)
				if err != nil {
					return orderedJSONValue{}, err
				}
				result.object = append(result.object, orderedJSONField{name: name, value: fieldValue})
			}
			if _, err := decoder.Token(); err != nil {
				return orderedJSONValue{}, err
			}
			return result, nil
		}
	}
	return orderedJSONValue{}, fmt.Errorf("unsupported JSON token %T", token)
}

func renderPythonJSON(value orderedJSONValue) string {
	switch value.kind {
	case jsonNull:
		return "null"
	case jsonBoolean:
		if value.boolean {
			return "true"
		}
		return "false"
	case jsonNumber:
		return value.text
	case jsonString:
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(value.text)
		return strings.TrimSuffix(encoded.String(), "\n")
	case jsonArray:
		items := make([]string, len(value.array))
		for i, item := range value.array {
			items[i] = renderPythonJSON(item)
		}
		return "[" + strings.Join(items, ", ") + "]"
	case jsonObject:
		fields := make([]string, len(value.object))
		for i, field := range value.object {
			fields[i] = renderPythonJSON(orderedJSONString(field.name)) + ": " + renderPythonJSON(field.value)
		}
		return "{" + strings.Join(fields, ", ") + "}"
	default:
		return ""
	}
}
