<template>
  <div class="app">
    <header class="topbar">
      <span class="logo">Memora</span>
      <nav class="tabs" aria-label="页面">
        <a
          v-for="t in tabs"
          :key="t.id"
          :href="'#' + t.id"
          :class="{ on: active === t.id }"
          :aria-current="active === t.id ? 'page' : undefined"
        >
          {{ t.label }}
        </a>
      </nav>
    </header>

    <!-- 聊天视图保持挂载，切页不会中断生成或丢失草稿 -->
    <main class="page">
      <ChatView v-show="active === 'chat'" />
      <CalendarView v-if="visited.calendar" v-show="active === 'calendar'" />
      <MemoryView v-if="visited.memory" v-show="active === 'memory'" />
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import ChatView from './components/ChatView.vue'
import CalendarView from './components/CalendarView.vue'
import MemoryView from './components/MemoryView.vue'

const tabs = [
  { id: 'chat', label: '聊天' },
  { id: 'calendar', label: '日历' },
  { id: 'memory', label: '记忆' },
] as const

type TabID = (typeof tabs)[number]['id']

const ids = tabs.map((t) => t.id) as readonly string[]

const hash = ref(window.location.hash.replace('#', ''))
const active = computed<TabID>(() => (ids.includes(hash.value) ? (hash.value as TabID) : 'chat'))

const visited = reactive({ calendar: false, memory: false })

function sync() {
  hash.value = window.location.hash.replace('#', '')
  if (active.value === 'calendar') visited.calendar = true
  if (active.value === 'memory') visited.memory = true
}

onMounted(() => {
  sync()
  window.addEventListener('hashchange', sync)
})
onUnmounted(() => window.removeEventListener('hashchange', sync))
</script>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.topbar {
  flex: none;
  display: flex;
  align-items: center;
  gap: 20px;
  height: 52px;
  padding: 0 24px;
  border-bottom: 1px solid var(--border);
  background: var(--bg);
}

.logo {
  color: var(--text-muted);
  font-size: 14px;
  font-weight: 500;
  letter-spacing: 0.02em;
}

.tabs {
  display: flex;
  gap: 4px;
}

.tabs a {
  padding: 5px 12px;
  border-radius: 999px;
  color: var(--text-muted);
  font-size: 13.5px;
  text-decoration: none;
  transition: background 0.15s, color 0.15s;
}

.tabs a:hover {
  color: var(--text);
}

.tabs a.on {
  background: var(--bg-user);
  color: var(--text);
}

.tabs a:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.page {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

@media (max-width: 640px) {
  .topbar {
    gap: 12px;
    padding: 0 16px;
  }
}
</style>
