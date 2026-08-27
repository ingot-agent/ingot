package usagedefault

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dlclark/regexp2"
	hftokenizer "github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/normalizer"
	"github.com/sugarme/tokenizer/pretokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

//go:embed assets/deepseek_v4_tokenizer.json.gz
var deepSeekV4TokenizerGZIP []byte

const deepSeekV4SplitPattern = `[!"#$%&'()*+,\-./:;<=>?@\[\\\]^_` + "`" + `{|}~][A-Za-z]+|[^\r\n\p{L}\p{P}\p{S}]?[\p{L}\p{M}]+| ?[\p{P}\p{S}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`

type deepSeekTokenizer struct {
	tokenizer *hftokenizer.Tokenizer
	added     *addedTokenTrie
}

type addedTokenTrie struct {
	terminal bool
	children map[byte]*addedTokenTrie
}

func newDeepSeekV4Tokenizer() (*deepSeekTokenizer, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(deepSeekV4TokenizerGZIP))
	if err != nil {
		return nil, fmt.Errorf("open embedded tokenizer: %w", err)
	}
	defer compressed.Close()
	var cfg hftokenizer.Config
	decoder := json.NewDecoder(io.LimitReader(compressed, 16*1024*1024))
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode embedded tokenizer: %w", err)
	}
	bpeModel, err := pretrained.CreateModel(&cfg)
	if err != nil {
		return nil, fmt.Errorf("create DeepSeek V4 BPE: %w", err)
	}
	thirdPattern, err := newRegexp2Pattern(deepSeekV4SplitPattern)
	if err != nil {
		return nil, fmt.Errorf("compile DeepSeek V4 pre-tokenizer: %w", err)
	}
	preTokenizer := pretokenizer.NewSequence([]hftokenizer.PreTokenizer{
		pretokenizer.NewSplit(normalizer.NewRegexpPattern(`\p{N}{1,3}`), normalizer.IsolatedBehavior, false),
		pretokenizer.NewSplit(normalizer.NewRegexpPattern(`[一-龥぀-ゟ゠-ヿ]+`), normalizer.IsolatedBehavior, false),
		pretokenizer.NewSplit(thirdPattern, normalizer.IsolatedBehavior, false),
		deepSeekByteLevel{},
	})
	instance := hftokenizer.NewTokenizer(bpeModel)
	instance.WithPreTokenizer(preTokenizer)
	added := &addedTokenTrie{children: make(map[byte]*addedTokenTrie)}
	for _, token := range cfg.AddedTokens {
		added.insert(token.Content)
	}
	return &deepSeekTokenizer{tokenizer: instance, added: added}, nil
}

func (t *deepSeekTokenizer) count(value string) (int64, error) {
	var count int64
	plainStart := 0
	for index := 0; index < len(value); {
		end, matched := t.added.longest(value, index)
		if !matched {
			index++
			continue
		}
		plain, err := t.countPlain(value[plainStart:index])
		if err != nil {
			return 0, err
		}
		count += plain + 1
		index = end
		plainStart = end
	}
	plain, err := t.countPlain(value[plainStart:])
	if err != nil {
		return 0, err
	}
	return count + plain, nil
}

func (t *deepSeekTokenizer) countPlain(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	encoded, err := t.tokenizer.EncodeSingle(value, false)
	if err != nil {
		return 0, err
	}
	return int64(len(encoded.GetIds())), nil
}

func (t *addedTokenTrie) insert(value string) {
	if value == "" {
		return
	}
	node := t
	for i := 0; i < len(value); i++ {
		next := node.children[value[i]]
		if next == nil {
			next = &addedTokenTrie{children: make(map[byte]*addedTokenTrie)}
			node.children[value[i]] = next
		}
		node = next
	}
	node.terminal = true
}

func (t *addedTokenTrie) longest(value string, start int) (int, bool) {
	node := t
	best := -1
	for i := start; i < len(value); i++ {
		node = node.children[value[i]]
		if node == nil {
			break
		}
		if node.terminal {
			best = i + 1
		}
	}
	return best, best >= 0
}

// regexp2Pattern adapts the one DeepSeek pre-tokenizer expression containing a
// negative lookahead. regexp2 reports rune offsets; sugarme's normalizer uses
// UTF-8 byte offsets, so FindMatches converts them explicitly.
type regexp2Pattern struct {
	expression *regexp2.Regexp
}

func newRegexp2Pattern(value string) (*regexp2Pattern, error) {
	expression, err := regexp2.Compile(value, regexp2.None)
	if err != nil {
		return nil, err
	}
	return &regexp2Pattern{expression: expression}, nil
}

func (p *regexp2Pattern) FindMatches(inside string) []normalizer.OffsetsMatch {
	if inside == "" {
		return []normalizer.OffsetsMatch{{Offsets: []int{0, 0}, Match: false}}
	}
	runeToByte := make([]int, 0, len(inside)+1)
	for index := range inside {
		runeToByte = append(runeToByte, index)
	}
	runeToByte = append(runeToByte, len(inside))
	match, err := p.expression.FindStringMatch(inside)
	if err != nil || match == nil {
		return []normalizer.OffsetsMatch{{Offsets: []int{0, len(inside)}, Match: false}}
	}
	result := make([]normalizer.OffsetsMatch, 0, 8)
	position := 0
	for match != nil {
		start := runeToByte[match.Index]
		end := runeToByte[match.Index+match.Length]
		if position < start {
			result = append(result, normalizer.OffsetsMatch{Offsets: []int{position, start}, Match: false})
		}
		result = append(result, normalizer.OffsetsMatch{Offsets: []int{start, end}, Match: true})
		position = end
		match, err = p.expression.FindNextMatch(match)
		if err != nil {
			break
		}
	}
	if position < len(inside) {
		result = append(result, normalizer.OffsetsMatch{Offsets: []int{position, len(inside)}, Match: false})
	}
	return result
}

var _ normalizer.Pattern = (*regexp2Pattern)(nil)

// deepSeekByteLevel implements the official ByteLevel pre-tokenizer with
// use_regex=false. sugarme's built-in ByteLevel always applies its own GPT-2
// split expression, which would split DeepSeek pieces such as "_bar" again.
type deepSeekByteLevel struct{}

func (deepSeekByteLevel) PreTokenize(pretokenized *hftokenizer.PreTokenizedString) (*hftokenizer.PreTokenizedString, error) {
	return pretokenized.Normalize(func(value *normalizer.NormalizedString) *normalizer.NormalizedString {
		var changes []normalizer.ChangeMap
		for _, current := range value.GetNormalized() {
			for index, currentByte := range []byte(string(current)) {
				change := 0
				if index > 0 {
					change = 1
				}
				changes = append(changes, normalizer.ChangeMap{
					RuneVal: pretokenizer.BytesChar[currentByte],
					Changes: change,
				})
			}
		}
		return value.Transform(changes, 0)
	}), nil
}

var _ hftokenizer.PreTokenizer = deepSeekByteLevel{}
