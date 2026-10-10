<template>
  <div class="memory">
    <div class="scroll">
      <div class="inner">
        <header class="head">
          <div>
            <h1>长期记忆</h1>
            <p class="sub">这些是从聊天里留下来的、会在以后被想起的事。</p>
          </div>
          <div class="filter" role="group" aria-label="筛选">
            <button
              v-for="f in filters"
              :key="f.id"
              type="button"
              :class="{ on: status === f.id }"
              :aria-pressed="status === f.id"
              @click="setFilter(f.id)"
            >
              {{ f.label }}
            </button>
          </div>
        </header>

        <p v-if="loading" class="hint">正在加载…</p>
        <p v-else-if="error" class="hint err">
          {{ error }}
          <button type="button" class="link" @click="load()">重试</button>
        </p>

        <template v-else>
          <p v-if="notice" class="notice">{{ notice }}</p>
          <p v-if="failure" class="hint err">{{ failure }}</p>

          <p v-if="!list.length" class="hint empty">
            {{ status === 'active' ? '还没有长期记忆。多聊聊，我会慢慢记下来。' : '这里还没有内容。' }}
          </p>

          <ul v-else class="cards">
            <li v-for="m in list" :key="m.id" class="card" :class="{ off: m.status !== 'active' }">
              <p class="content">{{ m.content }}</p>

              <div class="meta">
                <span v-if="m.category" class="chip">{{ categoryText(m.category) }}</span>
                <span v-if="m.origin === 'user_corrected'" class="chip you">你改过的</span>
                <span v-if="m.status !== 'active'" class="chip dim">{{ statusText(m.status) }}</span>
                <span class="time">{{ fmtTime(m.updated_at || m.created_at) }}</span>
              </div>

              <div class="ops">
                <button type="button" class="link" @click="toggleSources(m.id)">
                  {{ openId === m.id ? '收起来源' : '看来源' }}
                </button>
                <template v-if="m.status === 'active'">
                  <button type="button" class="link" :disabled="busy" @click="startCorrect(m)">
                    纠正
                  </button>
                  <button type="button" class="link danger" :disabled="busy" @click="askForget(m.id)">
                    忘记
                  </button>
                </template>
                <button
                  v-else-if="m.superseded_by_id"
                  type="button"
                  class="link"
                  @click="jumpTo(m.superseded_by_id)"
                >
                  看修改后的那条
                </button>
              </div>

              <form v-if="editingId === m.id" class="edit" @submit.prevent="saveCorrection">
                <textarea
                  ref="editBox"
                  v-model="draft"
                  rows="3"
                  maxlength="1000"
                  :placeholder="m.content"
                  aria-label="改成你说的"
                />
                <p class="tip">
                  改完之后，原来这条会保留为「你改过」，新的一条会单独记下来；
                  它不会继续用旧的原话当依据。
                </p>
                <div class="edit-ops">
                  <button type="submit" class="solid" :disabled="busy || !draft.trim()">保存</button>
                  <button type="button" class="link" @click="editingId = null">取消</button>
                </div>
              </form>

              <div v-if="forgetId === m.id" class="confirm">
                <p>
                  忘记之后，这条不再参与以后的候选、召回，也不会再被当成回答的依据。
                  原始聊天和其他生活记录都还在；如果以后你又提起，可能形成新的一条。
                </p>
                <div class="edit-ops">
                  <button type="button" class="solid" :disabled="busy" @click="doForget(m.id)">
                    确认忘记
                  </button>
                  <button type="button" class="link" @click="forgetId = 0">算了</button>
                </div>
              </div>

              <div v-if="openId === m.id" class="sources">
                <p v-if="sourcesLoading" class="hint">正在加载来源…</p>
                <p v-else-if="sourcesError" class="hint err">
                  {{ sourcesError }}
                  <button type="button" class="link" @click="toggleSources(m.id, true)">重试</button>
                </p>
                <template v-else-if="sources">
                  <p v-if="!sources.sources.length" class="tip">
                    这条没有留下可查看的原文出处。
                  </p>
                  <ul v-else>
                    <li v-for="(s, i) in sources.sources" :key="i">
                      <p class="src-meta">{{ sourceText(s) }} · {{ fmtTime(s.created_at) }}</p>

                      <template v-if="s.source_type === 'correction' && s.action === 'forget'">
                        <p class="src-body">{{ s.content }}</p>
                        <p class="tip">这是你让它别再想起来之前，它记着的那句。</p>
                      </template>

                      <template v-else-if="s.source_type === 'correction'">
                        <p class="src-body">{{ s.new_content || s.content }}</p>
                        <p v-if="s.new_content" class="tip">
                          改之前是：{{ s.content }}。那句只说明原来是什么，不用来证明现在这条。
                        </p>
                      </template>

                      <template v-else>
                        <p class="src-body">{{ s.content }}</p>
                      </template>
                    </li>
                  </ul>
                </template>
              </div>

              <p v-if="cardError[m.id]" class="hint err">{{ cardError[m.id] }}</p>
            </li>
          </ul>

          <p v-if="truncated" class="hint">
            这里只显示了前 {{ shown }} 条，不是全部。
            <button type="button" class="link" :disabled="loadingMore" @click="loadMore">
              {{ loadingMore ? '加载中…' : '再看更多' }}
            </button>
          </p>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onMounted, reactive, ref } from 'vue'
import {
  correctMemory,
  fetchMemories,
  fetchMemorySources,
  forgetMemory,
} from '../api/client'
import type { Memory, MemorySource, MemorySourceView } from '../api/client'

const filters = [
  { id: 'active', label: '还在用的' },
  { id: 'all', label: '全部' },
]

const list = ref<Memory[]>([])
const status = ref('active')
const limit = ref(50)
const shown = ref(0)
const truncated = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const notice = ref('')
const failure = ref('')
const busy = ref(false)

const openId = ref(0)
const sources = ref<MemorySourceView | null>(null)
const sourcesLoading = ref(false)
const sourcesError = ref('')

const editingId = ref(0)
const draft = ref('')
const editBox = ref<HTMLTextAreaElement[] | null>(null)
const forgetId = ref(0)
const cardError = reactive<Record<number, string>>({})

function categoryText(c: string) {
  return (
    {
      goal: '目标',
      preference: '偏好',
      fact: '事实',
      habit: '习惯',
      relationship: '关系',
      work: '工作',
      health: '健康',
    }[c] ?? c
  )
}

function statusText(s: string) {
  return { superseded: '已被替代', corrected: '你改过', forgotten: '已忘记' }[s] ?? s
}

function sourceText(s: MemorySource) {
  if (s.source_type === 'correction') {
    return s.action === 'forget' ? '你让它别再想起来' : '你自己改的'
  }
  return { chat: '来自聊天', journal: '来自日记' }[s.source_type] ?? s.source_type
}

function fmtTime(v: string) {
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function clearFeedback() {
  notice.value = ''
  failure.value = ''
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await fetchMemories(status.value, limit.value)
    list.value = res.memories ?? []
    shown.value = list.value.length
    truncated.value = res.truncated
  } catch (e) {
    loading.value = false
    error.value = e instanceof Error ? e.message : '加载失败'
    return
  }
  loading.value = false
}

async function loadMore() {
  if (loadingMore.value) return
  loadingMore.value = true
  limit.value = Math.min(limit.value * 2, 200)
  await load()
  loadingMore.value = false
}

function setFilter(id: string) {
  if (status.value === id) return
  status.value = id
  closeAll()
  clearFeedback()
  void load()
}

function closeAll() {
  openId.value = 0
  editingId.value = 0
  forgetId.value = 0
}

async function toggleSources(id: number, force = false) {
  if (openId.value === id && !force) {
    openId.value = 0
    return
  }
  openId.value = id
  sources.value = null
  sourcesError.value = ''
  sourcesLoading.value = true
  try {
    sources.value = await fetchMemorySources(id)
  } catch (e) {
    sourcesLoading.value = false
    sourcesError.value = e instanceof Error ? e.message : '加载来源失败'
    return
  }
  sourcesLoading.value = false
}

function startCorrect(m: Memory) {
  clearFeedback()
  openId.value = 0
  forgetId.value = 0
  editingId.value = m.id
  draft.value = m.content
  nextTick(() => editBox.value?.[0]?.focus())
}

async function saveCorrection() {
  const id = editingId.value
  const content = draft.value.trim()
  if (!id || !content || busy.value) return

  busy.value = true
  clearFeedback()
  delete cardError[id]
  try {
    await correctMemory(id, content)
    editingId.value = 0
    await load()
    notice.value = '已经改成你说的了。原来那条留作记录，新的那条在上面。'
  } catch (e) {
    cardError[id] = e instanceof Error ? e.message : '纠正失败'
  }
  busy.value = false
}

function askForget(id: number) {
  clearFeedback()
  openId.value = 0
  editingId.value = 0
  forgetId.value = id
}

async function doForget(id: number) {
  if (busy.value) return
  busy.value = true
  clearFeedback()
  delete cardError[id]
  try {
    await forgetMemory(id)
    forgetId.value = 0
    await load()
    notice.value = '这条不会再被想起来了。'
  } catch (e) {
    cardError[id] = e instanceof Error ? e.message : '操作失败'
  }
  busy.value = false
}

function jumpTo(id: number) {
  const target = list.value.find((m) => m.id === id)
  if (!target) {
    notice.value = '修改后的那条不在这份列表里，切换到「全部」再看看。'
    return
  }
  clearFeedback()
  openId.value = id
  void toggleSources(id, true)
}

onMounted(load)
</script>

<style scoped>
.memory {
  display: flex;
  flex: 1;
  min-height: 0;
  flex-direction: column;
}

.scroll {
  flex: 1;
  overflow-y: auto;
}

.inner {
  max-width: 720px;
  margin: 0 auto;
  padding: 28px 24px 120px;
}

.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  flex-wrap: wrap;
}

.head h1 {
  margin: 0;
  font-size: 20px;
  font-weight: 500;
}

.sub {
  margin: 6px 0 0;
  color: var(--text-muted);
  font-size: 13.5px;
}

.filter {
  display: flex;
  gap: 6px;
}

.filter button {
  padding: 5px 13px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-panel);
  color: var(--text-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

.filter button:hover {
  border-color: var(--border-strong);
  color: var(--text);
}

.filter button.on {
  border-color: var(--border-strong);
  background: var(--bg-user);
  color: var(--text);
}

.filter button:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}

.cards {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin: 22px 0 0;
  padding: 0;
  list-style: none;
}

.card {
  padding: 16px 18px;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: var(--bg-panel);
}

.card.off {
  opacity: 0.62;
}

.content {
  margin: 0;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

.meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}

.chip {
  padding: 2px 9px;
  border-radius: 999px;
  background: var(--bg-user);
  color: var(--text-muted);
  font-size: 12px;
}

.chip.you {
  background: var(--state-manual-bg);
  color: var(--state-manual-text);
}

.chip.dim {
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text-faint);
}

.time {
  color: var(--text-faint);
  font-size: 12.5px;
}

.ops {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 14px;
  margin-top: 12px;
}

.link {
  padding: 0;
  border: none;
  background: none;
  color: var(--text-muted);
  font: inherit;
  font-size: 13px;
  text-decoration: none;
  cursor: pointer;
}

.link:hover:not(:disabled) {
  color: var(--text);
  text-decoration: underline;
}

.link:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.link.danger:hover:not(:disabled) {
  color: var(--danger);
}

.edit,
.confirm {
  margin-top: 14px;
}

.edit textarea {
  width: 100%;
  box-sizing: border-box;
  padding: 10px 12px;
  border: 1px solid var(--border-strong);
  border-radius: 12px;
  background: var(--bg);
  color: var(--text);
  font: inherit;
  font-size: 14px;
  line-height: 1.6;
  resize: vertical;
  outline: none;
}

.edit textarea:focus {
  border-color: var(--accent);
}

.confirm p {
  margin: 0;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.65;
}

.tip {
  margin: 8px 0 0;
  color: var(--text-muted);
  font-size: 12.5px;
  line-height: 1.6;
}

.edit-ops {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 10px;
}

.solid {
  padding: 7px 16px;
  border: none;
  border-radius: 999px;
  background: var(--accent);
  color: #fff;
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

.solid:disabled {
  background: #e3e1d9;
  color: var(--text-faint);
  cursor: not-allowed;
}

.sources {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px dashed var(--border);
}

.sources ul {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.src-meta {
  margin: 0;
  color: var(--text-faint);
  font-size: 12px;
}

.src-body {
  margin: 4px 0 0;
  padding-left: 12px;
  border-left: 2px solid var(--border-strong);
  color: var(--text-muted);
  font-size: 13.5px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
}

.notice {
  margin: 18px 0 0;
  padding: 9px 14px;
  border-radius: 10px;
  background: var(--state-manual-bg);
  color: var(--state-manual-text);
  font-size: 13px;
}

.hint {
  margin: 18px 0 0;
  color: var(--text-muted);
  font-size: 13.5px;
}

.hint.empty {
  margin-top: 28px;
}

.hint.err {
  color: var(--danger);
}

@media (max-width: 640px) {
  .inner {
    padding: 20px 14px 110px;
  }

  .card {
    padding: 14px;
  }

  .head {
    gap: 10px;
  }
}
</style>
