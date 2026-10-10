package service

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Xoyeir/Memora/internal/config"
	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
)

// 真实模型样例核查：只打印不断言（需 MEMORA_REALCHECK=1，指定样例用 MEMORA_ONLY=14,15）；跑法：set -a && . ./.env && set +a && MEMORA_REALCHECK=1 go test ./internal/service -run TestFormationSamples -v -count=1
func TestFormationSamples(t *testing.T) {
	if os.Getenv("MEMORA_REALCHECK") != "1" {
		t.Skip("需要 MEMORA_REALCHECK=1 才会调用真实模型")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	client := llm.NewClient(cfg)

	// 基准日固定，便于与上次输出逐字对比。
	const mentionedOn = "2026-10-09"

	prior := model.LifeRecord{
		ID: 31, Kind: model.LifeKindActivity, State: model.LifeStateDone,
		Basis: model.LifeBasisExplicit, Subject: model.LifeSubjectUser,
		Content: "跑步了", OccurredOn: "2026-10-08",
		DatePrecision: model.LifeDateDay, Status: model.LifeStatusActive,
	}
	known := map[int64]bool{31: true}

	samples := []struct {
		id      string
		message string
		known   []model.LifeRecord
		expect  string
	}{
		{"1", "今天很焦虑", nil, "一条 mood/explicit，日期 2026-10-09"},
		{"2", "忙完这一阵，我终于松了口气", nil, "「松了口气」是直接感受表达，标 explicit 可以接受；日期不确定，原话要留下"},
		{"3", "帮我解释一下 Go 接口是什么", nil, "技术问题，不该产生任何记录"},
		{"4", "今天运动了", nil, "一条 activity/done，日期 2026-10-09"},
		{"5", "明天想运动", nil, "具体到明天的行为，按规则该是 activity/planned；曾经被记成 goal/wish，是判据分歧点"},
		{"6", "今天没运动", nil, "activity/negated，日期 2026-10-09"},
		{"7", "朋友今天去跑步了", nil, "subject=other，不是用户自己"},
		{"8", "假如我今天跑步了，会怎么样", nil, "假设，不该落库"},
		{"9", "我想成为更自律的人", nil, "goal，不是 activity"},
		{"10", "我的生日是五月八日", nil, "month_day=5/8，没有年份"},
		{"11", "我昨天跑步了", nil, "日期 2026-10-08"},
		{"12", "早上焦虑，晚上轻松多了", nil, "两条 mood，不合并成一条"},
		{"13", "说错了，我不是跑步，是散步", []model.LifeRecord{prior}, "指向 life:31 的纠正"},
		{"14", "2025年3月15日我参加了校运会", nil, "absolute，日期 2025-03-15"},
		{"15", "三年前我开始跑步", nil, "超出相对换算范围：日期未知，但原话「三年前」必须留下"},
		{"16", "看书老是被我搁置", nil, "长期状态概括，不该记成某天的 activity"},
		{"17", "最近一直很忙，忙完这一阵再说", nil, "泛化的忙碌阶段，不该记成某天的 activity"},
	}

	only := os.Getenv("MEMORA_ONLY")
	for i, s := range samples {
		if only != "" && !strings.Contains(","+only+",", ","+s.id+",") {
			continue
		}
		turnID := int64(900 + i)
		input := buildFormationInput(turnID, s.message, nil, nil, s.known)

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		start := time.Now()
		res, err := client.Formation(ctx, input)
		cancel()
		if err != nil {
			t.Errorf("样例 %s 调用失败: %v", s.id, err)
			continue
		}
		ops := planLifeRecords(res.LifeRecords, turnID, s.message, mentionedOn, known)

		raw, _ := json.Marshal(res.LifeRecords)
		t.Logf("\n=== 样例 %s | %q\n  关注：%s\n  耗时：%s\n  原始输出：%s",
			s.id, s.message, s.expect, time.Since(start).Round(time.Millisecond), raw)
		if len(ops) == 0 {
			t.Logf("  落库 0 条（候选 %d 条）", len(res.LifeRecords))
		}
		for _, op := range ops {
			on := op.OccurredOn
			if on == "" {
				on = "（未知）"
			}
			t.Logf("  落库 [%s/%s/%s/%s] %q 日期=%s 精度=%s 原话时间说法=%q 纠正=%d",
				op.Kind, op.State, op.Basis, op.Subject, op.Content,
				on, op.DatePrecision, op.DateText, op.CorrectsID)
		}
	}
}
