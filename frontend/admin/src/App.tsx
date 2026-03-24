import {
  startTransition,
  useDeferredValue,
  useEffect,
  useRef,
  useState,
} from "react"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import { Link, Navigate, useLocation, Routes, Route } from "react-router-dom"

import {
  CalendarDots,
  ChatCircleText,
  ClockCountdown,
  Lightning,
  ListBullets,
  MagnifyingGlass,
  PaperPlaneTilt,
  Plus,
  Robot,
  User,
  WarningCircle,
  Wrench,
} from "@phosphor-icons/react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  chatStreamURL,
  createChatSession,
  fetchAutomations,
  fetchChatSessionDetail,
  fetchChatSessions,
  fetchWorkSessionDetail,
  fetchWorkSessions,
  sendChatMessage,
  type AutomationJob,
  type AutomationsResponse,
  type ChatMessage,
  type ChatSessionDetail,
  type ChatSessionSummary,
  type WorkContextHealth,
  type WorkSessionDetail,
  type WorkSessionStory,
  type WorkSessionSummary,
  type WorkTimelineEvent,
} from "@/lib/panel-api"
import { cn } from "@/lib/utils"

const sessionFilters = [
  { value: "all", label: "All" },
  { value: "attention", label: "Attention" },
  { value: "running", label: "Running" },
  { value: "waiting", label: "Waiting" },
  { value: "paused", label: "Paused" },
  { value: "done", label: "Done" },
] as const

const eventFilters = [
  { value: "all", label: "All" },
  { value: "user", label: "User" },
  { value: "agent", label: "Agent" },
  { value: "tools", label: "Tools" },
  { value: "control", label: "Control" },
  { value: "errors", label: "Errors" },
] as const

const automationFilters = [
  { value: "all", label: "All" },
  { value: "attention", label: "Attention" },
  { value: "enabled", label: "Enabled" },
  { value: "paused", label: "Held" },
] as const

function normalize(value: string) {
  return value.trim().toLowerCase()
}

function formatRelativeTime(value: string) {
  const timestamp = Date.parse(value)
  if (!Number.isFinite(timestamp)) {
    return value
  }

  const diffSeconds = Math.round((Date.now() - timestamp) / 1000)
  const future = diffSeconds < 0
  const absolute = Math.abs(diffSeconds)

  if (absolute < 8) return "just now"
  if (absolute < 60) return future ? `in ${absolute}s` : `${absolute}s ago`
  if (absolute < 3600) {
    const minutes = Math.floor(absolute / 60)
    return future ? `in ${minutes}m` : `${minutes}m ago`
  }
  if (absolute < 86400) {
    const hours = Math.floor(absolute / 3600)
    return future ? `in ${hours}h` : `${hours}h ago`
  }

  const days = Math.floor(absolute / 86400)
  return future ? `in ${days}d` : `${days}d ago`
}

function formatAbsoluteTime(value: string) {
  const timestamp = Date.parse(value)
  if (!Number.isFinite(timestamp)) {
    return value || "Not scheduled"
  }

  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(timestamp)
}

function sessionMatchesFilter(session: WorkSessionSummary, filter: string) {
  switch (filter) {
    case "attention":
      return (
        session.status === "failed" ||
        session.waiting_on_human ||
        session.has_anomaly
      )
    case "running":
      return session.working
    case "waiting":
      return session.waiting_on_human
    case "paused":
      return session.status === "paused"
    case "done":
      return session.status === "completed"
    default:
      return true
  }
}

function eventMatchesFilter(event: WorkTimelineEvent, filter: string) {
  return filter === "all" || event.category === filter
}

function automationMatchesFilter(job: AutomationJob, filter: string) {
  switch (filter) {
    case "attention":
      return job.tone === "failed" || job.tone === "danger"
    case "enabled":
      return job.enabled
    case "paused":
      return !job.enabled
    default:
      return true
  }
}

function toneClasses(tone: string) {
  switch (tone) {
    case "danger":
    case "failed":
      return "border-red-500/20 bg-red-500/10 text-red-200"
    case "warn":
      return "border-amber-500/20 bg-amber-500/10 text-amber-200"
    case "good":
    case "active":
      return "border-primary/20 bg-primary/10 text-foreground"
    default:
      return "border-border bg-muted/50 text-muted-foreground"
  }
}

function sessionTone(session: WorkSessionSummary) {
  if (session.status === "failed" || session.has_anomaly) return "danger"
  if (session.waiting_on_human) return "warn"
  if (session.working) return "active"
  return "neutral"
}

function sessionHeadline(story: WorkSessionStory, fallback: string) {
  return (
    story.current_state_detail ||
    story.latest_anomaly ||
    story.latest_conclusion ||
    story.last_meaningful_step ||
    fallback
  )
}

function metricValue(sessions: WorkSessionSummary[], filter: string) {
  return sessions.filter((session) => sessionMatchesFilter(session, filter))
    .length
}

function messageTone(role: string) {
  switch (role) {
    case "user":
      return "border-primary/20 bg-primary/10"
    case "agent":
      return "border-border bg-muted/40"
    case "error":
      return "border-red-500/20 bg-red-500/10"
    default:
      return "border-border bg-muted/40"
  }
}

function messageRoleLabel(role: string) {
  switch (role) {
    case "user":
      return "Operator"
    case "agent":
      return "Local Agent"
    case "error":
      return "Error"
    default:
      return role || "Message"
  }
}

function allAutomationJobs(automations: AutomationsResponse | null) {
  if (!automations) return []
  return [
    ...automations.attention_jobs,
    ...automations.scheduled_jobs,
    ...automations.paused_jobs,
  ]
}

function App() {
  const location = useLocation()

  const [sessions, setSessions] = useState<WorkSessionSummary[]>([])
  const [selectedSessionId, setSelectedSessionId] = useState("")
  const [detail, setDetail] = useState<WorkSessionDetail | null>(null)
  const [selectedEventSeq, setSelectedEventSeq] = useState<number | null>(null)
  const [sessionQuery, setSessionQuery] = useState("")
  const deferredQuery = useDeferredValue(sessionQuery)
  const [sessionFilter, setSessionFilter] = useState("all")
  const [eventFilter, setEventFilter] = useState("all")
  const [loadingSessions, setLoadingSessions] = useState(true)
  const [loadingDetail, setLoadingDetail] = useState(false)
  const [error, setError] = useState("")
  const [detailError, setDetailError] = useState("")

  const [automations, setAutomations] = useState<AutomationsResponse | null>(
    null
  )
  const [automationQuery, setAutomationQuery] = useState("")
  const deferredAutomationQuery = useDeferredValue(automationQuery)
  const [automationFilter, setAutomationFilter] = useState("all")
  const [selectedAutomationId, setSelectedAutomationId] = useState("")
  const [loadingAutomations, setLoadingAutomations] = useState(true)
  const [automationError, setAutomationError] = useState("")

  const [chatSessions, setChatSessions] = useState<ChatSessionSummary[]>([])
  const [selectedChatSessionId, setSelectedChatSessionId] = useState("")
  const [chatDetail, setChatDetail] = useState<ChatSessionDetail | null>(null)
  const [chatQuery, setChatQuery] = useState("")
  const deferredChatQuery = useDeferredValue(chatQuery)
  const [chatDraft, setChatDraft] = useState("")
  const [chatTitle, setChatTitle] = useState("")
  const [chatStatus, setChatStatus] = useState("Ready.")
  const [chatLoadingSessions, setChatLoadingSessions] = useState(true)
  const [chatLoadingDetail, setChatLoadingDetail] = useState(false)
  const [chatError, setChatError] = useState("")
  const [chatSending, setChatSending] = useState(false)
  const transcriptEndRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    let ignore = false
    const controller = new AbortController()

    async function loadSessions() {
      try {
        if (!ignore) {
          setLoadingSessions(true)
          setError("")
        }
        const nextSessions = await fetchWorkSessions(controller.signal)
        if (ignore) return
        setSessions(nextSessions)
      } catch (nextError) {
        if (ignore) return
        setError(
          nextError instanceof Error
            ? nextError.message
            : "Unable to load sessions."
        )
      } finally {
        if (!ignore) setLoadingSessions(false)
      }
    }

    void loadSessions()
    const interval = window.setInterval(() => {
      void loadSessions()
    }, 15000)

    return () => {
      ignore = true
      controller.abort()
      window.clearInterval(interval)
    }
  }, [])

  useEffect(() => {
    if (!sessions.length) {
      setSelectedSessionId("")
      return
    }
    if (
      !selectedSessionId ||
      !sessions.some((item) => item.session_id === selectedSessionId)
    ) {
      startTransition(() => {
        setSelectedSessionId(sessions[0]?.session_id ?? "")
      })
    }
  }, [sessions, selectedSessionId])

  useEffect(() => {
    if (!selectedSessionId) {
      setDetail(null)
      setSelectedEventSeq(null)
      return
    }

    const controller = new AbortController()
    let ignore = false

    async function loadDetail() {
      try {
        setLoadingDetail(true)
        setDetailError("")
        const nextDetail = await fetchWorkSessionDetail(
          selectedSessionId,
          controller.signal
        )
        if (ignore) return
        startTransition(() => {
          setDetail(nextDetail)
          setSelectedEventSeq((current) => {
            const currentExists = nextDetail.timeline.events.some(
              (event) => event.seq === current
            )
            if (currentExists) return current
            return nextDetail.timeline.events[0]?.seq ?? null
          })
        })
      } catch (nextError) {
        if (ignore) return
        setDetailError(
          nextError instanceof Error
            ? nextError.message
            : "Unable to load session detail."
        )
      } finally {
        if (!ignore) setLoadingDetail(false)
      }
    }

    void loadDetail()
    const interval = window.setInterval(() => {
      void loadDetail()
    }, 12000)

    return () => {
      ignore = true
      controller.abort()
      window.clearInterval(interval)
    }
  }, [selectedSessionId])

  useEffect(() => {
    let ignore = false
    const controller = new AbortController()

    async function loadAutomations() {
      try {
        if (!ignore) {
          setLoadingAutomations(true)
          setAutomationError("")
        }
        const nextAutomations = await fetchAutomations(controller.signal)
        if (ignore) return
        setAutomations(nextAutomations)
      } catch (nextError) {
        if (ignore) return
        setAutomationError(
          nextError instanceof Error
            ? nextError.message
            : "Unable to load automations."
        )
      } finally {
        if (!ignore) setLoadingAutomations(false)
      }
    }

    void loadAutomations()
    const interval = window.setInterval(() => {
      void loadAutomations()
    }, 20000)

    return () => {
      ignore = true
      controller.abort()
      window.clearInterval(interval)
    }
  }, [])

  const automationJobs = allAutomationJobs(automations)

  useEffect(() => {
    if (!automationJobs.length) {
      setSelectedAutomationId("")
      return
    }
    if (
      !selectedAutomationId ||
      !automationJobs.some((job) => job.id === selectedAutomationId)
    ) {
      startTransition(() => {
        setSelectedAutomationId(automationJobs[0]?.id ?? "")
      })
    }
  }, [automationJobs, selectedAutomationId])

  useEffect(() => {
    let ignore = false
    const controller = new AbortController()

    async function loadChatSessions() {
      try {
        if (!ignore) {
          setChatLoadingSessions(true)
          setChatError("")
        }
        const nextSessions = await fetchChatSessions(controller.signal)
        if (ignore) return
        setChatSessions(nextSessions)
      } catch (nextError) {
        if (ignore) return
        setChatError(
          nextError instanceof Error
            ? nextError.message
            : "Unable to load chat sessions."
        )
      } finally {
        if (!ignore) setChatLoadingSessions(false)
      }
    }

    void loadChatSessions()
    const interval = window.setInterval(() => {
      void loadChatSessions()
    }, 12000)

    return () => {
      ignore = true
      controller.abort()
      window.clearInterval(interval)
    }
  }, [])

  useEffect(() => {
    if (!chatSessions.length) return
    if (
      !selectedChatSessionId ||
      !chatSessions.some(
        (session) => session.session_id === selectedChatSessionId
      )
    ) {
      startTransition(() => {
        setSelectedChatSessionId(chatSessions[0]?.session_id ?? "")
      })
    }
  }, [chatSessions, selectedChatSessionId])

  useEffect(() => {
    if (!selectedChatSessionId) {
      setChatDetail(null)
      return
    }

    const controller = new AbortController()
    let ignore = false

    async function loadChatDetail() {
      try {
        setChatLoadingDetail(true)
        setChatError("")
        const nextDetail = await fetchChatSessionDetail(
          selectedChatSessionId,
          controller.signal
        )
        if (ignore) return
        startTransition(() => {
          setChatDetail(nextDetail)
          setChatStatus(
            nextDetail.session.working ? "Agent is responding..." : "Ready."
          )
        })
      } catch (nextError) {
        if (ignore) return
        setChatError(
          nextError instanceof Error
            ? nextError.message
            : "Unable to load chat thread."
        )
      } finally {
        if (!ignore) setChatLoadingDetail(false)
      }
    }

    void loadChatDetail()

    return () => {
      ignore = true
      controller.abort()
    }
  }, [selectedChatSessionId])

  useEffect(() => {
    if (!selectedChatSessionId) return

    const currentLastSeq = chatDetail?.session.last_seq ?? 0
    const source = new EventSource(
      chatStreamURL(selectedChatSessionId, currentLastSeq)
    )

    const refresh = async () => {
      try {
        const [nextSessions, nextDetail] = await Promise.all([
          fetchChatSessions(),
          fetchChatSessionDetail(selectedChatSessionId),
        ])
        startTransition(() => {
          setChatSessions(nextSessions)
          setChatDetail(nextDetail)
          setChatStatus(
            nextDetail.session.working ? "Agent is responding..." : "Ready."
          )
        })
      } catch (nextError) {
        setChatStatus(
          nextError instanceof Error
            ? nextError.message
            : "Chat refresh failed."
        )
      }
    }

    source.onopen = () => {
      setChatStatus("Live stream attached.")
    }
    source.addEventListener("session-event", () => {
      void refresh()
    })
    source.onerror = () => {
      setChatStatus("Stream interrupted. Reconnecting on next refresh...")
      source.close()
    }

    return () => {
      source.close()
    }
  }, [selectedChatSessionId, chatDetail?.session.last_seq])

  useEffect(() => {
    if (!chatDetail?.messages.length) return
    transcriptEndRef.current?.scrollIntoView({
      behavior: "smooth",
      block: "end",
    })
  }, [chatDetail?.messages.length, selectedChatSessionId])

  const visibleSessions = sessions.filter((session) => {
    const query = normalize(deferredQuery)
    const haystack = normalize(
      [
        session.title,
        session.conversation_id,
        session.status,
        session.latest_digest,
        session.waiting_reason,
        session.story.goal,
        session.story.current_state,
        session.story.current_state_detail,
        session.story.latest_conclusion,
      ]
        .filter(Boolean)
        .join(" ")
    )

    const queryMatch = !query || haystack.includes(query)
    return queryMatch && sessionMatchesFilter(session, sessionFilter)
  })

  useEffect(() => {
    if (!visibleSessions.length) return
    if (
      !visibleSessions.some(
        (session) => session.session_id === selectedSessionId
      )
    ) {
      startTransition(() => {
        setSelectedSessionId(visibleSessions[0]?.session_id ?? "")
      })
    }
  }, [selectedSessionId, visibleSessions])

  const filteredEvents = (detail?.timeline.events || []).filter((event) =>
    eventMatchesFilter(event, eventFilter)
  )

  const selectedEvent =
    filteredEvents.find((event) => event.seq === selectedEventSeq) ||
    filteredEvents[0] ||
    null

  const visibleAutomationJobs = automationJobs.filter((job) => {
    const query = normalize(deferredAutomationQuery)
    const haystack = normalize(
      [
        job.id,
        job.session_id,
        job.message,
        job.message_full,
        job.created_by,
        job.cron_expr,
        job.run_status,
      ]
        .filter(Boolean)
        .join(" ")
    )

    return (
      (!query || haystack.includes(query)) &&
      automationMatchesFilter(job, automationFilter)
    )
  })

  useEffect(() => {
    if (!visibleAutomationJobs.length) return
    if (!visibleAutomationJobs.some((job) => job.id === selectedAutomationId)) {
      startTransition(() => {
        setSelectedAutomationId(visibleAutomationJobs[0]?.id ?? "")
      })
    }
  }, [selectedAutomationId, visibleAutomationJobs])

  const selectedAutomation =
    visibleAutomationJobs.find((job) => job.id === selectedAutomationId) ||
    automationJobs.find((job) => job.id === selectedAutomationId) ||
    null

  const visibleChatSessions = chatSessions.filter((session) => {
    const query = normalize(deferredChatQuery)
    const haystack = normalize(
      [session.title, session.status, session.preview].filter(Boolean).join(" ")
    )
    return !query || haystack.includes(query)
  })

  async function handleSendChat() {
    const message = chatDraft.trim()
    if (!message || chatSending) return

    try {
      setChatSending(true)
      setChatError("")
      setChatStatus(
        selectedChatSessionId ? "Sending..." : "Starting local chat..."
      )
      if (selectedChatSessionId) {
        await sendChatMessage(selectedChatSessionId, message)
        setChatDraft("")
        setChatStatus("Waiting for local agent...")
        const [nextSessions, nextDetail] = await Promise.all([
          fetchChatSessions(),
          fetchChatSessionDetail(selectedChatSessionId),
        ])
        startTransition(() => {
          setChatSessions(nextSessions)
          setChatDetail(nextDetail)
        })
        return
      }

      const created = await createChatSession({
        title: chatTitle.trim() || undefined,
        message,
      })
      setChatDraft("")
      setChatTitle("")
      setChatStatus("Waiting for local agent...")
      startTransition(() => {
        setSelectedChatSessionId(created.session.session_id)
        setChatDetail(created)
      })
      const nextSessions = await fetchChatSessions()
      startTransition(() => {
        setChatSessions(nextSessions)
      })
    } catch (nextError) {
      setChatError(
        nextError instanceof Error
          ? nextError.message
          : "Unable to send message."
      )
      setChatStatus("Send failed.")
    } finally {
      setChatSending(false)
    }
  }

  async function handleCreateEmptyThread() {
    try {
      setChatSending(true)
      setChatError("")
      setChatStatus("Creating thread...")
      const created = await createChatSession({
        title: chatTitle.trim() || undefined,
      })
      setChatTitle("")
      startTransition(() => {
        setSelectedChatSessionId(created.session.session_id)
        setChatDetail(created)
      })
      const nextSessions = await fetchChatSessions()
      startTransition(() => {
        setChatSessions(nextSessions)
      })
      setChatStatus("Ready.")
    } catch (nextError) {
      setChatError(
        nextError instanceof Error
          ? nextError.message
          : "Unable to create thread."
      )
      setChatStatus("Create failed.")
    } finally {
      setChatSending(false)
    }
  }

  const summaryMetrics = [
    {
      label: "Active Sessions",
      value: sessions.filter((session) => session.status === "active").length,
      tone: "active",
      icon: Lightning,
    },
    {
      label: "Needs Attention",
      value: metricValue(sessions, "attention"),
      tone: "danger",
      icon: WarningCircle,
    },
    {
      label: "Enabled Cron",
      value: automations?.summary.enabled ?? 0,
      tone: "active",
      icon: ClockCountdown,
    },
    {
      label: "Live Threads",
      value: chatSessions.filter((session) => session.working).length,
      tone: "warn",
      icon: ChatCircleText,
    },
  ] as const

  return (
    <div className="h-svh overflow-hidden bg-background text-foreground">
      <div className="mx-auto flex h-full max-w-[1820px] flex-col gap-4 px-4 py-3">
        <header className="flex items-center justify-between border border-border bg-card/80 px-4 py-2 shadow-sm">
          <div className="flex items-center gap-4">
            <span className="inline-flex items-center gap-2 text-sm font-medium">
              <Wrench className="size-4" weight="fill" />
              Gopher
            </span>
            <div className="flex items-center gap-1 border border-border bg-muted/40 p-0.5">
              <Link
                to="/chat"
                className={cn(
                  "px-3 py-1 text-[11px] tracking-[0.2em] uppercase transition-all rounded-sm",
                  location.pathname === "/chat"
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                Chat
              </Link>
              <Link
                to="/sessions"
                className={cn(
                  "px-3 py-1 text-[11px] tracking-[0.2em] uppercase transition-all rounded-sm",
                  location.pathname === "/sessions"
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                Sessions
              </Link>
              <Link
                to="/automations"
                className={cn(
                  "px-3 py-1 text-[11px] tracking-[0.2em] uppercase transition-all rounded-sm",
                  location.pathname === "/automations"
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                Automations
              </Link>
            </div>
          </div>
          <div className="flex items-center gap-3 text-[11px]">
            {summaryMetrics.map((item) => {
              const Icon = item.icon
              return (
                <div
                  key={item.label}
                  className={cn(
                    "flex items-center gap-1.5 border px-2 py-1",
                    toneClasses(item.tone)
                  )}
                >
                  <Icon className="size-3" />
                  <span className="font-medium">{item.value}</span>
                  <span className="text-muted-foreground">{item.label}</span>
                </div>
              )
            })}
          </div>
        </header>

        <Routes>
          <Route path="/sessions" element={
            <div className="grid flex-1 gap-6 xl:grid-cols-[320px_minmax(0,1fr)_380px]">
              <Card className="min-h-0 border-border bg-card/80 shadow-xl shadow-black/20">
                <CardHeader className="gap-4 border-b border-border/60">
                  <div className="flex items-start justify-between gap-4">
                    <div>
                      <CardTitle className="text-base">Sessions</CardTitle>
                      <CardDescription>
                        Live operator work ordered by urgency and freshness.
                      </CardDescription>
                    </div>
                    <Badge
                      variant="outline"
                      className="border-border bg-background/40 px-3 py-1 text-[10px] tracking-[0.18em] uppercase"
                    >
                      {visibleSessions.length} visible
                    </Badge>
                  </div>

                  <div className="relative">
                    <MagnifyingGlass className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      value={sessionQuery}
                      onChange={(event) => setSessionQuery(event.target.value)}
                      placeholder="Search sessions, topics, or status"
                      className="h-11 border-border bg-background/50 pl-9"
                    />
                  </div>

                  <div className="flex items-center gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      className="border-border bg-background/50"
                      onClick={() => setSessionQuery("")}
                    >
                      Clear Search
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      className="border-border bg-background/50"
                      onClick={() => {
                        startTransition(() => {
                          setSelectedSessionId("")
                        })
                      }}
                    >
                      Reset Focus
                    </Button>
                  </div>

                  <Tabs value={sessionFilter} onValueChange={setSessionFilter}>
                    <TabsList
                      variant="line"
                      className="grid w-full grid-cols-3 gap-1 border border-border bg-muted/40 p-1"
                    >
                      {sessionFilters.map((filter) => (
                        <TabsTrigger
                          key={filter.value}
                          value={filter.value}
                          className="text-[10px] tracking-[0.18em] uppercase"
                        >
                          {filter.label}
                        </TabsTrigger>
                      ))}
                    </TabsList>
                  </Tabs>

                  {error ? <InlineError>{error}</InlineError> : null}
                </CardHeader>

                <CardContent className="min-h-0 flex-1">
                  <ScrollArea className="h-[calc(100svh-24rem)] pr-3 xl:h-[calc(100svh-18rem)]">
                    <div className="space-y-3">
                      {loadingSessions && !sessions.length ? (
                        <EmptyPanel copy="Loading sessions..." />
                      ) : null}

                      {!loadingSessions && !visibleSessions.length ? (
                        <EmptyPanel copy="No sessions match the current search and filters." />
                      ) : null}

                      {visibleSessions.map((session) => {
                        const active = session.session_id === selectedSessionId
                        const story = session.story || {}
                        return (
                          <button
                            key={session.session_id}
                            type="button"
                            onClick={() => {
                              startTransition(() => {
                                setSelectedSessionId(session.session_id)
                              })
                            }}
                            className={cn(
                              "w-full border p-4 text-left transition-all",
                              active
                                ? "border-primary bg-primary/10"
                                : "border-border bg-background/40 hover:border-foreground/15 hover:bg-muted/20"
                            )}
                          >
                            <div className="mb-3 flex items-start justify-between gap-3">
                              <div className="space-y-1">
                                <div className="text-sm font-medium">
                                  {session.title}
                                </div>
                                <div className="text-[11px] tracking-[0.18em] text-muted-foreground uppercase">
                                  {session.conversation_id || session.session_id}
                                </div>
                              </div>
                              <Badge
                                variant="outline"
                                className={cn(
                                  "border px-2.5 py-1 text-[10px] tracking-[0.18em] uppercase",
                                  toneClasses(sessionTone(session))
                                )}
                              >
                                {session.priority_label}
                              </Badge>
                            </div>

                            <p className="line-clamp-3 text-sm leading-6 text-muted-foreground">
                              {sessionHeadline(story, session.latest_digest)}
                            </p>

                            {session.waiting_reason ? (
                              <div className="mt-3 rounded-2xl border border-amber-400/15 bg-amber-400/8 px-3 py-2 text-xs leading-5 text-amber-100">
                                {session.waiting_reason}
                              </div>
                            ) : null}

                            <div className="mt-4 flex items-center justify-between gap-3 text-[11px] tracking-[0.16em] text-muted-foreground uppercase">
                              <span>
                                {formatRelativeTime(session.updated_at)}
                              </span>
                              <span>
                                {session.working
                                  ? "Live"
                                  : session.waiting_on_human
                                    ? "Human"
                                    : session.status}
                              </span>
                            </div>
                          </button>
                        )
                      })}
                    </div>
                  </ScrollArea>
                </CardContent>
              </Card>

              <div className="grid min-h-0 gap-6 xl:grid-rows-[auto_minmax(0,1fr)_auto]">
                <WorkSummaryCard detail={detail} />
                <WorkTimelineCard
                  detail={detail}
                  detailError={detailError}
                  loadingDetail={loadingDetail}
                  eventFilter={eventFilter}
                  setEventFilter={setEventFilter}
                  filteredEvents={filteredEvents}
                  selectedEvent={selectedEvent}
                  setSelectedEventSeq={setSelectedEventSeq}
                />
                <WorkInspectorCard selectedEvent={selectedEvent} />
              </div>
            </div>
          } />
          <Route path="/automations" element={
            <div className="grid flex-1 gap-6 xl:grid-cols-[320px_minmax(0,1fr)_380px]">
              <Card className="min-h-0 border-border bg-card/80 shadow-xl shadow-black/20">
                <CardHeader className="gap-4 border-b border-border/60">
                  <div className="flex items-start justify-between gap-4">
                    <div>
                      <CardTitle className="text-base">Automations</CardTitle>
                      <CardDescription>
                        Scheduled and held cron jobs from the control store.
                      </CardDescription>
                    </div>
                    <Badge
                      variant="outline"
                      className="border-border bg-background/40 px-3 py-1 text-[10px] tracking-[0.18em] uppercase"
                    >
                      {automations?.summary.total ?? 0} jobs
                    </Badge>
                  </div>

                  <div className="grid grid-cols-2 gap-3">
                    <MetricTile
                      label="Enabled"
                      value={automations?.summary.enabled ?? 0}
                      tone="active"
                    />
                    <MetricTile
                      label="Held"
                      value={automations?.summary.paused ?? 0}
                      tone="warn"
                    />
                    <MetricTile
                      label="Failures"
                      value={automations?.summary.failed ?? 0}
                      tone="danger"
                    />
                    <MetricTile
                      label="Total"
                      value={automations?.summary.total ?? 0}
                      tone="neutral"
                    />
                  </div>

                  <div className="relative">
                    <MagnifyingGlass className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      value={automationQuery}
                      onChange={(event) =>
                        setAutomationQuery(event.target.value)
                      }
                      placeholder="Search cron jobs"
                      className="h-11 border-border bg-background/50 pl-9"
                    />
                  </div>

                  <Tabs
                    value={automationFilter}
                    onValueChange={setAutomationFilter}
                  >
                    <TabsList
                      variant="line"
                      className="grid w-full grid-cols-4 gap-1 border border-border bg-muted/40 p-1"
                    >
                      {automationFilters.map((filter) => (
                        <TabsTrigger
                          key={filter.value}
                          value={filter.value}
                          className="text-[10px] tracking-[0.16em] uppercase"
                        >
                          {filter.label}
                        </TabsTrigger>
                      ))}
                    </TabsList>
                  </Tabs>

                  {automationError ? (
                    <InlineError>{automationError}</InlineError>
                  ) : null}
                  {automations && !automations.has_cron_store ? (
                    <InlineError>
                      Automation storage is not available in this panel.
                    </InlineError>
                  ) : null}
                  {automations?.error ? (
                    <InlineError>{automations.error}</InlineError>
                  ) : null}
                </CardHeader>

                <CardContent className="min-h-0 flex-1">
                  <ScrollArea className="h-[calc(100svh-31rem)] pr-3 xl:h-[calc(100svh-24rem)]">
                    <div className="space-y-3">
                      {loadingAutomations && !automationJobs.length ? (
                        <EmptyPanel copy="Loading cron jobs..." />
                      ) : null}

                      {!loadingAutomations && !visibleAutomationJobs.length ? (
                        <EmptyPanel copy="No cron jobs match the current search and filter." />
                      ) : null}

                      {visibleAutomationJobs.map((job) => (
                        <button
                          key={job.id}
                          type="button"
                          onClick={() => setSelectedAutomationId(job.id)}
                          className={cn(
                            "w-full border p-4 text-left transition-all",
                            job.id === selectedAutomationId
                              ? "border-primary bg-primary/10"
                              : "border-border bg-background/40 hover:border-foreground/15 hover:bg-muted/20"
                          )}
                        >
                          <div className="mb-3 flex items-start justify-between gap-3">
                            <div className="space-y-1">
                              <div className="text-sm font-medium">
                                {job.id}
                              </div>
                              <div className="text-[11px] tracking-[0.18em] text-muted-foreground uppercase">
                                {job.session_id || "No linked session"}
                              </div>
                            </div>
                            <Badge
                              variant="outline"
                              className={cn(
                                "border px-2.5 py-1 text-[10px] tracking-[0.18em] uppercase",
                                toneClasses(job.tone)
                              )}
                            >
                              {job.run_status}
                            </Badge>
                          </div>

                          <p className="line-clamp-3 text-sm leading-6 text-muted-foreground">
                            {job.message || "No automation message configured."}
                          </p>

                          <div className="mt-4 grid gap-2 text-[11px] tracking-[0.14em] text-muted-foreground uppercase">
                            <div className="flex items-center justify-between gap-3">
                              <span>{job.enabled ? "Enabled" : "Held"}</span>
                              <span>{formatRelativeTime(job.updated_at)}</span>
                            </div>
                            <div className="flex items-center justify-between gap-3">
                              <span>Next</span>
                              <span>{formatAbsoluteTime(job.next_run_at)}</span>
                            </div>
                          </div>
                        </button>
                      ))}
                    </div>
                  </ScrollArea>
                </CardContent>
              </Card>

              <Card className="border-border bg-card/80 shadow-xl shadow-black/20">
                <CardHeader className="border-b border-border/60">
                  <CardTitle className="text-base">Automation Detail</CardTitle>
                  <CardDescription>
                    The selected job stays linked to its session so you can jump
                    straight into the queue.
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  {selectedAutomation ? (
                    <div className="space-y-4">
                      <div className="flex items-start justify-between gap-3">
                        <div>
                          <div className="text-lg font-medium">
                            {selectedAutomation.id}
                          </div>
                          <div className="text-[11px] tracking-[0.18em] text-muted-foreground uppercase">
                            {selectedAutomation.session_id ||
                              "No linked session"}
                          </div>
                        </div>
                        <Badge
                          variant="outline"
                          className={cn(
                            "border px-2.5 py-1 text-[10px] tracking-[0.18em] uppercase",
                            toneClasses(selectedAutomation.tone)
                          )}
                        >
                          {selectedAutomation.run_status}
                        </Badge>
                      </div>

                      <div className="border border-border/60 bg-background/40 p-4 text-sm leading-6 text-muted-foreground">
                        {selectedAutomation.message_full ||
                          selectedAutomation.message ||
                          "No automation message configured."}
                      </div>

                      <div className="grid gap-3 sm:grid-cols-2">
                        <InfoTile
                          label="Cron"
                          value={selectedAutomation.cron_expr || "-"}
                        />
                        <InfoTile
                          label="Timezone"
                          value={selectedAutomation.timezone || "-"}
                        />
                        <InfoTile
                          label="Last Run"
                          value={formatAbsoluteTime(
                            selectedAutomation.last_run_at
                          )}
                        />
                        <InfoTile
                          label="Next Run"
                          value={formatAbsoluteTime(
                            selectedAutomation.next_run_at
                          )}
                        />
                      </div>

                      <div className="flex flex-wrap items-center gap-2">
                        <Button
                          variant="outline"
                          className="border-border bg-background/50"
                          disabled={!selectedAutomation.session_id}
                          onClick={() => {
                            if (!selectedAutomation.session_id) return
                            startTransition(() => {
                              setSelectedSessionId(
                                selectedAutomation.session_id
                              )
                            })
                          }}
                        >
                          Open Linked Session
                        </Button>
                        <div className="text-xs text-muted-foreground">
                          Created by{" "}
                          {selectedAutomation.created_by || "unknown"}
                        </div>
                      </div>
                    </div>
                  ) : (
                    <EmptyPanel copy="Select a cron job to inspect it." />
                  )}
                </CardContent>
              </Card>
            </div>
          } />
          <Route path="/chat" element={
            <div className="grid flex-1 gap-6 xl:grid-cols-[320px_minmax(0,1fr)_340px]">
              <Card className="min-h-0 border-border bg-card/80 shadow-xl shadow-black/20">
                <CardHeader className="gap-4 border-b border-border/60">
                  <div className="flex items-start justify-between gap-4">
                    <div>
                      <CardTitle className="text-base">Threads</CardTitle>
                      <CardDescription>
                        Local operator conversations with live session-backed
                        state.
                      </CardDescription>
                    </div>
                    <Button
                      variant="outline"
                      size="sm"
                      className="border-border bg-background/50"
                      onClick={() => {
                        startTransition(() => {
                          setSelectedChatSessionId("")
                          setChatDetail(null)
                          setChatStatus("Drafting a new thread.")
                        })
                      }}
                    >
                      <Plus className="size-4" />
                      New Draft
                    </Button>
                  </div>

                  <div className="relative">
                    <MagnifyingGlass className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      value={chatQuery}
                      onChange={(event) => setChatQuery(event.target.value)}
                      placeholder="Search local threads"
                      className="h-11 border-border bg-background/50 pl-9"
                    />
                  </div>

                  {chatError ? <InlineError>{chatError}</InlineError> : null}
                </CardHeader>

                <CardContent className="min-h-0 flex-1">
                  <ScrollArea className="h-[calc(100svh-19rem)] pr-3">
                    <div className="space-y-3">
                      {chatLoadingSessions && !chatSessions.length ? (
                        <EmptyPanel copy="Loading chat threads..." />
                      ) : null}

                      {!chatLoadingSessions && !visibleChatSessions.length ? (
                        <EmptyPanel copy="No chat threads match the current search." />
                      ) : null}

                      {visibleChatSessions.map((session) => (
                        <button
                          key={session.session_id}
                          type="button"
                          onClick={() => {
                            startTransition(() => {
                              setSelectedChatSessionId(session.session_id)
                            })
                          }}
                          className={cn(
                            "w-full border p-4 text-left transition-all",
                            session.session_id === selectedChatSessionId
                              ? "border-primary bg-primary/10"
                              : "border-border bg-background/40 hover:border-foreground/15 hover:bg-muted/20"
                          )}
                        >
                          <div className="mb-3 flex items-start justify-between gap-3">
                            <div className="space-y-1">
                              <div className="text-sm font-medium">
                                {session.title}
                              </div>
                              <div className="text-[11px] tracking-[0.18em] text-muted-foreground uppercase">
                                {session.session_id}
                              </div>
                            </div>
                            <Badge
                              variant="outline"
                              className={cn(
                                "border px-2.5 py-1 text-[10px] tracking-[0.18em] uppercase",
                                toneClasses(
                                  session.working ? "active" : "neutral"
                                )
                              )}
                            >
                              {session.working ? "Live" : session.status}
                            </Badge>
                          </div>
                          <p className="line-clamp-3 text-sm leading-6 text-muted-foreground">
                            {session.preview || "No messages yet."}
                          </p>
                          <div className="mt-4 text-[11px] tracking-[0.16em] text-muted-foreground uppercase">
                            {formatRelativeTime(session.updated_at)}
                          </div>
                        </button>
                      ))}
                    </div>
                  </ScrollArea>
                </CardContent>
              </Card>

              <Card className="flex min-h-0 flex-col border-border bg-card/80 shadow-xl shadow-black/20">
                <CardHeader className="shrink-0 gap-4 border-b border-border/60">
                  <div className="flex flex-wrap items-start justify-between gap-4">
                    <div className="space-y-1">
                      <CardTitle className="text-base">
                        {chatDetail?.session.title || "New thread"}
                      </CardTitle>
                      <CardDescription>{chatStatus}</CardDescription>
                    </div>
                    <Badge
                      variant="outline"
                      className={cn(
                        "border px-3 py-1 text-[10px] tracking-[0.18em] uppercase",
                        toneClasses(
                          chatDetail?.session.working ? "active" : "neutral"
                        )
                      )}
                    >
                      {chatDetail?.session.working ? "Streaming" : "Idle"}
                    </Badge>
                  </div>
                </CardHeader>

                <CardContent className="flex min-h-0 flex-1 flex-col overflow-hidden">
                  <ScrollArea className="flex-1 overflow-hidden pr-3">
                    <div className="space-y-4">
                      {chatLoadingDetail && !chatDetail ? (
                        <EmptyPanel copy="Loading chat transcript..." />
                      ) : null}

                      {!selectedChatSessionId && !chatDetail?.messages?.length ? (
                        <div className="border border-dashed border-border bg-background/40 p-6">
                          <div className="max-w-xl space-y-3">
                            <div className="text-lg font-medium">
                              Start a new operator thread.
                            </div>
                            <p className="text-sm leading-6 text-muted-foreground">
                              Type directly below to open a new local thread
                              backed by the session runtime.
                            </p>
                          </div>
                        </div>
                      ) : null}

                      {(chatDetail?.messages || []).map((message) => (
                        <ChatBubble key={message.seq} message={message} />
                      ))}
                      <div ref={transcriptEndRef} />
                    </div>
                  </ScrollArea>
                </CardContent>

                <div className="shrink-0 grid gap-3 border-t border-border/60 p-4">
                  {!selectedChatSessionId ? (
                    <Input
                      value={chatTitle}
                      onChange={(event) => setChatTitle(event.target.value)}
                      placeholder="Optional thread title"
                      className="h-11 border-border bg-background/50"
                    />
                  ) : null}
                  <textarea
                    value={chatDraft}
                    onChange={(event) => setChatDraft(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter" && !event.shiftKey) {
                        event.preventDefault()
                        void handleSendChat()
                      }
                    }}
                    placeholder="Ask for a summary, inspect a session, or draft an operator response."
                    className="min-h-24 resize-none border border-border bg-background/50 px-4 py-4 text-sm leading-6 outline-none placeholder:text-muted-foreground focus:border-ring"
                  />
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <p className="text-xs text-muted-foreground">
                      Enter sends immediately. Shift+Enter adds a newline.
                    </p>
                    <div className="flex items-center gap-2">
                      {!selectedChatSessionId ? (
                        <Button
                          variant="outline"
                          className="border-border bg-background/50"
                          onClick={() => void handleCreateEmptyThread()}
                          disabled={chatSending}
                        >
                          <Plus className="size-4" />
                          New Empty Thread
                        </Button>
                      ) : null}
                      <Button
                        onClick={() => void handleSendChat()}
                        disabled={chatSending || !chatDraft.trim()}
                      >
                        <PaperPlaneTilt className="size-4" />
                        {selectedChatSessionId ? "Send" : "Create and Send"}
                      </Button>
                    </div>
                  </div>
                </div>
              </Card>

              <Card className="border-border bg-card/80 shadow-xl shadow-black/20">
                <CardHeader className="border-b border-border/60">
                  <CardTitle className="text-base">Thread Desk</CardTitle>
                  <CardDescription>
                    Live thread status and current context.
                  </CardDescription>
                </CardHeader>
                <CardContent className="h-full min-h-0 overflow-hidden">
                  <ScrollArea className="h-full pr-3">
                    <div className="space-y-5">
                      <div className="space-y-3">
                        <div className="text-[11px] tracking-[0.22em] text-muted-foreground uppercase">
                          Current Thread
                        </div>
                        {chatDetail ? (
                          <div className="space-y-3">
                            <InfoTile
                              label="Session"
                              value={chatDetail.session.session_id}
                            />
                            <InfoTile
                              label="Updated"
                              value={formatAbsoluteTime(
                                chatDetail.session.updated_at
                              )}
                            />
                            <InfoTile
                              label="Messages"
                              value={String(chatDetail.messages.length)}
                            />
                            <InfoTile
                              label="Status"
                              value={
                                chatDetail.session.working
                                  ? "Agent responding"
                                  : chatDetail.session.status
                              }
                            />
                          </div>
                        ) : (
                          <EmptyPanel copy="Pick an existing thread or start a new one." />
                        )}
                      </div>

                      <Separator className="bg-border/60" />

                      <div className="space-y-3">
                        <div className="text-[11px] tracking-[0.22em] text-muted-foreground uppercase">
                          Chat Surface
                        </div>
                        <div className="grid gap-3">
                          <FeatureChip
                            icon={Lightning}
                            title="Live updates"
                            copy="Thread changes follow the backend stream and refresh the transcript automatically."
                          />
                          <FeatureChip
                            icon={ListBullets}
                            title="Markdown aware"
                            copy="Agent replies render links, lists, code blocks, and tables directly in the transcript."
                          />
                          <FeatureChip
                            icon={CalendarDots}
                            title="Persistent"
                            copy="Creating or replying still produces real local session data, not mock-only UI state."
                          />
                        </div>
                      </div>
                    </div>
                  </ScrollArea>
                </CardContent>
              </Card>
            </div>
          } />
          <Route path="/" element={<Navigate to="/chat" replace />} />
        </Routes>
      </div>
    </div>
  )
}

function MetricTile({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone: string
}) {
  return (
    <div className={cn("border px-4 py-3", toneClasses(tone))}>
      <div className="text-[11px] tracking-[0.18em] text-inherit uppercase opacity-80">
        {label}
      </div>
      <div className="mt-2 text-2xl font-semibold tracking-tight text-foreground">
        {value}
      </div>
    </div>
  )
}

function InfoTile({ label, value }: { label: string; value: string }) {
  return (
    <div className="border border-border/60 bg-background/40 px-4 py-3">
      <div className="text-[11px] tracking-[0.18em] text-muted-foreground uppercase">
        {label}
      </div>
      <div className="mt-2 text-sm leading-6 text-foreground/85">
        {value || "-"}
      </div>
    </div>
  )
}

function FeatureChip({
  icon: Icon,
  title,
  copy,
}: {
  icon: typeof Lightning
  title: string
  copy: string
}) {
  return (
    <div className="border border-border/60 bg-background/40 px-4 py-3">
      <div className="flex items-center gap-2 text-sm font-medium">
        <Icon className="size-4 text-primary" />
        {title}
      </div>
      <p className="mt-2 text-sm leading-6 text-muted-foreground">{copy}</p>
    </div>
  )
}

function InlineError({ children }: { children: string }) {
  return (
    <div className="rounded-[20px] border border-red-500/20 bg-red-500/10 px-4 py-3 text-xs leading-5 text-red-100">
      {children}
    </div>
  )
}

function EmptyPanel({ copy }: { copy: string }) {
  return (
    <div className="border border-border/60 bg-background/40 px-4 py-6 text-sm leading-6 text-muted-foreground">
      {copy}
    </div>
  )
}

function WorkSummaryCard({ detail }: { detail: WorkSessionDetail | null }) {
  return (
    <Card className="border-border bg-card/80 shadow-xl shadow-black/20">
      <CardHeader className="gap-4 border-b border-border/60">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="space-y-1">
            <CardTitle className="text-xl">
              {detail?.session.title || "Select a session"}
            </CardTitle>
            <CardDescription>
              {detail?.session.conversation_id || "No session selected"}
            </CardDescription>
          </div>
          {detail ? (
            <Badge
              variant="outline"
              className={cn(
                "border px-3 py-1 text-[10px] tracking-[0.18em] uppercase",
                toneClasses(sessionTone(detail.session))
              )}
            >
              {detail.session.priority_label}
            </Badge>
          ) : null}
        </div>
      </CardHeader>
      <CardContent className="grid gap-4 lg:grid-cols-[minmax(0,1.15fr)_minmax(300px,0.85fr)]">
        <div className="space-y-4">
          <div className="border border-border/60 bg-background/40 p-4">
            <div className="text-[11px] tracking-[0.24em] text-muted-foreground uppercase">
              Operator Ask
            </div>
            <div className="mt-3 text-sm leading-7 text-foreground/85">
              {detail?.story.goal || "Choose a session to load the full story."}
            </div>
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            {[
              {
                label: "Current State",
                value:
                  detail?.story.current_state ||
                  detail?.session.status ||
                  "Idle",
                copy:
                  detail?.story.current_state_detail || "No state detail yet.",
              },
              {
                label: "Latest Conclusion",
                value: detail?.story.latest_conclusion || "Waiting for signal",
                copy:
                  detail?.story.last_meaningful_step ||
                  detail?.session.latest_digest ||
                  "No recent conclusion.",
              },
            ].map((item) => (
              <div
                key={item.label}
                className="border border-border/60 bg-background/40 p-4"
              >
                <div className="text-[11px] tracking-[0.22em] text-muted-foreground uppercase">
                  {item.label}
                </div>
                <div className="mt-2 text-sm font-medium">{item.value}</div>
                <p className="mt-2 text-xs leading-5 text-muted-foreground">
                  {item.copy}
                </p>
              </div>
            ))}
          </div>
        </div>

        <div className="grid gap-3">
          <div className="grid grid-cols-2 gap-3">
            {Object.entries(detail?.counts || {})
              .sort(([left], [right]) => left.localeCompare(right))
              .map(([key, value]) => (
                <div
                  key={key}
                  className="border border-border/60 bg-background/40 p-4"
                >
                  <div className="text-[11px] tracking-[0.22em] text-muted-foreground uppercase">
                    {key}
                  </div>
                  <div className="mt-2 text-2xl font-semibold">{value}</div>
                </div>
              ))}
          </div>
          <ContextHealthCard contextHealth={detail?.context_health || null} />
        </div>
      </CardContent>
    </Card>
  )
}

function ContextHealthCard({
  contextHealth,
}: {
  contextHealth: WorkContextHealth | null
}) {
  return (
    <div className="border border-border/60 bg-background/40 p-4">
      <div className="text-[11px] tracking-[0.22em] text-muted-foreground uppercase">
        Context Health
      </div>
      {contextHealth ? (
        <div className="mt-3 space-y-2 text-sm text-muted-foreground">
          <div className="text-base font-medium text-foreground">
            {contextHealth.model_display}
          </div>
          <div>
            Tokens: {contextHealth.estimated_input_tokens.toLocaleString()} /
            reserve {contextHealth.reserve_tokens.toLocaleString()}
          </div>
          <div>Recent messages: {contextHealth.recent_messages}</div>
          <div>Memory: {contextHealth.memory}</div>
          <div>Compaction: {contextHealth.compaction}</div>
        </div>
      ) : (
        <div className="mt-3 text-sm text-muted-foreground">
          No context health attached to this session.
        </div>
      )}
    </div>
  )
}

function WorkTimelineCard({
  detail,
  detailError,
  loadingDetail,
  eventFilter,
  setEventFilter,
  filteredEvents,
  selectedEvent,
  setSelectedEventSeq,
}: {
  detail: WorkSessionDetail | null
  detailError: string
  loadingDetail: boolean
  eventFilter: string
  setEventFilter: (value: string) => void
  filteredEvents: WorkTimelineEvent[]
  selectedEvent: WorkTimelineEvent | null
  setSelectedEventSeq: (value: number) => void
}) {
  return (
    <Card className="min-h-0 border-border bg-card/80 shadow-xl shadow-black/20">
      <CardHeader className="gap-4 border-b border-border/60">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle className="text-base">Timeline</CardTitle>
            <CardDescription>
              Narrative event log from the live session runtime.
            </CardDescription>
          </div>
          <Tabs value={eventFilter} onValueChange={setEventFilter}>
            <TabsList
              variant="line"
              className="border border-border bg-muted/40 p-1"
            >
              {eventFilters.map((filter) => (
                <TabsTrigger
                  key={filter.value}
                  value={filter.value}
                  className="text-[10px] tracking-[0.16em] uppercase"
                >
                  {filter.label}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
        </div>
      </CardHeader>
      <CardContent className="min-h-0">
        {detailError ? <InlineError>{detailError}</InlineError> : null}

        {loadingDetail && !detail ? (
          <EmptyPanel copy="Loading session detail..." />
        ) : null}

        <ScrollArea className="h-[calc(100svh-38rem)] pr-3 xl:h-[calc(100svh-31rem)]">
          <div className="space-y-3">
            {filteredEvents.map((event) => (
              <button
                key={event.seq}
                type="button"
                onClick={() => {
                  startTransition(() => {
                    setSelectedEventSeq(event.seq)
                  })
                }}
                className={cn(
                  "w-full border p-4 text-left transition-all",
                  selectedEvent?.seq === event.seq
                    ? "border-primary bg-primary/10"
                    : "border-border bg-background/40 hover:border-foreground/15 hover:bg-muted/20"
                )}
              >
                <div className="mb-3 flex items-start justify-between gap-3">
                  <div className="space-y-1">
                    <div className="text-[11px] tracking-[0.22em] text-muted-foreground uppercase">
                      {event.type_label || event.type}
                    </div>
                    <div className="flex items-center gap-2 text-sm font-medium">
                      <span className="text-base">{event.emoji}</span>
                      <span>{event.title}</span>
                    </div>
                  </div>
                  <Badge
                    variant="outline"
                    className={cn(
                      "border px-2.5 py-1 text-[10px] tracking-[0.18em] uppercase",
                      toneClasses(event.tone)
                    )}
                  >
                    #{event.seq}
                  </Badge>
                </div>
                {event.subtitle ? (
                  <p className="text-sm leading-6 text-muted-foreground">
                    {event.subtitle}
                  </p>
                ) : null}
                <div className="mt-4 flex items-center justify-between gap-3 text-[11px] tracking-[0.16em] text-muted-foreground uppercase">
                  <span>{event.from}</span>
                  <span>{formatRelativeTime(event.timestamp)}</span>
                </div>
              </button>
            ))}
          </div>
        </ScrollArea>
      </CardContent>
    </Card>
  )
}

function WorkInspectorCard({
  selectedEvent,
}: {
  selectedEvent: WorkTimelineEvent | null
}) {
  return (
    <Card className="border-border bg-card/80 shadow-xl shadow-black/20">
      <CardHeader className="gap-4 border-b border-border/60">
        <CardTitle className="text-base">Event Detail</CardTitle>
        <CardDescription>
          Focused payload and key facts for the selected timeline event.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {selectedEvent ? (
          <div className="space-y-5">
            <div className="flex items-start justify-between gap-3">
              <div className="space-y-2">
                <div className="text-[11px] tracking-[0.24em] text-muted-foreground uppercase">
                  {selectedEvent.type_label || selectedEvent.type}
                </div>
                <div className="text-lg font-medium">{selectedEvent.title}</div>
                {selectedEvent.subtitle ? (
                  <p className="text-sm leading-6 text-muted-foreground">
                    {selectedEvent.subtitle}
                  </p>
                ) : null}
              </div>
              <Badge
                variant="outline"
                className={cn(
                  "border px-3 py-1 text-[10px] tracking-[0.18em] uppercase",
                  toneClasses(selectedEvent.tone)
                )}
              >
                {selectedEvent.from}
              </Badge>
            </div>

            <Separator className="bg-border/60" />

            <div className="space-y-2">
              <div className="text-[11px] tracking-[0.24em] text-muted-foreground uppercase">
                Key Facts
              </div>
              <div className="grid gap-2">
                {selectedEvent.key_facts.map((fact) => (
                  <div
                    key={fact}
                    className="border border-border/60 bg-background/40 px-3 py-2 text-sm leading-6 text-muted-foreground"
                  >
                    {fact}
                  </div>
                ))}
              </div>
            </div>

            <div className="space-y-2">
              <div className="text-[11px] tracking-[0.24em] text-muted-foreground uppercase">
                Raw Payload
              </div>
              <pre className="overflow-x-auto border border-border/60 bg-background/80 p-4 text-[11px] leading-5 text-foreground/85">
                {selectedEvent.raw_json}
              </pre>
            </div>
          </div>
        ) : (
          <EmptyPanel copy="Select a timeline event to inspect it." />
        )}
      </CardContent>
    </Card>
  )
}

function ChatBubble({ message }: { message: ChatMessage }) {
  return (
    <article
      className={cn(
        "border px-4 py-4",
        messageTone(message.role),
        message.role === "user" ? "ml-8" : "mr-8"
      )}
    >
      <div className="mb-3 flex items-center justify-between gap-3 text-[11px] tracking-[0.2em] text-muted-foreground uppercase">
        <span className="inline-flex items-center gap-2">
          {message.role === "user" ? (
            <User className="size-4" />
          ) : (
            <Robot className="size-4" />
          )}
          {messageRoleLabel(message.role)}
        </span>
        <span>{formatRelativeTime(message.timestamp)}</span>
      </div>

      <div className="chat-markdown text-sm leading-7 text-foreground/90">
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          components={{
            a: ({ className, ...props }) => (
              <a
                {...props}
                className={cn(
                  "text-primary underline underline-offset-4",
                  className
                )}
                target="_blank"
                rel="noreferrer"
              />
            ),
          }}
        >
          {message.content}
        </ReactMarkdown>
      </div>
    </article>
  )
}

export default App
