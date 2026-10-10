<template>
  <div class="chat">
    <div ref="scrollEl" class="scroll">
      <div class="inner">
        <div v-if="!messages.length" class="empty">
          <h1>今天想聊点什么？</h1>
          <p>你说过的事，我会记得。</p>
          <div class="suggestions">
            <button
              v-for="s in suggestions"
              :key="s"
              type="button"
              :disabled="loading"
              @click="useSuggestion(s)"
            >
              {{ s }}
            </button>
          </div>
        </div>

        <div v-for="(msg, i) in messages" :key="i" class="row" :class="msg.role">
          <div v-if="msg.content" class="body">
            <div v-if="msg.role === 'user'" class="plain">{{ msg.content }}</div>
            <!-- v-html 只放净化后的内容，光标由 CSS 追加，不往这段字符串里拼 -->
            <div
              v-else
              class="md"
              :class="{ streaming: loading && i === messages.length - 1 }"
              v-html="renderMarkdownCached(msg.content)"
            />
          </div>
          <div v-else class="dots"><i /><i /><i /></div>
          <div v-if="msg.incomplete" class="incomplete">这条回复没有写完</div>
          <div v-if="msg.recalled?.length" class="recalled">
            <button type="button" @click="msg.open = !msg.open">
              想起了 {{ msg.recalled.length }} 件事
            </button>
            <ul v-if="msg.open">
              <li v-for="m in msg.recalled" :key="m.id">{{ m.content }}</li>
            </ul>
          </div>
          <div v-if="showRetry(i)" class="failed" :title="msg.failed">
            <span>{{ msg.failed || '这条没有回复完' }}</span>
            <button type="button" @click="retry(i)">重试</button>
          </div>
        </div>
      </div>
    </div>

    <div class="composer-wrap">
      <form class="composer" @submit.prevent="send">
        <textarea
          ref="inputEl"
          v-model="draft"
          rows="1"
          placeholder="给 Memora 发消息…"
          :disabled="loading"
          @input="autoGrow()"
          @keydown="onKeydown"
        />
        <button type="submit" :disabled="loading || !draft.trim()" aria-label="发送">
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
            <path
              d="M8 13V3M8 3L3.5 7.5M8 3l4.5 4.5"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        </button>
      </form>
      <p v-if="error" class="error">{{ error }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import { fetchHistory, newClientId, streamChat } from '../api/client'
import type { RecalledMemory } from '../api/client'
import { renderMarkdownCached } from '../markdown'

interface Msg {
  role: 'user' | 'assistant'
  content: string
  clientId?: string
  failed?: string
  incomplete?: boolean
  recalled?: RecalledMemory[]
  open?: boolean
}

const suggestions = [
  '你还记得我之前说过什么吗',
  '我想记录一下今天',
  '最近有点提不起劲',
  '帮我把最近在想的事理一理',
]

const messages = ref<Msg[]>([])
const draft = ref('')
const loading = ref(false)
const error = ref('')
const scrollEl = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLTextAreaElement | null>(null)

watch(
  messages,
  () => {
    nextTick(() => {
      const el = scrollEl.value
      if (el) el.scrollTop = el.scrollHeight
    })
  },
  { deep: true },
)

function autoGrow() {
  const el = inputEl.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 200) + 'px'
}

onMounted(async () => {
  try {
    messages.value = (await fetchHistory()).map((m) => ({
      role: m.role,
      content: m.content,
      clientId: m.client_id,
      incomplete: m.status === 'partial',
    }))
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载历史失败'
  }
})

function showRetry(i: number) {
  const msg = messages.value[i]
  if (msg?.role !== 'user' || !msg.clientId) return false
  return !!msg.failed || messages.value[i + 1]?.incomplete === true
}

function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Enter' || e.shiftKey || e.isComposing) return
  e.preventDefault()
  send()
}

async function sendText(text: string, clientId = newClientId()) {
  messages.value.push({ role: 'user', content: text, clientId })
  loading.value = true

  const idx = messages.value.length
  messages.value.push({ role: 'assistant', content: '' })

  try {
    await streamChat(
      clientId,
      text,
      (delta) => {
        messages.value[idx].content += delta
      },
      (memories) => {
        messages.value[idx].recalled = memories
      },
    )
  } catch (e) {
    messages.value[idx - 1].failed = e instanceof Error ? e.message : '请求失败'
    if (!messages.value[idx].content) {
      messages.value.splice(idx, 1)
    }
  } finally {
    loading.value = false
    inputEl.value?.focus()
  }
}

function useSuggestion(text: string) {
  if (loading.value) return
  sendText(text)
}

async function send() {
  const text = draft.value.trim()
  if (!text || loading.value) return

  draft.value = ''
  await nextTick()
  autoGrow()

  await sendText(text)
}

async function retry(i: number) {
  if (loading.value) return
  const msg = messages.value[i]
  if (msg?.role !== 'user' || !msg.clientId) return

  messages.value.splice(i, messages.value[i + 1]?.role === 'assistant' ? 2 : 1)
  await sendText(msg.content, msg.clientId)
}
</script>

<style scoped>
.chat {
  position: relative;
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
}

.scroll {
  flex: 1;
  overflow-y: auto;
}

.scroll::-webkit-scrollbar {
  width: 10px;
}

.scroll::-webkit-scrollbar-thumb {
  border: 3px solid transparent;
  border-radius: 6px;
  background: transparent;
  background-clip: content-box;
}

.scroll:hover::-webkit-scrollbar-thumb {
  background: var(--border-strong);
  background-clip: content-box;
}

.inner {
  display: flex;
  flex-direction: column;
  gap: 28px;
  max-width: 720px;
  margin: 0 auto;
  padding: 36px 24px 170px;
}

.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 52vh;
  text-align: center;
}

.empty h1 {
  margin: 0 0 10px;
  font-size: 30px;
  font-weight: 400;
  letter-spacing: -0.01em;
}

.empty p {
  margin: 0;
  color: var(--text-muted);
}

.suggestions {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 10px;
  max-width: 520px;
  margin-top: 32px;
}

.suggestions button {
  padding: 9px 16px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-panel);
  color: var(--text-muted);
  font: inherit;
  font-size: 13.5px;
  cursor: pointer;
  transition: border-color 0.15s, color 0.15s;
}

.suggestions button:hover:not(:disabled) {
  border-color: var(--border-strong);
  color: var(--text);
}

.row {
  display: flex;
  flex-direction: column;
}

.row.user {
  align-items: flex-end;
}

.row.assistant {
  align-items: flex-start;
}

.body {
  max-width: 100%;
  word-break: break-word;
}

.row.user .body {
  max-width: 80%;
  padding: 12px 18px;
  border-radius: 20px;
  background: var(--bg-user);
  white-space: pre-wrap;
  line-height: 1.6;
}

.md :deep(> :first-child) {
  margin-top: 0;
}

.md :deep(> :last-child) {
  margin-bottom: 0;
}

.md :deep(p) {
  margin: 0 0 12px;
}

.md :deep(ul),
.md :deep(ol) {
  margin: 0 0 12px;
  padding-left: 22px;
}

.md :deep(li) {
  margin: 4px 0;
}

.md :deep(strong) {
  font-weight: 600;
}

.md :deep(a) {
  color: var(--accent);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.md :deep(hr) {
  margin: 16px 0;
  border: none;
  border-top: 1px solid var(--border);
}

.md :deep(blockquote) {
  margin: 0 0 12px;
  padding-left: 14px;
  border-left: 2px solid var(--border-strong);
  color: var(--text-muted);
}

.md :deep(code) {
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--bg-user);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.9em;
}

.md :deep(pre) {
  margin: 0 0 12px;
  padding: 12px 14px;
  border-radius: 12px;
  background: var(--bg-user);
  overflow-x: auto;
}

.md :deep(pre code) {
  padding: 0;
  background: none;
  font-size: 13px;
  line-height: 1.6;
}

.md.streaming :deep(> :last-child)::after {
  content: '';
  display: inline-block;
  width: 7px;
  height: 1em;
  margin-left: 3px;
  vertical-align: -2px;
  border-radius: 1px;
  background: var(--accent);
  animation: blink 1s steps(2, start) infinite;
}

.incomplete {
  margin-top: 8px;
  font-size: 13px;
  color: var(--text-muted);
}

@keyframes blink {
  to {
    visibility: hidden;
  }
}

.dots {
  display: flex;
  gap: 5px;
  padding: 9px 0;
}

.dots i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-muted);
  animation: pulse 1.2s ease-in-out infinite;
}

.dots i:nth-child(2) {
  animation-delay: 0.15s;
}

.dots i:nth-child(3) {
  animation-delay: 0.3s;
}

@keyframes pulse {
  0%,
  60%,
  100% {
    opacity: 0.25;
  }
  30% {
    opacity: 1;
  }
}

.failed {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
  font-size: 13px;
  color: var(--danger);
}

.failed button {
  padding: 0;
  border: none;
  background: none;
  color: inherit;
  font: inherit;
  text-decoration: underline;
  cursor: pointer;
}

.recalled {
  margin-top: 10px;
}

.recalled button {
  padding: 0;
  border: none;
  background: none;
  color: var(--text-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
  transition: color 0.15s;
}

.recalled button:hover {
  color: var(--text);
}

.recalled ul {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 10px 0 0;
  padding: 0;
  list-style: none;
}

.recalled li {
  padding: 7px 0 7px 14px;
  border-left: 2px solid var(--border-strong);
  color: var(--text-muted);
  font-size: 13.5px;
  line-height: 1.6;
}

.composer-wrap {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  padding: 0 24px 20px;
  background: linear-gradient(to top, var(--bg) 65%, transparent);
}

.composer {
  display: flex;
  align-items: flex-end;
  gap: 8px;
  max-width: 720px;
  margin: 0 auto;
  padding: 12px 12px 12px 20px;
  border: 1px solid var(--border);
  border-radius: 24px;
  background: var(--bg-panel);
  box-shadow: 0 2px 12px rgba(31, 30, 29, 0.04);
  transition: border-color 0.15s, box-shadow 0.15s;
}

.composer:focus-within {
  border-color: var(--border-strong);
  box-shadow: 0 4px 20px rgba(31, 30, 29, 0.07);
}

.composer textarea {
  flex: 1;
  max-height: 200px;
  padding: 6px 0;
  border: none;
  outline: none;
  background: transparent;
  color: inherit;
  font: inherit;
  line-height: 1.6;
  resize: none;
}

.composer textarea::placeholder {
  color: var(--text-faint);
}

.composer button {
  flex: none;
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  border: none;
  border-radius: 50%;
  background: var(--accent);
  color: #fff;
  cursor: pointer;
  transition: background 0.15s;
}

.composer button:disabled {
  background: #e3e1d9;
  color: var(--text-faint);
  cursor: not-allowed;
}

.error {
  max-width: 720px;
  margin: 10px auto 0;
  font-size: 13px;
  color: var(--danger);
}

@media (max-width: 640px) {
  .inner {
    gap: 22px;
    padding: 24px 16px 150px;
  }

  .empty h1 {
    font-size: 24px;
  }

  .row.user .body {
    max-width: 88%;
  }

  .composer-wrap {
    padding: 0 12px 12px;
  }
}
</style>
