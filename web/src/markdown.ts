import DOMPurify from 'dompurify'
import MarkdownIt from 'markdown-it'

// html: false 是第一道防线：模型输出里的原始 HTML 一律按字面文本转义，不解析成标签。
// 关掉它，<script>、<img onerror> 这类东西就会直接进解析器，安全性只剩白名单一层。
const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: true,
})

// markdown-it 自带的 validateLink 会拒绝 javascript: / vbscript: / file: / data:，
// 这里没有覆盖它，危险链接在解析阶段就被拦下。DOMPurify 还会按 URI 规则再筛一遍。
//
// 外链新开标签，并切断 window.opener。
const renderLinkOpen = md.renderer.rules.link_open
md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  tokens[idx].attrSet('target', '_blank')
  tokens[idx].attrSet('rel', 'noopener noreferrer')
  if (renderLinkOpen) {
    return renderLinkOpen(tokens, idx, options, env, self)
  }
  return self.renderToken(tokens, idx, options)
}

// 白名单只留这些。刻意不放行 img、iframe、video、svg：
// 本轮不加载模型给出的图片和嵌入内容，它们会在净化时被整段丢掉。
const ALLOWED_TAGS = [
  'p',
  'br',
  'strong',
  'em',
  // markdown-it 把 ~~删除线~~ 渲染成 <s>，写成 del 会把它整个剥掉只剩文字
  's',
  'del',
  'ul',
  'ol',
  'li',
  'blockquote',
  'a',
  'code',
  'pre',
  'hr',
]

const ALLOWED_ATTR = ['href', 'title', 'target', 'rel']

// 渲染结果全部要经过这里，包括流式过程中每一次累计正文的重渲染。
export function renderMarkdown(text: string): string {
  return DOMPurify.sanitize(md.render(text), {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOW_DATA_ATTR: false,
  })
}

// 流式时每个 delta 都会得到一段更长的累计正文，而 React/Vue 的重渲染
// 会把所有历史消息一起过一遍。按正文缓存，让没变过的消息不必反复解析。
const cache = new Map<string, string>()

export function renderMarkdownCached(text: string): string {
  const hit = cache.get(text)
  if (hit !== undefined) return hit

  const html = renderMarkdown(text)
  if (cache.size >= 300) cache.clear()
  cache.set(text, html)
  return html
}
