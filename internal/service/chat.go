package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/repository"
)

// 幂等与并发相关的错误，handler 依此选择 HTTP 状态码。
var (
	// ErrClientIDConflict 同一个 client_id 被用于了不同的内容。
	ErrClientIDConflict = errors.New("相同请求标识携带了不同的内容")
	// ErrTurnInProgress 同一个 client_id 的另一次请求正在处理中。
	ErrTurnInProgress = errors.New("该请求正在处理中")
	// ErrTurnNotFound 恢复入口找不到这个 client_id 对应的轮次。
	ErrTurnNotFound = errors.New("找不到这一轮")
	// ErrTurnNotComplete 回复还没有成功，恢复记忆没有意义，应该走正常重试。
	ErrTurnNotComplete = errors.New("这一轮的回复还没有成功")
)

// writeTimeout 是脱离请求取消后，写业务状态的时限。
const writeTimeout = 5 * time.Second

// formationLease 是 Formation 的执行租约。
//
// 一次 Formation 模型调用的最坏耗时约为 3 次尝试 × 30s 超时 + 退避，约 95 秒。
// 取 5 分钟留出足够余量：正常执行中的不会被判为过期，进程崩溃留下的 running
// 也能在 5 分钟后被重新认领。
const formationLease = 5 * time.Minute

// 恢复入口的返回状态。
const (
	// RecoverApplied 本次把记忆补写完成。
	RecoverApplied = "applied"
	// RecoverAlreadyApplied 记忆早就写完了，什么都没做。
	RecoverAlreadyApplied = "already_applied"
	// RecoverInProgress 另一个还在有效期内的执行者正在处理，不抢。
	RecoverInProgress = "in_progress"
)

// newFormationOwner 生成本次 Formation 执行的归属标识。
func newFormationOwner() string {
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
	messages, final, _ := s.runAgent(ctx, messages)

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
	messages, _, recalled := s.runAgent(ctx, messages)

	if len(recalled) > 0 {
		onRecall(recalled)
	}

	// 取消之后不再发起任何模型请求。
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

// ChatHistory 返回界面上要显示的历史。
//
// 它和送进模型的上下文用的是两条查询：这里会多带回断掉一半的回复，
// 上下文那边只认完整结束的轮次。同一条 partial 回复可以出现在界面上，
// 但不能被当成完整回答送进下一轮——那是两件事，不该共用一条查询。
func (s *Service) ChatHistory(ctx context.Context, limit int) ([]model.ChatMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repo.ListConversationForDisplay(ctx, limit)
}

// turnRef 是一次发送的处理句柄。
// done 非空时表示这一轮此前已经成功结束，其值就是当时保存的回复。
type turnRef struct {
	turn *model.ChatTurn
	done string
}

// beginTurn 完成幂等入口的全部判断：
// 拿到（或创建）本轮的行、校验内容一致、判断是否已完成、拒绝并发重复处理。
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

// prepareTurn 读取历史、按需执行一次 Formation，并组装本轮的 system prompt。
func (s *Service) prepareTurn(ctx context.Context, turn *model.ChatTurn) (string, []llm.Message, error) {
	history, err := s.repo.ListChatMessages(ctx, turn.ID, 20)
	if err != nil {
		return "", nil, fmt.Errorf("读取历史失败: %w", err)
	}

	formation, err := s.tryFormation(ctx, turn, history)
	if err != nil {
		// 记忆写入失败不影响这一轮回复，这里只记录、不中断整轮。
		// 请求已经被取消的情况由调用方检查 ctx.Err() 处理。
		log.Printf("chat: 轮次 %d 的记忆写入没有完成（不影响回复）: %v", turn.ID, err)
	}

	historyMsgs := make([]llm.Message, len(history))
	for i, m := range history {
		historyMsgs[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	return buildResponseSystemPrompt(formation), historyMsgs, nil
}

// tryFormation 在拿得到执行权时跑一次 Formation。
//
// 拿不到执行权不是错误：说明这一轮已经写过（applied），或者另一个还在有效期内的
// 执行者正在处理（running）。两种情况都不该重跑，也不该报给用户。
func (s *Service) tryFormation(ctx context.Context, turn *model.ChatTurn, history []model.ChatMessage) (*llm.FormationResult, error) {
	owner := newFormationOwner()
	claimed, err := s.repo.ClaimFormation(ctx, turn.ID, owner, int(formationLease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("认领记忆写入失败: %w", err)
	}
	if !claimed {
		return nil, nil
	}
	return s.runFormation(ctx, turn, history, owner)
}

// runFormation 执行一次记忆形成。
//
// 模型调用在事务之外；通过校验的这一批写入连同 formation_status=applied
// 在同一个事务里提交，任一失败整批回滚。
func (s *Service) runFormation(ctx context.Context, turn *model.ChatTurn, history []model.ChatMessage, owner string) (*llm.FormationResult, error) {
	memories, err := s.repo.ListMemories(ctx, 20)
	if err != nil {
		s.abandonFormation(ctx, turn.ID, owner)
		return nil, fmt.Errorf("读取记忆失败: %w", err)
	}

	formation, err := s.llm.Formation(ctx, buildFormationInput(turn.ID, turn.Content, history, memories))
	if err != nil {
		s.abandonFormation(ctx, turn.ID, owner)
		return nil, err
	}

	ops := planMemoryOps(formation.MemoryActions, turn.ID, memories)
	if err := s.repo.ApplyFormation(ctx, turn.ID, owner, ops); err != nil {
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

// abandonFormation 把本轮退回可重试状态。
//
// 用脱离请求取消的短超时 context：这是状态落库，不是业务或模型调用，
// 请求已经被取消时也必须把 running 放掉，否则只能等租约过期。
func (s *Service) abandonFormation(ctx context.Context, turnID int64, owner string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := s.repo.FailFormation(writeCtx, turnID, owner); err != nil {
		log.Printf("chat: 标记轮次 %d 的记忆写入失败状态时出错: %v", turnID, err)
	}
}

// abortTurn 记录一次未完成的轮次并返回原始错误。
//
// partial 是已经流给用户的内容；它必须落库，否则「回复断流」会被误当成「整轮没有发生」。
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

// planMemoryOps 把模型的原始输出转成一批「准备执行」的操作。
//
// 这一步只做校验，不碰数据库：引用解析不出来、指向不存在的记忆、
// 缺必填字段的 action 在这里被明确拒绝并丢掉。
// 被拒绝是模型输出的问题，不是写入失败——剩下的合法操作照常执行。
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

// RecoverFormation 是补写记忆的入口：回复已经成功、但记忆没写成的轮次，
// 由调用方显式触发一次补写。
//
// 它只补记忆，不碰回复——回复的内容不会因为补记忆而变化，也不会重新生成。
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

	// 只取这一轮之前的历史：恢复针对的是原来那条消息，
	// 后面发生的聊天不能进入这一轮的上下文。
	history, err := s.repo.ListChatMessages(ctx, turn.ID, 20)
	if err != nil {
		return "", fmt.Errorf("读取历史失败: %w", err)
	}

	owner := newFormationOwner()
	claimed, err := s.repo.ClaimFormation(ctx, turn.ID, owner, int(formationLease.Seconds()))
	if err != nil {
		return "", fmt.Errorf("认领记忆写入失败: %w", err)
	}
	if !claimed {
		// 还在租约内的 running 说明有别处正在处理，不抢。
		return RecoverInProgress, nil
	}

	if _, err := s.runFormation(ctx, turn, history, owner); err != nil {
		return "", err
	}
	return RecoverApplied, nil
}

func buildFormationInput(chatMessageID int64, userMessage string, history []model.ChatMessage, memories []model.Memory) string {
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

你可以调用工具读取用户的长期记忆（recall_memories）和记忆的原始来源（get_memory_source）。回复需要引用用户过去的信息时，先调用 recall_memories。

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

func buildResponseSystemPrompt(formation *llm.FormationResult) string {
	var sb strings.Builder
	sb.WriteString(responseSystemPrompt)

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
