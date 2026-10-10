package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type DaySummaryResult struct {
	Label   string `json:"label"`
	Summary string `json:"summary"`
}

const daySummarySystemPrompt = `你是 Memora 的状态日历整理模块。你会收到某一天的聊天原文，以及从这些对话里抽出的、按规则筛选过的有效线索。任务是用一个短词概括这一天，再写一句简短说明。

严格只输出一个 JSON 对象，不要解释、不要前言、不要代码块标记：
{"label": "两个字到四个字", "summary": "一到两句说明"}

规则：
1. 这是"根据当天的聊天整理出来的状态"，不是对用户真实全天的客观判定。summary 要让人看得出依据是什么，不要写成断言。
2. 线索不足以判断时，label 填"未知"，并在 summary 里如实说明这一天看不出明确状态。不要为了有个结果而挑一个词，也不要在证据不足时判定开心、平静或专注。
3. 标签要短、自然、像人说的话（轻松、焦虑、疲惫、忙碌、充实、起伏、缓和之类都可以），不要从固定词表里机械挑选，也不要生造书面词。
4. 同一天里出现了明显不同的状态时，用"起伏"这类说法概括，不要只取最后一条线索覆盖全天。
5. 日期归属以系统给出的线索为准：只有发生在那一天的才算那一天的。用户说"昨天很焦虑"说的是前一天；"明天要去爬山"是计划，不是已经发生的事。
6. 别人的状态、假设和引用都不是用户自己的当天状态。
7. 不推断人格、不下心理判断、不做诊断，也不要统计正面与负面情绪各出现了几次来推断全天。
8. summary 只写这一天，一到两句，不超过 60 个字。`

// DaySummary 不判断日期归属、也不决定该不该调用：这些在 service 层算好后才走到这里。
func (c *Client) DaySummary(ctx context.Context, input string) (*DaySummaryResult, error) {
	messages := []chatMessage{
		{Role: "system", Content: daySummarySystemPrompt},
		{Role: "user", Content: input},
	}

	text, err := c.chat(ctx, messages, true)
	if err != nil {
		return nil, err
	}

	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var result DaySummaryResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("解析当天状态输出失败: %w", err)
	}

	return &result, nil
}
