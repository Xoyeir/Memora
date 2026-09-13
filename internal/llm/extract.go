package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type MemoryCandidate struct {
	Content  string `json:"content"`
	Category string `json:"category"`
}

type ExtractionResult struct {
	Summary      string            `json:"summary"`
	Moods        []string          `json:"moods"`
	Events       []string          `json:"events"`
	Achievements []string          `json:"achievements"`
	Goals        []string          `json:"goals"`
	Gratitude    []string          `json:"gratitude"`
	Memories     []MemoryCandidate `json:"memories"`
}

const extractSystemPrompt = `你是一个个人记忆提取助手。用户会给你一段生活日记，你需要提取结构化信息。

严格只输出一个 JSON 对象，不要有任何解释、前言或 markdown 代码块标记，格式如下：
{
  "summary": "用一句话概括这篇日记",
  "moods": ["主要情绪"],
  "events": ["发生的重要事件"],
  "achievements": ["值得记录的成果"],
  "goals": ["用户表达的短期或长期目标"],
  "gratitude": ["值得感恩的人或事"],
  "memories": [
    {"content": "未来对话中值得再次想起的一条长期信息", "category": "project/goal/decision/person/problem/other"}
  ]
}

规则：
1. 没有内容的字段输出空数组 []。
2. memories 只提取「未来值得再次想起」的长期信息，不要复述日记本身，可以没有（空数组）。
3. 每个字段用简短短语，不要长篇大论。`

func (c *Client) ExtractJournal(ctx context.Context, content string) (*ExtractionResult, error) {
	messages := []chatMessage{
		{Role: "system", Content: extractSystemPrompt},
		{Role: "user", Content: content},
	}

	text, err := c.chat(ctx, messages, true)
	if err != nil {
		return nil, err
	}

	// 部分模型即使开了 json mode 也可能多包一层代码块标记，这里兜底去掉
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var result ExtractionResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("解析 LLM 输出 JSON 失败: %w", err)
	}

	return &result, nil
}
