export interface ChatMessage {
  id: number
  role: 'user' | 'assistant'
  content: string
  // 展示接口才会带回这两个字段：status 用来标注「不完整」，
  // client_id 让刷新之后重试仍然落在原来那一轮上。
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

// 一次发送的稳定标识：重试时必须复用同一个 clientId，后端才能识别出这是同一轮。
// crypto.randomUUID 只在安全上下文（https / localhost）可用，否则退回时间戳加随机串。
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
