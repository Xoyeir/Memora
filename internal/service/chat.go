package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/repository"
)

// 幂等与并发相关的错误，handler 依此选择 HTTP 状态码。
var (
	ErrClientIDConflict = errors.New("相同请求标识携带了不同的内容")
	ErrTurnInProgress   = errors.New("该请求正在处理中")
	ErrTurnNotFound     = errors.New("找不到这一轮")
	ErrTurnNotComplete  = errors.New("这一轮的回复还没有成功")
)

// writeTimeout 是脱离请求取消后，写业务状态的时限。
const writeTimeout = 5 * time.Second

// 「相关 + 最近」合计 20 条；分「相关」是为补久远记忆盲区，不增总量与模型调用。
const (
	recentMemoryRefs  = 12
	relatedMemoryRefs = 8
)

// Formation 执行租约：模型调用最坏约 95 秒，取 5 分钟留余量，崩溃留下的 running 可被重新认领。
const formationLease = 5 * time.Minute

const (
	RecoverApplied        = "applied"
	RecoverAlreadyApplied = "already_applied"
	RecoverInProgress     = "in_progress"
)

// 生成一次执行的归属标识；租约判断靠「这一行还是不是我认领的那次」，每次认领都换新值。
func newExecOwner() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("owner-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (s *Service) Chat(ctx context.Context, clientID, userMessage string) (string, error) {
	turn, err := s.beginTurn(ctx, clientID, userMessage)
	if err != nil {
		return "", err
	}
	if turn.done != "" {
		return turn.done, nil
	}

	systemPrompt, historyMsgs, err := s.prepareTurn(ctx, turn.turn)
	if err != nil {
		return "", err
	}

	messages := buildAgentMessages(systemPrompt, historyMsgs, userMessage)
	messages, final, _ := s.runAgent(ctx, messages, s.clock.LocalDate(turn.turn.CreatedAt))

	if final == "" {
		if err := ctx.Err(); err != nil {
			return "", s.abortTurn(ctx, turn.turn.ID, "", err)
		}
		content, _, err := s.llm.ChatWithTools(ctx, messages, nil)
		if err != nil {
			return "", s.abortTurn(ctx, turn.turn.ID, "", err)
		}
		final = content
	}

	if err := s.repo.CompleteChatTurn(ctx, turn.turn.ID, final); err != nil {
		return "", fmt.Errorf("保存助手回复失败: %w", err)
	}
	return final, nil
}

func (s *Service) ChatStream(ctx context.Context, clientID, userMessage string,
	onRecall func([]model.Memory), onDelta func(string)) error {

	turn, err := s.beginTurn(ctx, clientID, userMessage)
	if err != nil {
		return err
	}
	if turn.done != "" {
		onDelta(turn.done)
		return nil
	}

	systemPrompt, historyMsgs, err := s.prepareTurn(ctx, turn.turn)
	if err != nil {
		return err
	}

	messages := buildAgentMessages(systemPrompt, historyMsgs, userMessage)
	messages, _, recalled := s.runAgent(ctx, messages, s.clock.LocalDate(turn.turn.CreatedAt))

	if len(recalled) > 0 {
		onRecall(recalled)
	}

	if err := ctx.Err(); err != nil {
		return s.abortTurn(ctx, turn.turn.ID, "", err)
	}

	reply, err := s.llm.StreamMessages(ctx, messages, onDelta)
	if err != nil {
		return s.abortTurn(ctx, turn.turn.ID, reply, err)
	}

	if err := s.repo.CompleteChatTurn(ctx, turn.turn.ID, reply); err != nil {
		return fmt.Errorf("保存助手回复失败: %w", err)
	}
	return nil
}

// 比送进模型的上下文多带回断掉一半的回复；partial 可显示，但不能当完整回答进下一轮。
func (s *Service) ChatHistory(ctx context.Context, limit int) ([]model.ChatMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repo.ListConversationForDisplay(ctx, limit)
}

// turnRef 的 done 非空表示这一轮此前已经成功结束，其值就是当时保存的回复。
type turnRef struct {
	turn *model.ChatTurn
	done string
}

func (s *Service) beginTurn(ctx context.Context, clientID, userMessage string) (*turnRef, error) {
	turn, err := s.repo.EnsureUserMessage(ctx, clientID, userMessage)
	if err != nil {
		return nil, fmt.Errorf("保存用户消息失败: %w", err)
	}

	if turn.Content != userMessage {
		return nil, ErrClientIDConflict
	}

	if turn.Status == model.TurnStatusOK {
		reply, err := s.repo.GetReply(ctx, turn.ID)
		if err != nil {
			return nil, fmt.Errorf("读取已保存回复失败: %w", err)
		}
		if reply != "" {
			log.Printf("chat: 轮次 %d 已成功结束，重放已保存的回复", turn.ID)
			return &turnRef{turn: turn, done: reply}, nil
		}
	}

	inProgress, err := s.repo.FormationInProgress(ctx, turn.ID, int(formationLease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("检查处理状态失败: %w", err)
	}
	if inProgress {
		return nil, ErrTurnInProgress
	}

	return &turnRef{turn: turn}, nil
}

func (s *Service) prepareTurn(ctx context.Context, turn *model.ChatTurn) (string, []llm.Message, error) {
	history, err := s.repo.ListChatMessages(ctx, turn.ID, 20)
	if err != nil {
		return "", nil, fmt.Errorf("读取历史失败: %w", err)
	}

	formation, err := s.tryFormation(ctx, turn, history)
	if err != nil {
		// 记忆写入失败不影响本轮回复，只记录；取消由调用方检查 ctx.Err()。
		log.Printf("chat: 轮次 %d 的记忆写入没有完成（不影响回复）: %v", turn.ID, err)
	}

	historyMsgs := make([]llm.Message, len(history))
	for i, m := range history {
		historyMsgs[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	return buildResponseSystemPrompt(formation, s.clock.LocalDate(turn.CreatedAt)), historyMsgs, nil
}

// 拿不到执行权不是错误：说明这一轮已写过或另一有效执行者正在处理，都不重跑也不报错。
func (s *Service) tryFormation(ctx context.Context, turn *model.ChatTurn, history []model.ChatMessage) (*llm.FormationResult, error) {
	owner := newExecOwner()
	claimed, err := s.repo.ClaimFormation(ctx, turn.ID, owner, int(formationLease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("认领记忆写入失败: %w", err)
	}
	if !claimed {
		return nil, nil
	}
	return s.runFormation(ctx, turn, history, owner)
}

// 只用当前消息检索「相关 + 最近」；只给最近会留盲区，久远记忆会让改写变成矛盾的新记忆。
func (s *Service) formationMemories(ctx context.Context, turn *model.ChatTurn) ([]model.Memory, error) {
	recent, err := s.repo.ListMemories(ctx, recentMemoryRefs)
	if err != nil {
		return nil, err
	}
	related, err := s.repo.SearchMemories(ctx, turn.Content, relatedMemoryRefs)
	if err != nil {
		return nil, err
	}

	merged := make([]model.Memory, 0, len(recent)+len(related))
	seen := make(map[int64]bool, len(recent)+len(related))
	for _, list := range [][]model.Memory{recent, related} {
		for _, m := range list {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			merged = append(merged, m)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID > merged[j].ID })
	return merged, nil
}

// 模型调用在事务外；通过校验的长期记忆与生活记录连同 formation_status=applied 在同一事务提交，任一失败整批回滚。
func (s *Service) runFormation(ctx context.Context, turn *model.ChatTurn, history []model.ChatMessage, owner string) (*llm.FormationResult, error) {
	memories, err := s.formationMemories(ctx, turn)
	if err != nil {
		s.abandonFormation(ctx, turn.ID, owner)
		return nil, fmt.Errorf("读取记忆失败: %w", err)
	}

	// 送进 Formation 供模型引用做纠正，只含用户本人且仍然成立的记录。
	lifeRecords, err := s.repo.ListRecentLifeRecords(ctx, recentLifeRecordRefs)
	if err != nil {
		s.abandonFormation(ctx, turn.ID, owner)
		return nil, fmt.Errorf("读取生活记录失败: %w", err)
	}

	formation, err := s.llm.Formation(ctx, buildFormationInput(turn.ID, turn.Content, history, memories, lifeRecords))
	if err != nil {
		s.abandonFormation(ctx, turn.ID, owner)
		return nil, err
	}

	ops := planMemoryOps(formation.MemoryActions, turn.ID, memories)

	// 「昨天」以这条消息落库那天为基准，不是 time.Now()：重试/恢复读到的是同一行的 created_at。
	mentionedOn := s.clock.LocalDate(turn.CreatedAt)
	knownLife := make(map[int64]bool, len(lifeRecords))
	for _, r := range lifeRecords {
		knownLife[r.ID] = true
	}
	lifeOps := planLifeRecords(formation.LifeRecords, turn.ID, turn.Content, mentionedOn, knownLife)

	if err := s.repo.ApplyFormation(ctx, turn.ID, owner, ops, lifeOps); err != nil {
		if errors.Is(err, repository.ErrFormationLeaseLost) {
			// 已经被别人接手并写完了，这不是失败，也不该把状态降回去。
			log.Printf("chat: 轮次 %d 的记忆写入归属已失效，放弃本次结果", turn.ID)
			return nil, err
		}
		s.abandonFormation(ctx, turn.ID, owner)
		return nil, fmt.Errorf("写入记忆失败: %w", err)
	}

	return formation, nil
}

// 用脱离请求取消的短超时 context：请求已取消也要放掉 running，否则只能等租约过期。
func (s *Service) abandonFormation(ctx context.Context, turnID int64, owner string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := s.repo.FailFormation(writeCtx, turnID, owner); err != nil {
		log.Printf("chat: 标记轮次 %d 的记忆写入失败状态时出错: %v", turnID, err)
	}
}

// partial 是已流给用户的内容，必须落库，否则「回复断流」会被误当成「整轮没发生」。
func (s *Service) abortTurn(ctx context.Context, turnID int64, partial string, cause error) error {
	status := model.TurnStatusFailed
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status = model.TurnStatusCanceled
	}

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := s.repo.FailChatTurn(writeCtx, turnID, status, partial); err != nil {
		log.Printf("chat: 记录轮次 %d 的中断状态失败: %v", turnID, err)
	}
	return cause
}

func buildAgentMessages(systemPrompt string, historyMsgs []llm.Message, userMessage string) []llm.Message {
	messages := make([]llm.Message, 0, len(historyMsgs)+2)
	messages = append(messages, llm.Message{Role: "system", Content: systemPrompt})
	messages = append(messages, historyMsgs...)
	messages = append(messages, llm.Message{Role: "user", Content: userMessage})
	return messages
}

// 只做校验不碰库：解析不出引用/指向不存在的记忆/缺必填字段的 action 在此丢弃，其余照常执行。
func planMemoryOps(actions []llm.MemoryAction, userChatID int64, memories []model.Memory) []model.MemoryOp {
	known := make(map[int64]bool, len(memories))
	for _, m := range memories {
		known[m.ID] = true
	}

	ops := make([]model.MemoryOp, 0, len(actions))
	for _, action := range actions {
		switch action.Action {
		case "FORM":
			if action.Candidate == nil || action.Candidate.Content == "" {
				log.Printf("formation: FORM 缺少 candidate，已拒绝")
				continue
			}
			ops = append(ops, model.MemoryOp{
				Kind:          model.OpForm,
				Content:       action.Candidate.Content,
				Category:      action.Candidate.Category,
				ChatMessageID: userChatID,
			})

		case "REINFORCE":
			memoryID, ok := parseRef(action.Ref, "memory:")
			if !ok || !known[memoryID] {
				log.Printf("formation: REINFORCE 引用了无效的记忆 %q，已拒绝", action.Ref)
				continue
			}
			// 一次 REINFORCE 只产生一条证据：证据必须且只能是本轮这条 user 消息。
			evidence := false
			for _, ref := range action.EvidenceRefs {
				if chatID, ok := parseRef(ref, "chat:"); ok && chatID == userChatID {
					evidence = true
					break
				}
			}
			if !evidence {
				log.Printf("formation: REINFORCE 的 evidence_refs 没有指向本轮消息，已拒绝")
				continue
			}
			ops = append(ops, model.MemoryOp{
				Kind:          model.OpReinforce,
				MemoryID:      memoryID,
				ChatMessageID: userChatID,
			})

		case "SUPERSEDE":
			memoryID, ok := parseRef(action.Ref, "memory:")
			if !ok || !known[memoryID] {
				log.Printf("formation: SUPERSEDE 引用了无效的记忆 %q，已拒绝", action.Ref)
				continue
			}
			if action.Candidate == nil || action.Candidate.Content == "" {
				log.Printf("formation: SUPERSEDE 缺少候选内容，已拒绝")
				continue
			}
			ops = append(ops, model.MemoryOp{
				Kind:          model.OpSupersede,
				MemoryID:      memoryID,
				Content:       action.Candidate.Content,
				Category:      action.Candidate.Category,
				ChatMessageID: userChatID,
			})

		case "CORRECT":
			memoryID, ok := parseRef(action.Ref, "memory:")
			if !ok || !known[memoryID] {
				log.Printf("formation: CORRECT 引用了无效的记忆 %q，已拒绝", action.Ref)
				continue
			}
			if action.Candidate != nil && action.Candidate.Content != "" {
				ops = append(ops, model.MemoryOp{
					Kind:          model.OpCorrectRewrite,
					MemoryID:      memoryID,
					Content:       action.Candidate.Content,
					Category:      action.Candidate.Category,
					ChatMessageID: userChatID,
				})
			} else {
				ops = append(ops, model.MemoryOp{
					Kind:     model.OpCorrectDrop,
					MemoryID: memoryID,
				})
			}

		default:
			log.Printf("formation: 未知的 action %q，已拒绝", action.Action)
		}
	}
	return ops
}

// 补写「回复已成功但记忆没写成」的轮次；只补记忆，不碰也不重生成回复。
func (s *Service) RecoverFormation(ctx context.Context, clientID string) (string, error) {
	turn, err := s.repo.FindTurnByClientID(ctx, clientID)
	if err != nil {
		return "", fmt.Errorf("读取轮次失败: %w", err)
	}
	if turn == nil {
		return "", ErrTurnNotFound
	}
	if turn.Status != model.TurnStatusOK {
		return "", ErrTurnNotComplete
	}
	if turn.FormationStatus == model.FormationApplied {
		return RecoverAlreadyApplied, nil
	}

	// 只取这一轮之前的历史：后面发生的聊天不能进入这一轮的上下文。
	history, err := s.repo.ListChatMessages(ctx, turn.ID, 20)
	if err != nil {
		return "", fmt.Errorf("读取历史失败: %w", err)
	}

	owner := newExecOwner()
	claimed, err := s.repo.ClaimFormation(ctx, turn.ID, owner, int(formationLease.Seconds()))
	if err != nil {
		return "", fmt.Errorf("认领记忆写入失败: %w", err)
	}
	if !claimed {
		// 还在租约内的 running 说明有别处正在处理。
		return RecoverInProgress, nil
	}

	if _, err := s.runFormation(ctx, turn, history, owner); err != nil {
		return "", err
	}
	return RecoverApplied, nil
}

func buildFormationInput(chatMessageID int64, userMessage string, history []model.ChatMessage,
	memories []model.Memory, lifeRecords []model.LifeRecord) string {

	var sb strings.Builder
	sb.WriteString("最近对话：\n")
	if len(history) == 0 {
		sb.WriteString("（无）\n")
	} else {
		for _, m := range history {
			fmt.Fprintf(&sb, "chat:%d [%s] | %s\n", m.ID, m.Role, m.Content)
		}
	}

	fmt.Fprintf(&sb, "\n当前消息：\nchat:%d [user] | %s\n", chatMessageID, userMessage)

	sb.WriteString("\n已有生活记录：\n")
	if len(lifeRecords) == 0 {
		sb.WriteString("（无）\n")
	} else {
		for _, r := range lifeRecords {
			sb.WriteString(formatLifeRecordRef(r))
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n已有长期记忆：\n")
	if len(memories) == 0 {
		sb.WriteString("（无）\n")
		return sb.String()
	}
	for _, m := range memories {
		if m.Category != "" {
			fmt.Fprintf(&sb, "memory:%d | %s（%s）\n", m.ID, m.Content, m.Category)
		} else {
			fmt.Fprintf(&sb, "memory:%d | %s\n", m.ID, m.Content)
		}
	}
	return sb.String()
}

func parseRef(ref, prefix string) (int64, bool) {
	num, ok := strings.CutPrefix(ref, prefix)
	if !ok || num == "" || num[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(num); i++ {
		if num[i] < '0' || num[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

const responseSystemPrompt = `你是 Memora，一个长期陪伴和记录用户的个人 AI。

你可以调用工具读取用户的长期记忆（recall_memories）、记忆的原始来源（get_memory_source），以及带出处的日常生活记录（recall_life_records）。

要不要调用只看一件事：这一步是不是真的缺了只有这些工具才能提供的信息。当前输入已经足够完成任务时就直接做——翻译、改写、解释、算数、写代码、闲聊都属于这类，不要为了「也许有帮助」先查一遍，也不要只因为句子里提到了某件事就去查那件事。反过来，当回答确实依赖用户过去的经历、偏好或某个具体日期时，先查再答，不要凭印象替代。

查不到东西就如实说没有记录，也不要把查到的内容当成用户没问的东西硬塞进回答。

* 生活记录里 occurred_on 为空表示这件事发生在哪天并不知道（用户可能只说「三年前」），不要替它安一个日期。
* state 是 planned 说明还没做，negated 说明用户明确说没做，只有 done 才是已经发生。别把计划说成完成，也别把没记录说成没发生。
* 召回结果是有限的，不能拿它当完整的活动统计去下结论。

你的目标是自然地回应用户，而不是急着分析他或解决他的问题。

* 用户主要在分享、倾诉、表达情绪或想法时：先听懂并回应，不要自动给建议、做任务拆解或心理分析。
  但他讲得越深、越说不清，越要往下多说一点你自己的理解——这件事可能为什么会这样、还可能是什么别的样子，一次给一两个，并说清这都只是猜测。不要用一连串问题把话头递回去：一轮最多问一个问题，不问也可以。
* 用户明确求建议、信息、判断或方案时：认真帮助他解决问题。你能给的那部分先给出来，再说还缺什么，不要用一串问题把整个回答推到下一轮。
* 用户一次提到很多细节时，不要逐项分析，但可以自然地带着这些细节回应。具体永远比概括好：与其写一句“你最近压力挺大的”这种谁都能用的总结，不如说一句只有他这段经历才对得上的话。
* 不要把推断写成结论：可以说“这有点像……”“有没有可能是……”，但不能断言他就一定是这样。
* 可以自然使用已有记忆让交流有连续感，但不要为了展示“记得”而强行提过去；用户当前表达优先于旧记忆。
* Formation 的理解和 guidance 只是辅助参考，不是用户亲口说出的新事实。
* 回复自然、口语化。长度跟着这一轮走，不要预设长短——用户随口一句就简短回应，他认真讲了一件事就把话接完，该展开就展开，别为了显得简洁而压成一句，也别为了显得认真而硬撑长。一轮仍然围绕一个核心点，展开是指在同一个点上说得更具体，不是多加总结句、空泛的安慰或没被问的建议。

关于怎么表达：

* 平时就自然说话，用段落。不要默认套上标题、编号或「总结一下」的格式——那是报告，不是聊天。
* 用户明确要你分析、比较、复盘或做规划时，该结构化就结构化：列表和加粗在这种时候是帮忙的，不用刻意回避，也不必为了显得亲切而放弃清晰。
* 表情符号可以少量用，跟当下的气氛配得上才用。不必每轮都出现，也不要连着堆。
* 对方在讲严肃或难过的事时，别用轻快活泼的语气。
* 不要输出图片、表情包或任何图片地址，也不要写 HTML。需要引用链接时直接给文字链接。

记住：

用户想被听见时，就认真回应他；
用户真的来求解时，再认真帮他解决。
`

// today 来自落库的原始消息时间，不是 time.Now()：重试同一轮必须是同一个值，否则「昨天」会变。
func buildResponseSystemPrompt(formation *llm.FormationResult, today string) string {
	var sb strings.Builder
	sb.WriteString(responseSystemPrompt)

	fmt.Fprintf(&sb, "\n今天是 %s。用户说「今天」「昨天」「上周」时，以这一天为基准。\n", today)

	if formation != nil {
		sb.WriteString("\n本轮你对用户当前消息的理解：\n")
		sb.WriteString(formation.Understanding.Summary)
		sb.WriteString("\n")

		if len(formation.ResponseGuidance.UserMood) > 0 {
			fmt.Fprintf(&sb, "用户当前情绪：%s\n", strings.Join(formation.ResponseGuidance.UserMood, "、"))
		}
		if len(formation.ResponseGuidance.ResponseContext) > 0 {
			sb.WriteString("应影响的上下文（自然带出，不必显式引用）：\n")
			for _, c := range formation.ResponseGuidance.ResponseContext {
				fmt.Fprintf(&sb, "- %s\n", c)
			}
		}
	}

	return sb.String()
}
