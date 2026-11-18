package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

// commandOnlyContent reports whether content carries an injected slash command
// (plus Claude Code's synthetic blocks) and nothing a human typed. parse
// extracts a command from one text block.
func commandOnlyContent(content gjson.Result, parse func(string) (bool, string)) bool {
	switch {
	case content.Type == gjson.String:
		found, stripped := parse(content.String())
		return found && isOnlyInjectedCommandText(stripped)
	case content.Type == gjson.JSON && content.IsArray():
		seenCommand := false
		allSynthetic := true
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				allSynthetic = false
				return false
			}
			text := block.Get("text").String()
			if strings.TrimSpace(text) == "" || isClaudeCodeInjectedBlock(text) {
				return true
			}
			found, stripped := parse(text)
			if found && isOnlyInjectedCommandText(stripped) {
				seenCommand = true
				return true
			}
			allSynthetic = false
			return false
		})
		return seenCommand && allSynthetic
	default:
		return false
	}
}

// ackOnlyContent reports whether content carries only the router's own ack
// lines; blank text blocks are ignored. isAck recognizes one ack line.
func ackOnlyContent(content gjson.Result, isAck func(string) bool) bool {
	switch {
	case content.Type == gjson.String:
		return isAck(content.String())
	case content.Type == gjson.JSON && content.IsArray():
		seenAck := false
		allSynthetic := true
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				allSynthetic = false
				return false
			}
			text := block.Get("text").String()
			if strings.TrimSpace(text) == "" {
				return true
			}
			if isAck(text) {
				seenAck = true
				return true
			}
			allSynthetic = false
			return false
		})
		return seenAck && allSynthetic
	default:
		return false
	}
}
