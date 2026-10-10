<template>
  <div class="calendar">
    <div class="scroll">
      <div class="inner">
        <header class="head">
          <div class="nav">
            <button type="button" aria-label="上个月" @click="step(-1)">‹</button>
            <span class="month">{{ monthLabel }}</span>
            <button type="button" aria-label="下个月" @click="step(1)">›</button>
          </div>
          <button v-if="view && view.month !== view.today.slice(0, 7)" type="button" class="today-btn" @click="goToday">
            回到今天
          </button>
        </header>

        <ul class="legend">
          <li><i class="swatch auto" />自动整理</li>
          <li><i class="swatch manual" />你设置的</li>
          <li><i class="swatch pending" />整理中</li>
          <li><i class="swatch none" />没有聊天</li>
        </ul>

        <p v-if="loading" class="hint">正在加载…</p>
        <p v-else-if="error" class="hint err">
          {{ error }}
          <button type="button" @click="load()">重试</button>
        </p>

        <template v-else-if="view">
          <div class="weekdays" aria-hidden="true">
            <span v-for="w in weekdays" :key="w">{{ w }}</span>
          </div>
          <div class="grid">
            <span v-for="n in leading" :key="'pad' + n" />
            <button
              v-for="d in view.days"
              :key="d.day"
              type="button"
              class="day"
              :class="[cellClass(d), { selected: d.day === selected }]"
              :aria-label="ariaLabel(d)"
              :aria-pressed="d.day === selected"
              @click="select(d.day)"
            >
              <span class="num">{{ Number(d.day.slice(-2)) }}</span>
              <span class="label">{{ cellText(d) }}</span>
            </button>
          </div>
        </template>

        <section v-if="selected" class="detail">
          <header class="detail-head">
            <h2>{{ selectedLabel }}</h2>
            <span v-if="detail?.state.source" class="badge" :class="detail.state.source">
              {{ detail.state.source === 'manual' ? '你设置的' : '自动整理' }}
            </span>
          </header>

          <p v-if="detailLoading" class="hint">正在加载这一天的依据…</p>
          <p v-else-if="detailError" class="hint err">
            {{ detailError }}
            <button type="button" @click="loadDetail(selected)">重试</button>
          </p>

          <template v-else-if="detail">
            <p class="summary">{{ detailText }}</p>
            <p v-if="detail.state.input_truncated" class="note">
              整理时用到的内容被截断过，这份结果不是完整一天的依据。
            </p>
            <p v-if="detail.state.needs_summary" class="note">这一天的聊天有更新，正在重新整理。</p>

            <div class="actions">
              <template v-if="!editing">
                <button
                  v-if="detail.state.status !== 'no_chat'"
                  type="button"
                  class="ghost"
                  @click="startEdit"
                >
                  改一下标签
                </button>
                <button
                  v-if="detail.state.source === 'manual'"
                  type="button"
                  class="ghost"
                  :disabled="busy"
                  @click="restoreAuto"
                >
                  恢复自动
                </button>
                <button
                  v-if="detail.state.status === 'failed' || detail.state.needs_summary"
                  type="button"
                  class="ghost"
                  :disabled="busy"
                  @click="retrySummary"
                >
                  重新整理
                </button>
              </template>
              <form v-else class="edit" @submit.prevent="saveLabel">
                <input
                  ref="labelInput"
                  v-model="labelDraft"
                  maxlength="12"
                  placeholder="比如：轻松"
                  aria-label="这一天的标签"
                />
                <button type="submit" class="solid" :disabled="busy || !labelDraft.trim()">保存</button>
                <button type="button" class="ghost" @click="editing = false">取消</button>
              </form>
            </div>

            <p v-if="feedback" class="feedback">{{ feedback }}</p>
            <p v-if="actionError" class="hint err">{{ actionError }}</p>

            <section v-if="detail.clues.length" class="block">
              <h3>状态依据</h3>
              <ul class="clues">
                <li v-for="c in detail.clues" :key="c.id">
                  <p class="clue-text">{{ c.content }}</p>
                  <p class="clue-meta">
                    {{ kindText(c.kind) }}{{ stateText(c.state) }} ·
                    {{ c.basis === 'inferred' ? '由内容推测' : '用户明确说的' }}
                  </p>
                  <p v-if="c.evidence_quote" class="quote">「{{ c.evidence_quote }}」</p>
                </li>
              </ul>
            </section>
            <p v-else class="note">这一天没有可用于判断状态的线索，所以没有自动标签。</p>

            <section v-if="detail.messages.length" class="block">
              <h3>那天的原话</h3>
              <ul class="talk">
                <li v-for="m in detail.messages" :key="m.id" class="talk-item">{{ m.content }}</li>
              </ul>
            </section>
          </template>
        </section>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import {
  clearDayLabel,
  fetchDay,
  fetchMonth,
  setDayLabel,
  summarizeDay,
} from '../api/client'
import type { DayDetail, DayState, MonthView } from '../api/client'

const weekdays = ['一', '二', '三', '四', '五', '六', '日']

const maxParallelSummaries = 2
const pollInterval = 2500
const maxPolls = 20

const view = ref<MonthView | null>(null)
const loading = ref(false)
const error = ref('')
const selected = ref('')
const detail = ref<DayDetail | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
const editing = ref(false)
const labelDraft = ref('')
const busy = ref(false)
const feedback = ref('')
const actionError = ref('')
const labelInput = ref<HTMLInputElement | null>(null)

// 每次切换月份换令牌，迟到的响应凭令牌自行丢弃，避免上月数据贴到本月。
let loadToken = 0
let abortMonth: AbortController | null = null
let pollTimer: ReturnType<typeof setTimeout> | null = null
let polls = 0

const monthLabel = computed(() => {
  const m = view.value?.month ?? ''
  if (!/^\d{4}-\d{2}$/.test(m)) return '　'
  return `${m.slice(0, 4)} 年 ${Number(m.slice(5))} 月`
})

const leading = computed(() => {
  const first = view.value?.days[0]?.day
  if (!first) return 0
  const weekday = new Date(`${first}T00:00:00`).getDay()
  return (weekday + 6) % 7
})

function cellText(d: DayState) {
  switch (d.status) {
    case 'ready':
      return d.label || '未知'
    case 'pending':
      return d.needs_summary ? '待整理' : '整理中…'
    case 'failed':
      return '整理失败'
    case 'no_clue':
      return '线索不足'
    default:
      return ''
  }
}

function cellClass(d: DayState) {
  const cls = [`s-${d.status}`]
  if (d.status === 'ready') cls.push(d.source === 'manual' ? 'm-manual' : 'm-auto')
  if (view.value?.today === d.day) cls.push('today')
  return cls
}

function ariaLabel(d: DayState) {
  const text = cellText(d)
  return text ? `${d.day}，${text}` : `${d.day}，没有聊天`
}

function kindText(kind: string) {
  return { mood: '情绪', activity: '活动', goal: '目标', date: '日期' }[kind] ?? kind
}

function stateText(state: string) {
  if (state === 'planned') return '（计划中）'
  if (state === 'negated') return '（明确说没做）'
  if (state === 'done') return '（已完成）'
  return ''
}

const selectedLabel = computed(() => {
  const d = selected.value
  if (!d) return ''
  return `${Number(d.slice(5, 7))} 月 ${Number(d.slice(8))} 日`
})

const detailText = computed(() => {
  const st = detail.value?.state
  if (!st) return ''
  if (st.summary) return st.summary
  if (st.status === 'no_chat') return '这一天没有聊天记录。'
  if (st.status === 'no_clue') return '这一天聊过天，但没有足以判断状态的内容，所以没有自动标签。'
  if (st.status === 'pending') return st.needs_summary ? '还没有整理过这一天。' : '正在整理这一天…'
  if (st.status === 'failed') return st.error_message || '这一天整理失败了。'
  return ''
})

function step(delta: number) {
  const m = view.value?.month
  if (!m) return
  const year = Number(m.slice(0, 4))
  const month = Number(m.slice(5)) + delta
  const next = new Date(year, month - 1, 1)
  load(`${next.getFullYear()}-${String(next.getMonth() + 1).padStart(2, '0')}`)
}

function goToday() {
  const today = view.value?.today
  if (today) load(today.slice(0, 7))
}

function clearPoll() {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
}

async function load(month?: string) {
  const token = ++loadToken
  clearPoll()
  abortMonth?.abort()
  const controller = new AbortController()
  abortMonth = controller

  loading.value = true
  error.value = ''
  try {
    const next = await fetchMonth(month ?? view.value?.month ?? '', controller.signal)
    if (token !== loadToken) return
    view.value = next
    loading.value = false
    if (next.pending_days.length) {
      void runSummaries(next.pending_days, token)
    }
  } catch (e) {
    if (token !== loadToken || controller.signal.aborted) return
    loading.value = false
    error.value = e instanceof Error ? e.message : '加载日历失败'
  }
}

async function runBatch(days: string[], token: number) {
  const queue = [...days]
  const workers = Array.from({ length: Math.min(maxParallelSummaries, queue.length) }, () =>
    (async () => {
      while (queue.length) {
        const day = queue.shift()
        if (!day || token !== loadToken) return
        try {
          await summarizeDay(day)
        } catch {
        }
      }
    })(),
  )
  await Promise.all(workers)
}

async function runSummaries(days: string[], token: number) {
  await runBatch(days, token)
  for (polls = 0; polls < maxPolls; polls++) {
    await sleep(pollInterval)
    if (token !== loadToken) return

    let next: MonthView
    try {
      next = await fetchMonth(view.value?.month ?? '')
    } catch {
      return
    }
    if (token !== loadToken) return
    view.value = next

    if (next.pending_days.length) {
      await runBatch(next.pending_days, token)
      if (token !== loadToken) return
      continue
    }
    if (!next.days.some((d) => d.status === 'pending')) return
  }
}

function sleep(ms: number) {
  return new Promise<void>((resolve) => {
    pollTimer = setTimeout(resolve, ms)
  })
}

async function select(day: string) {
  selected.value = day
  editing.value = false
  feedback.value = ''
  actionError.value = ''
  await loadDetail(day)
}

async function loadDetail(day: string) {
  detailLoading.value = true
  detailError.value = ''
  try {
    detail.value = await fetchDay(day)
  } catch (e) {
    detailLoading.value = false
    detailError.value = e instanceof Error ? e.message : '加载失败'
    return
  }
  detailLoading.value = false
  labelDraft.value = detail.value.state.label
}

// 只用返回的那天替换对应格，不重拉整月，避免又触发一轮整理。
function patchDay(state: DayState) {
  if (!view.value) return
  const i = view.value.days.findIndex((d) => d.day === state.day)
  if (i >= 0) view.value.days[i] = state
  if (detail.value) detail.value.state = state
}

function startEdit() {
  labelDraft.value = detail.value?.state.label ?? ''
  editing.value = true
  nextTick(() => labelInput.value?.focus())
}

async function saveLabel() {
  const day = selected.value
  const label = labelDraft.value.trim()
  if (!day || !label || busy.value) return

  busy.value = true
  actionError.value = ''
  feedback.value = ''
  try {
    patchDay(await setDayLabel(day, label))
    editing.value = false
    feedback.value = '已经改成你设置的了。'
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    busy.value = false
  }
}

async function restoreAuto() {
  const day = selected.value
  if (!day || busy.value) return

  busy.value = true
  actionError.value = ''
  feedback.value = ''
  try {
    const state = await clearDayLabel(day)
    patchDay(state)
    feedback.value = '已经恢复成自动整理的结果。'
    if (state.needs_summary) void runSummaries([day], loadToken)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : '取消失败'
  } finally {
    busy.value = false
  }
}

async function retrySummary() {
  const day = selected.value
  if (!day || busy.value) return

  busy.value = true
  actionError.value = ''
  feedback.value = ''
  try {
    const state = await summarizeDay(day)
    patchDay(state)
    feedback.value = state.status === 'ready' ? '整理好了。' : '这次还是没有整理出结果。'
    if (state.status === 'ready') await loadDetail(day)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : '重新整理失败'
  } finally {
    busy.value = false
  }
}

onMounted(() => load())

onUnmounted(() => {
  loadToken++
  clearPoll()
  abortMonth?.abort()
})
</script>

<style scoped>
.calendar {
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
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.nav {
  display: flex;
  align-items: center;
  gap: 6px;
}

.nav button {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border: 1px solid var(--border);
  border-radius: 50%;
  background: var(--bg-panel);
  color: var(--text-muted);
  font-size: 15px;
  line-height: 1;
  cursor: pointer;
}

.nav button:hover {
  border-color: var(--border-strong);
  color: var(--text);
}

.month {
  min-width: 116px;
  text-align: center;
  font-size: 15px;
  font-weight: 500;
}

.today-btn {
  padding: 5px 12px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-panel);
  color: var(--text-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

.today-btn:hover {
  border-color: var(--border-strong);
  color: var(--text);
}

.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 14px;
  margin: 18px 0 0;
  padding: 0;
  list-style: none;
  color: var(--text-muted);
  font-size: 12.5px;
}

.legend li {
  display: flex;
  align-items: center;
  gap: 5px;
}

.swatch {
  width: 9px;
  height: 9px;
  border-radius: 3px;
}

.swatch.auto {
  background: var(--state-auto-bg);
  border: 1px solid var(--state-auto-border);
}

.swatch.manual {
  background: var(--state-manual-bg);
  border: 1px solid var(--state-manual-border);
}

.swatch.pending {
  background: var(--bg-user);
  border: 1px solid var(--border);
}

.swatch.none {
  background: transparent;
  border: 1px dashed var(--border-strong);
}

/* minmax(0, 1fr)：1fr 的最小值是 auto，长文字会把列顶宽导致横向溢出 */
.weekdays,
.grid {
  display: grid;
  grid-template-columns: repeat(7, minmax(0, 1fr));
  gap: 6px;
}

.weekdays {
  margin: 20px 0 8px;
  color: var(--text-faint);
  font-size: 12px;
  text-align: center;
}

.day {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  justify-content: space-between;
  gap: 4px;
  min-width: 0;
  min-height: 62px;
  padding: 7px 8px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--bg-panel);
  color: var(--text-muted);
  font: inherit;
  text-align: left;
  cursor: pointer;
  overflow: hidden;
}

.day:hover {
  border-color: var(--border-strong);
}

.day:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}

.day .num {
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}

.day .label {
  font-size: 12px;
  line-height: 1.35;
  overflow-wrap: anywhere;
}

.day.s-ready.m-auto {
  border-color: var(--state-auto-border);
  background: var(--state-auto-bg);
  color: var(--state-auto-text);
}

.day.s-ready.m-manual {
  border-color: var(--state-manual-border);
  background: var(--state-manual-bg);
  color: var(--state-manual-text);
}

.day.s-pending {
  border-style: dashed;
  background: var(--bg-user);
  color: var(--text-muted);
}

.day.s-failed {
  border-color: #e8c9bc;
  background: #fbf0eb;
  color: var(--danger);
}

.day.s-no_clue {
  border-style: dashed;
  color: var(--text-faint);
}

.day.today .num {
  font-weight: 700;
  color: var(--accent);
}

.day.selected {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}

.detail {
  margin-top: 28px;
  padding-top: 22px;
  border-top: 1px solid var(--border);
}

.detail-head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.detail-head h2 {
  margin: 0;
  font-size: 17px;
  font-weight: 500;
}

.badge {
  padding: 2px 9px;
  border-radius: 999px;
  font-size: 12px;
}

.badge.auto {
  background: var(--state-auto-bg);
  color: var(--state-auto-text);
}

.badge.manual {
  background: var(--state-manual-bg);
  color: var(--state-manual-text);
}

.summary {
  margin: 12px 0 0;
  line-height: 1.7;
}

.note {
  margin: 10px 0 0;
  color: var(--text-muted);
  font-size: 13px;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 16px;
}

.ghost {
  padding: 6px 12px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-panel);
  color: var(--text-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

.ghost:hover:not(:disabled) {
  border-color: var(--border-strong);
  color: var(--text);
}

.ghost:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.edit {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  width: 100%;
}

.edit input {
  flex: 1;
  min-width: 140px;
  padding: 7px 12px;
  border: 1px solid var(--border-strong);
  border-radius: 999px;
  background: var(--bg-panel);
  color: var(--text);
  font: inherit;
  font-size: 13.5px;
  outline: none;
}

.edit input:focus {
  border-color: var(--accent);
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

.feedback {
  margin: 12px 0 0;
  color: var(--state-manual-text);
  font-size: 13px;
}

.hint {
  margin: 20px 0 0;
  color: var(--text-muted);
  font-size: 13.5px;
}

.hint.err {
  color: var(--danger);
}

.hint button {
  padding: 0;
  border: none;
  background: none;
  color: inherit;
  font: inherit;
  text-decoration: underline;
  cursor: pointer;
}

.block {
  margin-top: 22px;
}

.block h3 {
  margin: 0 0 8px;
  color: var(--text-muted);
  font-size: 13px;
  font-weight: 500;
}

.clues,
.talk {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.clues li {
  padding-left: 13px;
  border-left: 2px solid var(--border-strong);
}

.clue-text {
  margin: 0;
}

.clue-meta {
  margin: 3px 0 0;
  color: var(--text-faint);
  font-size: 12.5px;
}

.quote {
  margin: 5px 0 0;
  color: var(--text-muted);
  font-size: 13px;
}

.talk-item {
  padding: 10px 14px;
  border-radius: 14px;
  background: var(--bg-user);
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 14px;
}

@media (max-width: 640px) {
  .inner {
    padding: 20px 14px 110px;
  }

  .weekdays,
  .grid {
    gap: 4px;
  }

  .day {
    min-height: 54px;
    padding: 5px 5px;
  }

  .day .label {
    font-size: 11px;
  }

  .legend {
    gap: 10px;
  }
}
</style>
