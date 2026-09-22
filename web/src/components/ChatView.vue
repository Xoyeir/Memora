<template>
  <div class="chat">
    <div class="messages">
      <div v-for="(msg, i) in messages" :key="i" :class="['msg', msg.role]">
        <div class="bubble">{{ msg.content }}</div>
      </div>
      <div v-if="loading" class="msg assistant">
        <div class="bubble thinking">…</div>
      </div>
    </div>

    <form class="input" @submit.prevent="send">
      <input v-model="draft" placeholder="说点什么…" :disabled="loading" />
      <button type="submit" :disabled="loading || !draft.trim()">发送</button>
    </form>

    <p v-if="error" class="error">{{ error }}</p>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { streamChat } from '../api/client'

interface Msg {
  role: 'user' | 'assistant'
  content: string
}

const messages = ref<Msg[]>([])
const draft = ref('')
const loading = ref(false)
const error = ref('')

async function send() {
  const text = draft.value.trim()
  if (!text || loading.value) return
  draft.value = ''
  error.value = ''

  messages.value.push({ role: 'user', content: text })
  loading.value = true

  const idx = messages.value.length
  messages.value.push({ role: 'assistant', content: '' })

  try {
    await streamChat(text, (delta) => {
      messages.value[idx].content += delta
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : '请求失败'
    if (!messages.value[idx].content) {
      messages.value.splice(idx, 1)
    }
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.chat {
  display: flex;
  flex-direction: column;
  height: 100vh;
  max-width: 720px;
  margin: 0 auto;
  padding: 16px;
  box-sizing: border-box;
}

.messages {
  flex: 1;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding-bottom: 12px;
}

.msg {
  display: flex;
}

.msg.user {
  justify-content: flex-end;
}

.msg.assistant {
  justify-content: flex-start;
}

.bubble {
  max-width: 80%;
  padding: 10px 14px;
  border-radius: 14px;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.5;
}

.msg.user .bubble {
  background: #2f6fed;
  color: #fff;
  border-bottom-right-radius: 4px;
}

.msg.assistant .bubble {
  background: #f0f0f0;
  color: #1a1a1a;
  border-bottom-left-radius: 4px;
}

.thinking {
  color: #999;
}

.input {
  display: flex;
  gap: 8px;
}

.input input {
  flex: 1;
  padding: 10px 12px;
  border: 1px solid #ddd;
  border-radius: 10px;
  font-size: 15px;
  outline: none;
}

.input input:focus {
  border-color: #2f6fed;
}

.input button {
  padding: 10px 20px;
  border: none;
  border-radius: 10px;
  background: #2f6fed;
  color: #fff;
  font-size: 15px;
  cursor: pointer;
}

.input button:disabled {
  background: #b6c8f0;
  cursor: not-allowed;
}

.error {
  color: #d32f2f;
  font-size: 13px;
  margin: 8px 0 0;
}
</style>
