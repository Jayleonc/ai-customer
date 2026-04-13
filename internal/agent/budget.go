package agent

import (
	"strings"
	"unicode/utf8"
)

// estimateTokens 粗略估算文本的 token 数
// 中文约 1.5 tokens/字符，英文约 0.25 tokens/字符
// 混合文本用 1.2 tokens/rune 偏保守估算（宁多不少）
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return int(float64(utf8.RuneCountInString(text))*1.2) + 4
}

// estimateMessagesTokens 估算消息列表总 token 数
func estimateMessagesTokens(messages []chatMessage) int {
	total := 0
	for _, m := range messages {
		total += estimateTokens(m.Content)
		total += 4 // 每条消息的 role/分隔符开销
		for _, tc := range m.ToolCalls {
			total += estimateTokens(tc.Function.Arguments)
			total += estimateTokens(tc.Function.Name) + 8
		}
	}
	total += 3 // reply priming
	return total
}

// trimHistoryByDecay 按距离衰减策略裁剪历史消息
//   - keepFull: 最近 N 条完整保留
//   - summarizeRange: 接下来 N 条中 user 完整保留、assistant 截断到首句
//   - 更远的: 仅保留问题型 user 消息
func trimHistoryByDecay(history []chatMessage, keepFull, summarizeRange int) []chatMessage {
	total := len(history)
	if total == 0 {
		return history
	}

	result := make([]chatMessage, 0, total)
	for i, msg := range history {
		distFromEnd := total - 1 - i

		if distFromEnd < keepFull {
			// 最近几条：完整保留
			result = append(result, msg)
		} else if distFromEnd < keepFull+summarizeRange {
			// 中间段：user 完整保留，assistant 截断到首句
			if msg.Role == "user" {
				result = append(result, msg)
			} else if msg.Role == "assistant" {
				result = append(result, chatMessage{
					Role:    msg.Role,
					Content: truncateToFirstSentence(msg.Content, 80),
				})
			}
		} else {
			// 远端：仅保留问题型 user 消息
			if msg.Role == "user" && isQuestionLike(msg.Content) {
				result = append(result, msg)
			}
		}
	}
	return result
}

// truncateToFirstSentence 截取到第一个句末标点或换行，最多 maxRunes 字符
func truncateToFirstSentence(text string, maxRunes int) string {
	if text == "" {
		return text
	}

	sentenceEnders := []string{"。", ".", "！", "!", "？", "?", "\n"}
	minIdx := len(text)
	for _, sep := range sentenceEnders {
		idx := strings.Index(text, sep)
		if idx >= 0 && idx < minIdx {
			minIdx = idx + len(sep)
		}
	}
	if minIdx < len(text) {
		text = text[:minIdx]
	}

	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return text
}

// trimMessagesToBudget 当总 token 超预算时，从最老的历史消息开始裁剪
// 保护首条 system 消息和最后 2 条消息（当前 user + preSearch）不被裁剪
func trimMessagesToBudget(messages []chatMessage, maxTokens int) []chatMessage {
	if maxTokens <= 0 || estimateMessagesTokens(messages) <= maxTokens {
		return messages
	}

	// 从 index 1（最老的历史）开始删，保留至少 3 条（system + user + preSearch）
	for estimateMessagesTokens(messages) > maxTokens && len(messages) > 3 {
		messages = append(messages[:1], messages[2:]...)
	}
	return messages
}
