package translate

import (
	"strings"

	"aiand-os/router/internal/router"

	"github.com/tidwall/gjson"
)

// BetaCommandResult describes a leading /beta directive. Invalid is true
// when the command has arguments or trailing prompt text; /beta intentionally
// has one toggle-only form.
type BetaCommandResult struct {
	Invalid  bool
	Strategy router.Strategy
}

// ExtractBetaCommand scans the final user message for a leading /beta
// directive and strips it so the command is never forwarded upstream.
func (e *RequestEnvelope) ExtractBetaCommand() (BetaCommandResult, bool) {
	var result BetaCommandResult
	found := e.extractLeadingCommand(func(text string) (bool, string) {
		parsed, ok, stripped := parseBetaCommand(text)
		if ok {
			result = parsed
		}
		return ok, stripped
	})
	return result, found
}

// StripBetaArtifacts removes prior command-only /beta turns and the router's
// synthetic acknowledgements from model-visible history. The trailing user
// command is preserved so ExtractBetaCommand can still toggle the session.
func (e *RequestEnvelope) StripBetaArtifacts() int {
	switch e.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return 0
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}

	lastUserIdx := -1
	msgs.ForEach(func(key, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			lastUserIdx = int(key.Int())
		}
		return true
	})

	removed := 0
	removedCommands := make(map[int]struct{})
	rebuilt := make([]string, 0, len(msgs.Array()))
	msgs.ForEach(func(key, msg gjson.Result) bool {
		idx := int(key.Int())
		role := msg.Get("role").String()
		content := msg.Get("content")
		if role == "user" && idx != lastUserIdx && isBetaCommandOnlyContent(content) {
			removedCommands[idx] = struct{}{}
			removed++
			return true
		}
		if role == "assistant" {
			_, followsRemovedCommand := removedCommands[idx-1]
			if isBetaAckOnlyContent(content) || (followsRemovedCommand && isEmptyTextContent(content)) {
				removed++
				return true
			}
		}
		rebuilt = append(rebuilt, msg.Raw)
		return true
	})
	if removed == 0 {
		return 0
	}
	return e.setMessages(rebuilt, removed)
}

func isEmptyTextContent(content gjson.Result) bool {
	switch {
	case content.Type == gjson.String:
		return strings.TrimSpace(content.String()) == ""
	case content.Type == gjson.JSON && content.IsArray():
		empty := true
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" || strings.TrimSpace(block.Get("text").String()) != "" {
				empty = false
				return false
			}
			return true
		})
		return empty
	default:
		return false
	}
}

func parseBetaCommand(text string) (result BetaCommandResult, found bool, stripped string) {
	prefixEnd := leadingInjectedPrefixEnd(text)
	prefix := text[:prefixEnd]
	body := text[prefixEnd:]
	lines := strings.Split(body, "\n")
	commandLine := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			return BetaCommandResult{}, false, text
		}
		switch fields[0] {
		case "/beta":
			result.Strategy = router.StrategyHMMBeta
		case "/beta-llm":
			result.Strategy = router.StrategyHMMQwen
		default:
			return BetaCommandResult{}, false, text
		}
		commandLine = i
		result.Invalid = len(fields) != 1
		break
	}
	if commandLine < 0 {
		return BetaCommandResult{}, false, text
	}

	remaining := make([]string, 0, len(lines)-1)
	remaining = append(remaining, lines[:commandLine]...)
	remaining = append(remaining, lines[commandLine+1:]...)
	for _, line := range remaining {
		if strings.TrimSpace(line) != "" {
			result.Invalid = true
			break
		}
	}
	return result, true, strings.TrimSpace(prefix + strings.Join(remaining, "\n"))
}

func isBetaCommandOnlyContent(content gjson.Result) bool {
	return commandOnlyContent(content, func(text string) (bool, string) {
		_, found, stripped := parseBetaCommand(text)
		return found, stripped
	})
}

func isBetaAckOnlyContent(content gjson.Result) bool {
	return ackOnlyContent(content, isBetaAckText)
}

func isBetaAckText(text string) bool {
	switch strings.TrimSpace(text) {
	case "✦ **Aiand Router** → Beta enabled. Type /beta again to turn it off.",
		"✦ **Aiand Router** → Qwen beta enabled. Type /beta-llm again to turn it off.",
		"✦ **Aiand Router** → Qwen beta disabled. Stable routing restored.",
		"✦ **Aiand Router** → Qwen beta is unavailable for this session.",
		"✦ **Aiand Router** → Usage: /beta-llm",
		"✦ **Aiand Router** → " + BetaRetiredMessage,
		"✦ **Aiand Router** → Beta disabled. Stable routing restored.",
		"✦ **Aiand Router** → Beta is unavailable for this session.",
		"✦ **Aiand Router** → Usage: /beta":
		return true
	default:
		return false
	}
}
