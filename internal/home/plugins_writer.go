package home

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
)

var pluginHeaderPattern = regexp.MustCompile(`(?m)^[ \t]*\[\[plugins\]\][ \t]*(?:#[^\r\n]*)?\r?$`)
var pluginAssignmentPattern = regexp.MustCompile(`^([ \t]*)(module|version|path)[ \t]*=`)

// marshalDesiredPreservingComments reuses and reorders existing raw plugin
// table blocks. Updated source assignments retain surrounding comments; newly
// added declarations use the stable writer format.
func marshalDesiredPreservingComments(path string, desired *builder.DesiredPlugins) ([]byte, error) {
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	existingData, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return desired.MarshalTOML()
	}
	if err != nil {
		return nil, err
	}
	existing, err := builder.ParseDesired(path)
	if err != nil {
		return nil, err
	}
	preamble, blocks, ok := splitPluginBlocks(string(existingData))
	if !ok || len(blocks) != len(existing.Plugins) {
		return desired.MarshalTOML()
	}
	type preserved struct {
		declaration builder.DesiredPlugin
		block       string
	}
	byModule := make(map[string]preserved, len(existing.Plugins))
	for index, declaration := range existing.Plugins {
		byModule[declaration.Module] = preserved{declaration: declaration, block: blocks[index]}
	}
	var output strings.Builder
	output.WriteString(preamble)
	if output.Len() > 0 && !strings.HasSuffix(output.String(), "\n") {
		output.WriteByte('\n')
	}
	for _, declaration := range desired.Plugins {
		if previous, exists := byModule[declaration.Module]; exists {
			block := previous.block
			if previous.declaration != declaration {
				block = updatePluginBlock(block, declaration)
			}
			output.WriteString(block)
			if !strings.HasSuffix(block, "\n") {
				output.WriteByte('\n')
			}
			continue
		}
		if output.Len() > 0 && !strings.HasSuffix(output.String(), "\n\n") {
			output.WriteByte('\n')
		}
		output.WriteString(renderPluginBlock(declaration))
	}
	return []byte(output.String()), nil
}

func splitPluginBlocks(document string) (string, []string, bool) {
	indices := pluginHeaderPattern.FindAllStringIndex(document, -1)
	if len(indices) == 0 {
		return document, nil, false
	}
	blocks := make([]string, len(indices))
	for index, position := range indices {
		end := len(document)
		if index+1 < len(indices) {
			end = indices[index+1][0]
		}
		blocks[index] = document[position[0]:end]
	}
	return document[:indices[0][0]], blocks, true
}

func updatePluginBlock(block string, declaration builder.DesiredPlugin) string {
	lines := strings.SplitAfter(block, "\n")
	found := map[string]bool{}
	result := make([]string, 0, len(lines)+1)
	moduleLine := -1
	for _, line := range lines {
		lineWithoutNewline := strings.TrimSuffix(line, "\n")
		newline := ""
		if strings.HasSuffix(line, "\n") {
			newline = "\n"
		}
		match := pluginAssignmentPattern.FindStringSubmatch(lineWithoutNewline)
		if match == nil {
			result = append(result, line)
			continue
		}
		key, indent := match[2], match[1]
		found[key] = true
		value := declaration.Module
		present := true
		switch key {
		case "version":
			value, present = declaration.Version, declaration.Version != ""
		case "path":
			value, present = declaration.Path, declaration.Path != ""
		}
		comment := inlineComment(lineWithoutNewline)
		if !present {
			if comment != "" {
				result = append(result, indent+comment+newline)
			}
			continue
		}
		rewritten := indent + key + " = " + strconv.Quote(value)
		if comment != "" {
			rewritten += " " + comment
		}
		result = append(result, rewritten+newline)
		if key == "module" {
			moduleLine = len(result)
		}
	}
	missingKey, missingValue := "", ""
	if declaration.Version != "" && !found["version"] {
		missingKey, missingValue = "version", declaration.Version
	}
	if declaration.Path != "" && !found["path"] {
		missingKey, missingValue = "path", declaration.Path
	}
	if missingKey != "" {
		line := missingKey + " = " + strconv.Quote(missingValue) + "\n"
		if moduleLine < 0 || moduleLine >= len(result) {
			result = append(result, line)
		} else {
			result = append(result, "")
			copy(result[moduleLine+1:], result[moduleLine:])
			result[moduleLine] = line
		}
	}
	return strings.Join(result, "")
}

func inlineComment(line string) string {
	equals := strings.IndexByte(line, '=')
	if equals < 0 {
		return ""
	}
	quote := byte(0)
	escaped := false
	for index := equals + 1; index < len(line); index++ {
		character := line[index]
		if quote == '"' && character == '\\' && !escaped {
			escaped = true
			continue
		}
		if (character == '"' || character == '\'') && quote == 0 {
			quote = character
			continue
		}
		if character == quote && !escaped {
			quote = 0
			continue
		}
		if character == '#' && quote == 0 {
			return strings.TrimSpace(line[index:])
		}
		escaped = false
	}
	return ""
}

func renderPluginBlock(declaration builder.DesiredPlugin) string {
	sourceKey, sourceValue := "version", declaration.Version
	if declaration.Path != "" {
		sourceKey, sourceValue = "path", declaration.Path
	}
	return fmt.Sprintf("[[plugins]]\nmodule = %s\n%s = %s\n", strconv.Quote(declaration.Module), sourceKey, strconv.Quote(sourceValue))
}
