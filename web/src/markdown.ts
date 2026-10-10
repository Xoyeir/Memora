import DOMPurify from 'dompurify'
import MarkdownIt from 'markdown-it'

// html: false：模型输出里的原始 HTML 一律按字面文本转义，不解析成标签。
const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: true,
})

// 外链新开标签并切断 window.opener；javascript: 等危险协议由 markdown-it 的 validateLink 拦下。
const renderLinkOpen = md.renderer.rules.link_open
md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  tokens[idx].attrSet('target', '_blank')
  tokens[idx].attrSet('rel', 'noopener noreferrer')
  if (renderLinkOpen) {
    return renderLinkOpen(tokens, idx, options, env, self)
  }
  return self.renderToken(tokens, idx, options)
}

// 白名单刻意不放行 img/iframe/video/svg：模型给出的图片与嵌入内容会被整段丢掉。
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

export function renderMarkdown(text: string): string {
  return DOMPurify.sanitize(md.render(text), {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOW_DATA_ATTR: false,
  })
}

const cache = new Map<string, string>()

export function renderMarkdownCached(text: string): string {
  const hit = cache.get(text)
  if (hit !== undefined) return hit

  const html = renderMarkdown(text)
  if (cache.size >= 300) cache.clear()
  cache.set(text, html)
  return html
}
