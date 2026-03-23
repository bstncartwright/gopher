export type WorkSessionStory = {
  goal?: string
  current_state?: string
  current_state_detail?: string
  latest_conclusion?: string
  last_meaningful_step?: string
  latest_anomaly?: string
}

export type WorkSessionSummary = {
  session_id: string
  title: string
  conversation_id?: string
  status: string
  working: boolean
  waiting_on_human: boolean
  waiting_reason?: string
  has_anomaly: boolean
  latest_digest: string
  story: WorkSessionStory
  updated_at: string
  last_seq: number
  priority_label: string
  resume_pending?: boolean
}

export type WorkContextHealth = {
  model_display: string
  model_context_window: number
  reserve_tokens: number
  estimated_input_tokens: number
  overflow_retries: number
  overflow_stage?: string
  summary_strategy?: string
  tool_truncation: number
  recent_messages: string
  memory: string
  compaction: string
}

export type WorkTimelineEvent = {
  seq: number
  timestamp: string
  from: string
  type: string
  type_label: string
  category: string
  digest: string
  emoji: string
  title: string
  subtitle?: string
  tone: string
  key_facts: string[]
  raw_json: string
  waiting: boolean
  anomaly: boolean
  is_meaningful: boolean
  bundle_kind?: string
  bundle_id?: string
  bundle_title?: string
  result_status?: string
}

export type WorkSessionDetail = {
  session: WorkSessionSummary
  story: WorkSessionStory
  counts: Record<string, number>
  latest_anomaly?: string
  context_health?: WorkContextHealth
  timeline: {
    session_id: string
    first_seq: number
    last_seq: number
    has_older: boolean
    events: WorkTimelineEvent[]
  }
}

export type AutomationSummary = {
  total: number
  enabled: number
  paused: number
  failed: number
}

export type AutomationJob = {
  id: string
  session_id: string
  schedule: string
  run_status: string
  next_run_at: string
  last_run_at: string
  updated_at: string
  timezone: string
  message: string
  message_full: string
  created_by: string
  cron_expr: string
  enabled: boolean
  tone: string
}

export type AutomationsResponse = {
  generated_at: string
  has_cron_store: boolean
  error?: string
  summary: AutomationSummary
  attention_jobs: AutomationJob[]
  scheduled_jobs: AutomationJob[]
  paused_jobs: AutomationJob[]
}

export type ChatSessionSummary = {
  session_id: string
  title: string
  status: string
  updated_at: string
  last_seq: number
  working: boolean
  preview?: string
}

export type ChatMessage = {
  seq: number
  role: string
  actor_id: string
  content: string
  timestamp: string
}

export type ChatSessionDetail = {
  session: ChatSessionSummary
  messages: ChatMessage[]
}

const PANEL_ROOT = String(import.meta.env.VITE_PANEL_ROOT || "/admin").replace(
  /\/$/,
  ""
)
const CHAT_ROOT = String(import.meta.env.VITE_CHAT_ROOT || "/chat").replace(
  /\/$/,
  ""
)

async function request<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`${PANEL_ROOT}${path}`, {
    credentials: "same-origin",
    signal,
  })

  if (!response.ok) {
    const text = await response.text()
    throw new Error(text || `Request failed with status ${response.status}`)
  }

  return response.json() as Promise<T>
}

async function requestAbsolute<T>(
  path: string,
  init?: RequestInit
): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...init,
  })

  if (!response.ok) {
    const text = await response.text()
    throw new Error(text || `Request failed with status ${response.status}`)
  }

  return response.json() as Promise<T>
}

export async function fetchWorkSessions(
  signal?: AbortSignal
): Promise<WorkSessionSummary[]> {
  const payload = await request<{ sessions: WorkSessionSummary[] }>(
    "/api/work/sessions",
    signal
  )
  return payload.sessions || []
}

export async function fetchWorkSessionDetail(
  sessionId: string,
  signal?: AbortSignal
): Promise<WorkSessionDetail> {
  return request<WorkSessionDetail>(
    `/api/work/session/${encodeURIComponent(sessionId)}`,
    signal
  )
}

export async function fetchAutomations(
  signal?: AbortSignal
): Promise<AutomationsResponse> {
  return request<AutomationsResponse>("/api/automations", signal)
}

export async function fetchChatSessions(
  signal?: AbortSignal
): Promise<ChatSessionSummary[]> {
  const payload = await requestAbsolute<{ sessions: ChatSessionSummary[] }>(
    `${CHAT_ROOT}/api/sessions`,
    { signal }
  )
  return payload.sessions || []
}

export async function fetchChatSessionDetail(
  sessionId: string,
  signal?: AbortSignal
): Promise<ChatSessionDetail> {
  return requestAbsolute<ChatSessionDetail>(
    `${CHAT_ROOT}/api/session/${encodeURIComponent(sessionId)}`,
    { signal }
  )
}

export async function createChatSession(input: {
  title?: string
  message?: string
}): Promise<ChatSessionDetail> {
  return requestAbsolute<ChatSessionDetail>(`${CHAT_ROOT}/api/sessions`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(input),
  })
}

export async function sendChatMessage(
  sessionId: string,
  message: string
): Promise<void> {
  await requestAbsolute<{ ok: boolean }>(
    `${CHAT_ROOT}/api/session/${encodeURIComponent(sessionId)}/messages`,
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ message }),
    }
  )
}

export function chatStreamURL(sessionId: string, afterSeq = 0) {
  return `${CHAT_ROOT}/api/session/${encodeURIComponent(sessionId)}/stream?after_seq=${encodeURIComponent(String(afterSeq))}`
}
