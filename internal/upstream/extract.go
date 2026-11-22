package upstream

import (
	"encoding/json"
	"strings"
	"unicode/utf16"
)

// Delta 是抽取器吐出的增量文本。
type Delta struct {
	Content   string
	Reasoning string
}

// FinalResult 是流结束后的完整结果。
type FinalResult struct {
	Content   string
	Reasoning string
	Action    string
	Raw       string
	// Streamed 表示 Content 是否已经在流里逐字吐过。
	Streamed bool
}

const (
	roleNone = iota
	roleKey
	roleContent
	roleReasoning
	roleSkip
)

// Extractor 从上游「JSON 文本流」里增量抽取 taskCompletion / thinking。
//
// 上游 assistant 的 content 是一段被 SSE 逐字切开的 JSON：
//
//	{"action":"completion","thinking":"...","taskCompletion":"..."}
//
// 有时还会被 ```json 围栏包住、或前面带一段历史回显。这里用一个
// 只认顶层 key 的小状态机做增量解码，避免等整段结束才出字。
type Extractor struct {
	raw strings.Builder

	depth    int
	inStr    bool
	esc      bool
	uni      bool
	uniHex   []rune
	uniHigh  rune
	role     int
	strBuf   strings.Builder
	lastKey  string
	haveKey  bool
	awaitVal bool

	contentBuf   strings.Builder
	reasoningBuf strings.Builder
	streamed     bool
}

// NewExtractor 构造抽取器。
func NewExtractor() *Extractor { return &Extractor{} }

// Feed 喂入一段 content，返回可立即下发的增量。
func (e *Extractor) Feed(fragment string) []Delta {
	if fragment == "" {
		return nil
	}
	e.raw.WriteString(fragment)
	var out []Delta
	for _, r := range fragment {
		if d, ok := e.step(r); ok {
			out = append(out, d)
		}
	}
	return out
}

func (e *Extractor) step(r rune) (Delta, bool) {
	// 1) 字符串内部
	if e.inStr {
		if e.role == roleContent || e.role == roleReasoning {
			return e.stepTargetString(r)
		}
		return e.stepOtherString(r)
	}

	// 2) 字符串外部
	switch r {
	case '"':
		e.inStr = true
		e.esc = false
		e.strBuf.Reset()
		if e.awaitVal && e.haveKey && e.depth == 1 {
			switch e.lastKey {
			case "taskCompletion":
				e.role = roleContent
			case "thinking":
				e.role = roleReasoning
			default:
				e.role = roleSkip
			}
			e.awaitVal = false
			e.haveKey = false
		} else {
			e.role = roleKey
			e.awaitVal = false
			e.haveKey = false
		}
		return Delta{}, false
	case '{', '[':
		e.depth++
		e.haveKey = false
		e.awaitVal = false
		return Delta{}, false
	case '}', ']':
		if e.depth > 0 {
			e.depth--
		}
		e.haveKey = false
		e.awaitVal = false
		return Delta{}, false
	case ':':
		if e.haveKey && e.lastKey != "" {
			e.awaitVal = true
		}
		return Delta{}, false
	case ',':
		e.haveKey = false
		e.awaitVal = false
		return Delta{}, false
	case '\n', '\r', '\t', ' ':
		// 空白不改变「等待值」状态：JSON 常见 `"key": "value"` 写法。
		return Delta{}, false
	default:
		e.haveKey = false
		e.awaitVal = false
		return Delta{}, false
	}
}

// stepTargetString 处理 taskCompletion / thinking 的字符串值。
func (e *Extractor) stepTargetString(r rune) (Delta, bool) {
	if e.uni {
		e.uniHex = append(e.uniHex, r)
		if len(e.uniHex) < 4 {
			return Delta{}, false
		}
		hex := string(e.uniHex)
		e.uni = false
		e.uniHex = e.uniHex[:0]
		decoded, ok := decodeUnicodeEscape(hex, &e.uniHigh)
		if !ok {
			// 代理对前半段：等下一个 \u。
			return Delta{}, false
		}
		return e.emitRune(decoded)
	}
	if e.esc {
		e.esc = false
		switch r {
		case 'n':
			return e.emitRune('\n')
		case 't':
			return e.emitRune('\t')
		case 'r':
			return e.emitRune('\r')
		case 'b':
			return e.emitRune('\b')
		case 'f':
			return e.emitRune('\f')
		case 'u':
			e.uni = true
			e.uniHex = e.uniHex[:0]
			return Delta{}, false
		default:
			return e.emitRune(r)
		}
	}
	if r == '\\' {
		e.esc = true
		return Delta{}, false
	}
	if r == '"' {
		e.inStr = false
		e.role = roleNone
		return Delta{}, false
	}
	return e.emitRune(r)
}

// stepOtherString 处理 key / 普通字符串值。
func (e *Extractor) stepOtherString(r rune) (Delta, bool) {
	if e.esc {
		e.esc = false
		if e.role == roleKey {
			e.strBuf.WriteRune(r)
		}
		return Delta{}, false
	}
	if r == '\\' {
		e.esc = true
		return Delta{}, false
	}
	if r == '"' {
		e.inStr = false
		if e.role == roleKey {
			e.finishKey()
		}
		e.role = roleNone
		return Delta{}, false
	}
	if e.role == roleKey {
		e.strBuf.WriteRune(r)
	}
	return Delta{}, false
}

func (e *Extractor) finishKey() {
	key := e.strBuf.String()
	if e.depth == 1 && key != "" {
		e.lastKey = key
		e.haveKey = true
	} else {
		e.lastKey = ""
		e.haveKey = false
	}
}

func (e *Extractor) emitRune(r rune) (Delta, bool) {
	var d Delta
	switch e.role {
	case roleContent:
		e.contentBuf.WriteRune(r)
		e.streamed = true
		d.Content = string(r)
	case roleReasoning:
		e.reasoningBuf.WriteRune(r)
		d.Reasoning = string(r)
	}
	return d, d.Content != "" || d.Reasoning != ""
}

// Finish 解析整段 JSON，返回最终文本与动作。
func (e *Extractor) Finish() FinalResult {
	raw := e.raw.String()
	res := FinalResult{
		Content:   e.contentBuf.String(),
		Reasoning: e.reasoningBuf.String(),
		Raw:       raw,
	}
	if obj := findAssistantObject(raw); obj != nil {
		if s, ok := obj["action"].(string); ok {
			res.Action = s
		}
		if s, ok := obj["taskCompletion"].(string); ok && strings.TrimSpace(s) != "" {
			res.Content = s
		} else if s, ok := obj["question"].(string); ok && strings.TrimSpace(s) != "" {
			res.Content = s
			if opts, ok := obj["options"].([]any); ok && len(opts) > 0 {
				var parts []string
				for _, o := range opts {
					if str, ok := o.(string); ok {
						parts = append(parts, str)
					}
				}
				if len(parts) > 0 {
					res.Content += "\n\n可选：" + strings.Join(parts, " / ")
				}
			}
		}
		if s, ok := obj["thinking"].(string); ok && strings.TrimSpace(s) != "" {
			if strings.TrimSpace(res.Reasoning) == "" {
				res.Reasoning = s
			}
		}
	}
	if strings.TrimSpace(res.Content) == "" {
		res.Content = cleanupPlain(raw)
		res.Streamed = false
	} else {
		res.Streamed = e.streamed && res.Content == e.contentBuf.String()
	}
	return res
}

// findAssistantObject 在文本里找到最后一个包含 action/taskCompletion 的 JSON 对象。
func findAssistantObject(s string) map[string]any {
	var best map[string]any
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		if end, ok := matchObject(s, i); ok {
			var m map[string]any
			if json.Unmarshal([]byte(s[i:end]), &m) == nil {
				if _, has := m["action"]; has {
					best = m
				} else if _, has := m["taskCompletion"]; has {
					best = m
				}
			}
		}
	}
	return best
}

// matchObject 返回从 start 开始的平衡 JSON 对象结束位置（不含）。
func matchObject(s string, start int) (int, bool) {
	if start >= len(s) || s[start] != '{' {
		return 0, false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// cleanupPlain 去掉常见的 ```json 围栏。
func cleanupPlain(s string) string {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(t, "```json")
	t = strings.TrimPrefix(t, "```")
	t = strings.TrimSuffix(t, "```")
	return strings.TrimSpace(t)
}

// decodeUnicodeEscape 把 \uXXXX 转成 rune（含代理对）。
func decodeUnicodeEscape(hex string, high *rune) (rune, bool) {
	if len(hex) != 4 {
		return 0, false
	}
	var v rune
	for _, c := range hex {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v += c - '0'
		case c >= 'a' && c <= 'f':
			v += c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v += c - 'A' + 10
		default:
			return 0, false
		}
	}
	if v >= 0xD800 && v <= 0xDBFF {
		*high = v
		return 0, false
	}
	if v >= 0xDC00 && v <= 0xDFFF && *high != 0 {
		r := utf16.DecodeRune(*high, v)
		*high = 0
		return r, true
	}
	*high = 0
	return v, true
}
