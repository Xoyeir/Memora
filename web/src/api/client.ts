export interface ChatMessage {
  id: number
  role: 'user' | 'assistant'
  content: string
  status?: 'ok' | 'partial'
  client_id?: string
  created_at: string
}

export interface RecalledMemory {
  id: number
  content: string
  category?: string
}

export async function fetchHistory(limit = 50): Promise<ChatMessage[]> {
  const resp = await fetch(`/api/chat/messages?limit=${limit}`)
  if (!resp.ok) {
    throw new Error(`加载历史失败（${resp.status}）`)
  }
  const data = await resp.json()
  return data.messages ?? []
}

// 重试必须复用同一个 clientId，后端据此认出这是同一轮。
export function newClientId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

export async function streamChat(
  clientId: string,
  message: string,
  onDelta: (text: string) => void,
  onRecalled: (memories: RecalledMemory[]) => void,
): Promise<void> {
  const resp = await fetch('/api/chat/stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ client_id: clientId, message }),
  })
  if (!resp.ok || !resp.body) {
    if (resp.status === 409) {
      const data = await resp.json().catch(() => null)
      throw new Error(data?.error ?? '请求冲突（409）')
    }
    throw new Error(`请求失败（${resp.status}）`)
  }

  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    const parts = buffer.split('\n\n')
    buffer = parts.pop() ?? ''

    for (const part of parts) {
      const line = part.split('\n').find((l) => l.startsWith('data: '))
      if (!line) continue
      const data = JSON.parse(line.slice(6))
      if (data.error) throw new Error(data.error)
      if (data.recalled) onRecalled(data.recalled)
      if (data.content) onDelta(data.content)
    }
  }
}

export type DayStatus = 'no_chat' | 'no_clue' | 'pending' | 'ready' | 'failed'

export interface DayState {
  day: string
  status: DayStatus
  label: string
  summary: string
  // 空串表示还没有结果
  source: '' | 'auto' | 'manual'
  clue_count: number
  input_truncated: boolean
  needs_summary: boolean
  error_message?: string
}

export interface MonthView {
  month: string
  timezone: string
  today: string
  days: DayState[]
  pending_days: string[]
}

export interface DayClue {
  id: number
  kind: string
  state: string
  basis: string
  content: string
  evidence_quote: string
  occurred_on?: string
  chat_message_id: number
}

export interface DayDetail {
  day: string
  state: DayState
  clues: DayClue[]
  messages: ChatMessage[]
}

async function failure(resp: Response, fallback: string): Promise<Error> {
  const data = await resp.json().catch(() => null)
  return new Error(data?.error ?? `${fallback}（${resp.status}）`)
}

// 不传月份表示「今天所在的月份」，由服务端按业务时区决定，前端不自己算今天是几月。
export async function fetchMonth(month = '', signal?: AbortSignal): Promise<MonthView> {
  const q = month ? `?month=${encodeURIComponent(month)}` : ''
  const resp = await fetch(`/api/calendar/month${q}`, { signal })
  if (!resp.ok) throw await failure(resp, '加载日历失败')
  return resp.json()
}

export async function fetchDay(day: string): Promise<DayDetail> {
  const resp = await fetch(`/api/calendar/days/${day}`)
  if (!resp.ok) throw await failure(resp, '加载这一天的依据失败')
  return resp.json()
}

export async function summarizeDay(day: string): Promise<DayState> {
  const resp = await fetch(`/api/calendar/days/${day}/summarize`, { method: 'POST' })
  if (!resp.ok) throw await failure(resp, '整理这一天失败')
  return resp.json()
}

export async function setDayLabel(day: string, label: string): Promise<DayState> {
  const resp = await fetch(`/api/calendar/days/${day}/label`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ label }),
  })
  if (!resp.ok) throw await failure(resp, '保存标签失败')
  return resp.json()
}

export async function clearDayLabel(day: string): Promise<DayState> {
  const resp = await fetch(`/api/calendar/days/${day}/label`, { method: 'DELETE' })
  if (!resp.ok) throw await failure(resp, '取消标签失败')
  return resp.json()
}

export interface Memory {
  id: number
  content: string
  category?: string
  status: 'active' | 'superseded' | 'corrected' | 'forgotten'
  origin: 'auto' | 'user_corrected'
  superseded_by_id?: number
  created_at: string
  updated_at: string
}

export interface MemorySource {
  source_type: 'chat' | 'journal' | 'correction'
  content: string
  created_at: string
  action?: 'correct' | 'forget'
  new_content?: string
}

export interface MemoryList {
  memories: Memory[]
  status: string
  limit: number
  truncated: boolean
}

export interface MemorySourceView {
  memory: Memory
  sources: MemorySource[]
}

export async function fetchMemories(status = 'active', limit = 50): Promise<MemoryList> {
  const resp = await fetch(`/api/memories?status=${encodeURIComponent(status)}&limit=${limit}`)
  if (!resp.ok) throw await failure(resp, '加载记忆失败')
  return resp.json()
}

export async function fetchMemorySources(id: number): Promise<MemorySourceView> {
  const resp = await fetch(`/api/memories/${id}/sources`)
  if (!resp.ok) throw await failure(resp, '加载来源失败')
  return resp.json()
}

export async function correctMemory(id: number, content: string): Promise<Memory> {
  const resp = await fetch(`/api/memories/${id}/content`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  })
  if (!resp.ok) throw await failure(resp, '纠正失败')
  const data = await resp.json()
  return data.memory
}

export async function forgetMemory(id: number): Promise<Memory> {
  const resp = await fetch(`/api/memories/${id}`, { method: 'DELETE' })
  if (!resp.ok) throw await failure(resp, '忘记失败')
  const data = await resp.json()
  return data.memory
}
