(function () {
  var app = document.getElementById("work-app");
  if (!app) {
    return;
  }

  var PANEL_ROOT = (document.body && document.body.getAttribute("data-panel-root")) || "/admin";
  var state = {
    sessionID: String(app.getAttribute("data-initial-session") || "").trim(),
    filter: String(app.getAttribute("data-initial-filter") || "all").trim(),
    view: String(app.getAttribute("data-initial-view") || "narrative").trim(),
    noise: String(app.getAttribute("data-initial-noise") || "grouped").trim(),
    selectedEventSeq: String(app.getAttribute("data-initial-event") || "").trim(),
    statusFilter: "all",
    sessionQuery: "",
    sessions: [],
    detail: null,
    inspectorEvent: null,
    inspectorOutOfWindow: false,
    inspectorTab: "summary",
    source: null,
    firstSeq: 0,
    lastSeq: 0,
    hasOlder: false
  };

  function escapeHTML(value) {
    return String(value || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function normalize(value) {
    return String(value || "").trim().toLowerCase();
  }

  function clip(value, max) {
    var text = String(value || "").trim();
    if (!max || text.length <= max) return text;
    if (max <= 1) return "…";
    return text.slice(0, max - 1) + "…";
  }

  function firstText() {
    for (var i = 0; i < arguments.length; i++) {
      var value = String(arguments[i] || "").trim();
      if (value) return value;
    }
    return "";
  }

  function humanize(value) {
    return String(value || "")
      .replace(/[_\.]+/g, " ")
      .replace(/\s+/g, " ")
      .trim()
      .replace(/\b\w/g, function (match) {
        return match.toUpperCase();
      });
  }

  function relativeTime(value) {
    if (window.GopherPanelTime && typeof window.GopherPanelTime.formatRelative === "function") {
      return window.GopherPanelTime.formatRelative(value);
    }
    return String(value || "");
  }

  function applyRelativeTimes(root) {
    if (window.GopherPanelTime && typeof window.GopherPanelTime.applyRelativeTimes === "function") {
      window.GopherPanelTime.applyRelativeTimes(root || document);
    }
  }

  function api(path) {
    return PANEL_ROOT + path;
  }

  function fetchJSON(path) {
    return fetch(path, { credentials: "same-origin" }).then(function (response) {
      if (!response.ok) {
        return response.text().then(function (message) {
          throw new Error(message || ("Request failed with status " + response.status));
        });
      }
      return response.json();
    });
  }

  function selectedTimelineEvent() {
    return state.inspectorEvent || null;
  }

  function syncURL(replace) {
    if (!window.history || typeof window.history.pushState !== "function") return;
    var path = PANEL_ROOT + "/work";
    if (state.sessionID) {
      path += "/" + encodeURIComponent(state.sessionID);
    }
    var params = new URLSearchParams();
    if (state.filter !== "all") params.set("filter", state.filter);
    if (state.view !== "narrative") params.set("view", state.view);
    if (state.noise !== "grouped") params.set("noise", state.noise);
    if (state.selectedEventSeq) params.set("event", state.selectedEventSeq);
    var next = path + (params.toString() ? "?" + params.toString() : "");
    var current = window.location.pathname + window.location.search;
    if (current === next) return;
    window.history[replace ? "replaceState" : "pushState"]({}, "", next);
  }

  function closeSource() {
    if (state.source) {
      state.source.close();
      state.source = null;
    }
  }

  function openSource() {
    closeSource();
    if (!state.sessionID) return;
    state.source = new EventSource(api("/api/work/session/" + encodeURIComponent(state.sessionID) + "/stream?after_seq=" + state.lastSeq));
    state.source.addEventListener("session-event", function () {
      refreshSessions();
      loadNewer();
    });
  }

  function refreshSessions() {
    return fetchJSON(api("/api/work/sessions")).then(function (payload) {
      state.sessions = Array.isArray(payload.sessions) ? payload.sessions : [];
      renderSessions();
      if (!state.sessionID && state.sessions.length) {
        state.sessionID = state.sessions[0].session_id;
        syncURL(true);
        loadSession(state.sessionID, true);
      }
    }).catch(function (error) {
      document.getElementById("work-session-list").innerHTML = '<p class="empty-state">' + escapeHTML(error.message) + "</p>";
    });
  }

  function filteredSessions() {
    return state.sessions.filter(function (session) {
      var query = normalize(state.sessionQuery);
      var haystack = normalize(
        session.title + " " + (session.conversation_id || "") + " " + session.status + " " + session.latest_digest
      );
      var queryMatch = !query || haystack.indexOf(query) >= 0;
      var statusMatch = state.statusFilter === "all" || normalize(session.status) === state.statusFilter;
      return queryMatch && statusMatch;
    });
  }

  function renderSessions() {
    var container = document.getElementById("work-session-list");
    var sessions = filteredSessions();
    if (!sessions.length) {
      container.innerHTML = '<p class="empty-state">No sessions match the current filters.</p>';
      return;
    }
    var priority = sessions.filter(function (item) {
      return item.status === "failed" || item.waiting_on_human || item.has_anomaly;
    });
    var recent = sessions.filter(function (item) {
      return !(item.status === "failed" || item.waiting_on_human || item.has_anomaly);
    });
    var html = "";
    if (priority.length) {
      html += renderSessionGroup("Needs Attention", priority);
    }
    if (recent.length) {
      html += renderSessionGroup(priority.length ? "Recent Work" : "Sessions", recent);
    }
    container.innerHTML = html;
    applyRelativeTimes(container);
  }

  function renderSessionGroup(label, sessions) {
    var rows = sessions.map(function (session) {
      var active = session.session_id === state.sessionID ? " is-active" : "";
      var tone = session.status === "failed" ? " tone-danger" : (session.working ? " tone-active" : "");
      return '' +
        '<button type="button" class="queue-item' + active + tone + '" data-session-id="' + escapeHTML(session.session_id) + '">' +
          '<div class="queue-item-head">' +
            '<strong>' + escapeHTML(session.title) + '</strong>' +
            '<span class="pill tone-' + escapeHTML(session.status === "failed" ? "danger" : (session.waiting_on_human ? "warn" : "neutral")) + '">' + escapeHTML(session.priority_label) + '</span>' +
          '</div>' +
          '<div class="queue-item-copy">' + escapeHTML(session.latest_digest || "No recent digest.") + '</div>' +
          '<div class="queue-meta">' +
            '<span>' + escapeHTML(session.status) + '</span>' +
            (session.conversation_id ? '<span>' + escapeHTML(session.conversation_id) + '</span>' : '') +
            '<time datetime="' + escapeHTML(session.updated_at) + '" data-relative-time data-absolute-time="' + escapeHTML(session.updated_at) + '">' + escapeHTML(session.updated_at) + '</time>' +
          '</div>' +
        '</button>';
    }).join("");
    return '<section class="queue-group"><p class="queue-group-label">' + escapeHTML(label) + '</p>' + rows + '</section>';
  }

  function loadSession(sessionID, replace) {
    if (!sessionID) return;
    state.sessionID = sessionID;
    state.inspectorEvent = null;
    state.inspectorOutOfWindow = false;
    state.inspectorTab = state.view === "raw" ? "raw" : "summary";
    syncURL(!!replace);
    fetchJSON(api("/api/work/session/" + encodeURIComponent(sessionID))).then(function (detail) {
      state.detail = detail;
      state.firstSeq = detail.timeline ? (detail.timeline.first_seq || 0) : 0;
      state.lastSeq = detail.timeline ? (detail.timeline.last_seq || 0) : 0;
      state.hasOlder = !!(detail.timeline && detail.timeline.has_older);
      renderDetail();
      if (state.selectedEventSeq) {
        selectEventFromState();
      }
      renderSessions();
      openSource();
      app.classList.remove("queue-open");
    }).catch(function (error) {
      document.getElementById("work-timeline").innerHTML = '<p class="empty-state">' + escapeHTML(error.message) + '</p>';
    });
  }

  function selectEventFromState() {
    if (!state.selectedEventSeq || !state.sessionID) {
      renderInspector();
      return;
    }
    var seq = Number(state.selectedEventSeq) || 0;
    var events = (state.detail && state.detail.timeline && state.detail.timeline.events) || [];
    for (var i = 0; i < events.length; i++) {
      if ((events[i].seq || 0) === seq) {
        state.inspectorEvent = events[i];
        state.inspectorOutOfWindow = false;
        renderTimeline();
        renderInspector();
        return;
      }
    }
    fetchJSON(api("/api/work/session/" + encodeURIComponent(state.sessionID) + "/events/" + encodeURIComponent(seq))).then(function (payload) {
      state.inspectorEvent = payload.event || null;
      state.inspectorOutOfWindow = !!payload.out_of_window;
      renderTimeline();
      renderInspector();
    }).catch(function () {
      state.inspectorEvent = null;
      state.inspectorOutOfWindow = false;
      renderInspector();
    });
  }

  function countsMarkup(counts) {
    var keys = ["user", "agent", "tools", "control", "errors", "other"];
    return keys.map(function (key) {
      var value = counts && counts[key] ? counts[key] : 0;
      return '<div class="session-summary-card"><span>' + escapeHTML(humanize(key)) + '</span><strong>' + escapeHTML(String(value)) + '</strong></div>';
    }).join("");
  }

  function toneClass(tone) {
    var value = normalize(tone);
    if (!value) return "";
    if (value === "success") value = "active";
    if (value === "failure") value = "danger";
    if (value === "neutral") value = "muted";
    if (value === "tools") value = "active";
    return " tone-" + value;
  }

  function cardToneClass(event) {
    var value = normalize(event.tone);
    if (event.anomaly || event.result_status === "failure") value = "danger";
    else if (event.result_status === "success") value = "active";
    else if (event.result_status === "waiting") value = "warn";
    else if (!value) value = "muted";
    return " toned-" + value;
  }

  function badgeForEvent(event) {
    if (event.result_status === "success") {
      return '<span class="pill tone-active">Success</span>';
    }
    if (event.result_status === "failure") {
      return '<span class="pill tone-danger">Failed</span>';
    }
    if (event.result_status === "waiting") {
      return '<span class="pill tone-warn">Running</span>';
    }
    if (event.anomaly) {
      return '<span class="pill tone-danger">Review</span>';
    }
    if (event.is_meaningful) {
      return '<span class="pill tone-neutral">Key step</span>';
    }
    return '<span class="pill tone-muted">Background</span>';
  }

  function renderStoryCard(label, value, detail, tone) {
    return '' +
      '<article class="story-card' + toneClass(tone) + '">' +
        '<span>' + escapeHTML(label) + '</span>' +
        '<strong>' + escapeHTML(value || "Unavailable") + '</strong>' +
        (detail ? '<p class="story-copy">' + escapeHTML(detail) + '</p>' : "") +
      '</article>';
  }

  function renderDetail() {
    renderSummary();
    renderTimeline();
    renderInspector();
  }

  function renderSummary() {
    var heading = document.getElementById("work-session-heading");
    var subtitle = document.getElementById("work-session-subtitle");
    var summary = document.getElementById("work-session-summary");
    if (!state.detail) {
      heading.textContent = "Select a session";
      subtitle.textContent = "Use the queue to open a session and inspect the timeline.";
      summary.innerHTML = '<p class="empty-state">Session story will appear here.</p>';
      return;
    }
    var session = state.detail.session;
    var story = state.detail.story || {};
    heading.textContent = session.title;
    subtitle.textContent = session.conversation_id ? session.conversation_id : "Session " + session.session_id;
    summary.innerHTML = '' +
      '<div class="story-grid">' +
        renderStoryCard("Current State", story.current_state || session.priority_label, story.current_state_detail, session.status === "failed" ? "danger" : (session.waiting_on_human ? "warn" : "active")) +
        renderStoryCard("Latest Goal", story.goal || "No recent user ask in this window.", "", "user") +
        renderStoryCard("Latest Conclusion", story.latest_conclusion || "No recent agent conclusion in this window.", "", "agent") +
        renderStoryCard("Last Meaningful Step", story.last_meaningful_step || "No meaningful work step in this window.", "", "active") +
        renderStoryCard("Latest Anomaly", story.latest_anomaly || "None in current window.", "", story.latest_anomaly ? "danger" : "muted") +
      '</div>' +
      '<div class="session-summary-grid">' +
        '<div class="session-summary-card"><span>Status</span><strong>' + escapeHTML(session.priority_label) + '</strong></div>' +
        '<div class="session-summary-card"><span>Updated</span><strong><time datetime="' + escapeHTML(session.updated_at) + '" data-relative-time data-absolute-time="' + escapeHTML(session.updated_at) + '">' + escapeHTML(session.updated_at) + '</time></strong></div>' +
        '<div class="session-summary-card"><span>Latest Anomaly</span><strong>' + escapeHTML(state.detail.latest_anomaly || "None in current window.") + '</strong></div>' +
        '<div class="session-summary-card"><span>Last Seq</span><strong>' + escapeHTML(String(session.last_seq || 0)) + '</strong></div>' +
      '</div>' +
      '<div class="session-summary-grid counts-grid">' + countsMarkup(state.detail.counts || {}) + '</div>';
    applyRelativeTimes(summary);
    document.getElementById("work-load-older").hidden = !state.hasOlder;
    document.getElementById("work-timeline-meta").textContent = timelineMetaText();
  }

  function timelineMetaText() {
    var events = ((state.detail && state.detail.timeline && state.detail.timeline.events) || []).length;
    var label = state.view === "raw" ? "Raw JSON" : (state.view === "stream" ? "Event Stream" : "Narrative");
    return "Loaded " + events + " events · " + label;
  }

  function visibleEvents() {
    var events = (state.detail && state.detail.timeline && state.detail.timeline.events) || [];
    if (state.filter === "all") return events;
    return events.filter(function (event) {
      return normalize(event.category) === state.filter;
    });
  }

  function timelineItems(events) {
    if (state.view !== "narrative" || state.noise !== "grouped") {
      return events.map(function (event) { return { kind: "event", event: event }; });
    }
    var items = [];
    var current = null;

    function flush() {
      if (!current) return;
      if (current.events.length === 1) {
        items.push({ kind: "event", event: current.events[0] });
      } else {
        items.push({ kind: "bundle", bundle: current });
      }
      current = null;
    }

    for (var i = 0; i < events.length; i++) {
      var event = events[i];
      if (!event.bundle_id) {
        flush();
        items.push({ kind: "event", event: event });
        continue;
      }
      if (current && current.id === event.bundle_id) {
        current.events.push(event);
        continue;
      }
      flush();
      current = {
        id: event.bundle_id,
        kind: event.bundle_kind || "bundle",
        title: event.bundle_title || "Grouped activity",
        events: [event]
      };
    }
    flush();
    return items;
  }

  function renderTimeline() {
    var container = document.getElementById("work-timeline");
    if (!state.detail) {
      container.innerHTML = '<p class="empty-state">No session selected.</p>';
      return;
    }
    var items = timelineItems(visibleEvents());
    if (!items.length) {
      container.innerHTML = '<p class="empty-state">No events match the current filter.</p>';
      return;
    }
    container.innerHTML = items.map(function (item) {
      if (item.kind === "bundle") {
        return renderBundle(item.bundle);
      }
      return renderEventCard(item.event, false);
    }).join("");
    applyRelativeTimes(container);
    document.getElementById("work-timeline-meta").textContent = timelineMetaText();
  }

  function renderBundle(bundle) {
    var latest = bundle.events[bundle.events.length - 1];
    var preview = firstText(latest.subtitle, latest.digest, latest.title);
    var open = bundle.events.some(function (event) {
      return String(event.seq) === String(state.selectedEventSeq);
    }) ? " open" : "";
    var rows = bundle.events.map(function (event) {
      return '<div class="bundle-row">' + renderEventCard(event, true) + '</div>';
    }).join("");
    return '' +
      '<details class="event-bundle bundle-' + escapeHTML(normalize(bundle.kind || "bundle")) + '"' + open + '>' +
        '<summary>' +
          '<div class="bundle-head">' +
            '<div>' +
              '<p class="bundle-label">' + escapeHTML(bundle.title) + '</p>' +
              '<strong class="bundle-summary">' + escapeHTML(firstText(latest.title, latest.type_label, latest.type)) + '</strong>' +
            '</div>' +
            '<span class="bundle-meta">' + escapeHTML(String(bundle.events.length)) + ' steps · <time datetime="' + escapeHTML(latest.timestamp) + '" data-relative-time data-absolute-time="' + escapeHTML(latest.timestamp) + '">' + escapeHTML(latest.timestamp) + '</time></span>' +
          '</div>' +
          '<div class="bundle-item-copy">' + escapeHTML(preview || "Expand to inspect this bundle.") + '</div>' +
        '</summary>' +
        '<div class="bundle-items">' + rows + '</div>' +
      '</details>';
  }

  function renderEventCard(event, nested) {
    var selected = String(event.seq) === String(state.selectedEventSeq) ? " is-selected" : "";
    var body = state.view === "raw"
      ? clip(event.raw_json, 260)
      : firstText(event.subtitle, event.digest, event.title);
    var kicker = state.view === "stream"
      ? humanize(event.type_label || event.type)
      : humanize(event.category || event.type);
    return '' +
      '<button type="button" class="event-card' + selected + cardToneClass(event) + (nested ? " is-nested" : "") + '" data-event-seq="' + escapeHTML(String(event.seq)) + '">' +
        '<div class="event-card-head">' +
          '<div class="event-title-block">' +
            '<span class="event-emoji" aria-hidden="true">' + escapeHTML(event.emoji || "📌") + '</span>' +
            '<div class="event-title-copy">' +
              '<p class="event-kicker">' + escapeHTML(kicker) + '</p>' +
              '<strong class="event-title">' + escapeHTML(firstText(event.title, event.type_label, event.type)) + '</strong>' +
            '</div>' +
          '</div>' +
          badgeForEvent(event) +
        '</div>' +
        '<div class="event-card-copy">' + escapeHTML(body || "No narrative summary available.") + '</div>' +
        '<div class="event-meta">' +
          '<span>' + escapeHTML(event.from || "system") + '</span>' +
          '<span>#' + escapeHTML(String(event.seq)) + '</span>' +
          '<span>' + escapeHTML(event.type) + '</span>' +
          '<time datetime="' + escapeHTML(event.timestamp) + '" data-relative-time data-absolute-time="' + escapeHTML(event.timestamp) + '">' + escapeHTML(event.timestamp) + '</time>' +
        '</div>' +
      '</button>';
  }

  function renderInspector() {
    var container = document.getElementById("work-inspector");
    if (!state.detail) {
      container.innerHTML = '<p class="empty-state">Choose an event to inspect its details.</p>';
      return;
    }
    var event = selectedTimelineEvent();
    if (!event) {
      var story = state.detail.story || {};
      container.innerHTML = '' +
        '<section class="inspector-section">' +
          '<p class="eyebrow">Session Story</p>' +
          '<h3>' + escapeHTML(state.detail.session.title) + '</h3>' +
          '<p class="inspector-copy">' + escapeHTML(firstText(story.current_state_detail, story.last_meaningful_step, state.detail.latest_anomaly, "Open a timeline step to inspect its details.")) + '</p>' +
          '<div class="inspector-chip-row">' +
            '<span class="pill tone-neutral">' + escapeHTML(story.current_state || state.detail.session.priority_label) + '</span>' +
            (story.latest_anomaly ? '<span class="pill tone-danger">Anomaly</span>' : '<span class="pill tone-active">Readable</span>') +
          '</div>' +
        '</section>' +
        renderStoryInspector(story) +
        renderHealthSection(state.detail.context_health);
      return;
    }
    container.innerHTML = '' +
      '<section class="inspector-section">' +
        '<p class="eyebrow">Event Inspector</p>' +
        '<div class="inspector-title-row">' +
          '<span class="inspector-emoji" aria-hidden="true">' + escapeHTML(event.emoji || "📌") + '</span>' +
          '<div>' +
            '<h3>' + escapeHTML(firstText(event.title, event.type_label, event.type)) + '</h3>' +
            '<p class="inspector-copy">' + escapeHTML(firstText(event.subtitle, event.digest, "No readable summary available.")) + '</p>' +
          '</div>' +
        '</div>' +
        (state.inspectorOutOfWindow ? '<p class="empty-state">This event was restored from the URL but is outside the currently loaded event window.</p>' : '') +
        '<div class="inspector-chip-row">' +
          badgeForEvent(event) +
          '<span class="pill tone-neutral">#' + escapeHTML(String(event.seq)) + '</span>' +
          (event.bundle_title ? '<span class="pill tone-muted">' + escapeHTML(event.bundle_title) + '</span>' : '') +
        '</div>' +
      '</section>' +
      '<div class="inspector-tabs">' +
        '<button type="button" class="inspector-tab' + (state.inspectorTab === "summary" ? " is-active" : "") + '" data-inspector-tab="summary">Summary</button>' +
        '<button type="button" class="inspector-tab' + (state.inspectorTab === "raw" ? " is-active" : "") + '" data-inspector-tab="raw">Raw JSON</button>' +
      '</div>' +
      (state.inspectorTab === "raw" ? renderRawSection(event) : renderSummarySection(event));
  }

  function renderStoryInspector(story) {
    return '' +
      '<section class="inspector-section">' +
        '<div class="kv-grid">' +
          '<div><dt>Latest Goal</dt><dd>' + escapeHTML(story.goal || "-") + '</dd></div>' +
          '<div><dt>Latest Conclusion</dt><dd>' + escapeHTML(story.latest_conclusion || "-") + '</dd></div>' +
          '<div><dt>Last Meaningful Step</dt><dd>' + escapeHTML(story.last_meaningful_step || "-") + '</dd></div>' +
          '<div><dt>Latest Anomaly</dt><dd>' + escapeHTML(story.latest_anomaly || "-") + '</dd></div>' +
        '</div>' +
      '</section>';
  }

  function renderSummarySection(event) {
    var facts = Array.isArray(event.key_facts) ? event.key_facts : [];
    var items = facts.length ? facts.map(function (fact) {
      return '<li>' + escapeHTML(fact) + '</li>';
    }).join("") : '<li>No structured key facts available.</li>';
    return '' +
      '<section class="inspector-section">' +
        '<div class="kv-grid">' +
          '<div><dt>From</dt><dd>' + escapeHTML(event.from || "system") + '</dd></div>' +
          '<div><dt>Category</dt><dd>' + escapeHTML(humanize(event.category || event.type)) + '</dd></div>' +
          '<div><dt>Type</dt><dd>' + escapeHTML(event.type) + '</dd></div>' +
          '<div><dt>When</dt><dd><time datetime="' + escapeHTML(event.timestamp) + '" data-relative-time data-absolute-time="' + escapeHTML(event.timestamp) + '">' + escapeHTML(relativeTime(event.timestamp)) + '</time></dd></div>' +
        '</div>' +
      '</section>' +
      '<section class="inspector-section"><ul class="activity-list">' + items + "</ul></section>";
  }

  function renderRawSection(event) {
    return '<section class="inspector-section"><pre class="event-pre">' + escapeHTML(event.raw_json || "{}") + "</pre></section>";
  }

  function renderHealthSection(health) {
    if (!health) {
      return '<section class="inspector-section"><p class="empty-state">No context-health data has been published for this session.</p></section>';
    }
    return '' +
      '<section class="inspector-section">' +
        '<p class="eyebrow">Context Health</p>' +
        '<div class="kv-grid">' +
          '<div><dt>Model</dt><dd>' + escapeHTML(health.model_display || "-") + '</dd></div>' +
          '<div><dt>Window</dt><dd>' + escapeHTML(String(health.model_context_window || 0)) + '</dd></div>' +
          '<div><dt>Reserve</dt><dd>' + escapeHTML(String(health.reserve_tokens || 0)) + '</dd></div>' +
          '<div><dt>Estimated</dt><dd>' + escapeHTML(String(health.estimated_input_tokens || 0)) + '</dd></div>' +
          '<div><dt>Retries</dt><dd>' + escapeHTML(String(health.overflow_retries || 0)) + '</dd></div>' +
          '<div><dt>Stage</dt><dd>' + escapeHTML(health.overflow_stage || "-") + '</dd></div>' +
          '<div><dt>Recent</dt><dd>' + escapeHTML(health.recent_messages || "-") + '</dd></div>' +
          '<div><dt>Memory</dt><dd>' + escapeHTML(health.memory || "-") + '</dd></div>' +
        '</div>' +
      '</section>';
  }

  function prependEvents(events) {
    var existing = ((state.detail && state.detail.timeline && state.detail.timeline.events) || []).slice();
    var seen = {};
    for (var i = 0; i < existing.length; i++) seen[String(existing[i].seq)] = true;
    var next = [];
    for (var j = 0; j < events.length; j++) {
      if (!seen[String(events[j].seq)]) next.push(events[j]);
    }
    state.detail.timeline.events = next.concat(existing);
  }

  function appendEvents(events) {
    var existing = ((state.detail && state.detail.timeline && state.detail.timeline.events) || []).slice();
    var seen = {};
    for (var i = 0; i < existing.length; i++) seen[String(existing[i].seq)] = true;
    for (var j = 0; j < events.length; j++) {
      if (!seen[String(events[j].seq)]) existing.push(events[j]);
    }
    state.detail.timeline.events = existing;
  }

  function loadOlder() {
    if (!state.sessionID || !state.hasOlder || !state.firstSeq) return;
    fetchJSON(api("/api/work/session/" + encodeURIComponent(state.sessionID) + "/events?before_seq=" + encodeURIComponent(state.firstSeq) + "&limit=50")).then(function (payload) {
      prependEvents(payload.events || []);
      state.firstSeq = payload.first_seq || state.firstSeq;
      state.hasOlder = !!payload.has_older;
      renderTimeline();
      document.getElementById("work-load-older").hidden = !state.hasOlder;
    });
  }

  function loadNewer() {
    if (!state.sessionID) return;
    fetchJSON(api("/api/work/session/" + encodeURIComponent(state.sessionID) + "/events?after_seq=" + encodeURIComponent(state.lastSeq || 0) + "&limit=200")).then(function (payload) {
      var events = payload.events || [];
      if (!events.length) return;
      appendEvents(events);
      state.lastSeq = payload.last_seq || state.lastSeq;
      renderTimeline();
      if (!state.selectedEventSeq) renderInspector();
    });
  }

  function selectEvent(seq) {
    state.selectedEventSeq = String(seq || "");
    state.inspectorTab = state.view === "raw" ? "raw" : "summary";
    syncURL(false);
    selectEventFromState();
  }

  function syncButtons(selector, attribute, activeValue) {
    var nodes = document.querySelectorAll(selector);
    for (var i = 0; i < nodes.length; i++) {
      var node = nodes[i];
      var match = (node.getAttribute(attribute) || "") === activeValue;
      node.classList.toggle("is-active", match);
      node.setAttribute("aria-pressed", match ? "true" : "false");
    }
  }

  function attachEvents() {
    app.addEventListener("click", function (event) {
      var sessionButton = event.target.closest("[data-session-id]");
      if (sessionButton && sessionButton.classList.contains("queue-item")) {
        loadSession(sessionButton.getAttribute("data-session-id") || "", false);
        return;
      }
      var eventButton = event.target.closest("[data-event-seq]");
      if (eventButton && eventButton.classList.contains("event-card")) {
        selectEvent(eventButton.getAttribute("data-event-seq") || "");
        return;
      }
      var tabButton = event.target.closest("[data-inspector-tab]");
      if (tabButton) {
        state.inspectorTab = tabButton.getAttribute("data-inspector-tab") || "summary";
        renderInspector();
        return;
      }
      var filterButton = event.target.closest("[data-work-filter]");
      if (filterButton) {
        state.filter = filterButton.getAttribute("data-work-filter") || "all";
        syncButtons("[data-work-filter]", "data-work-filter", state.filter);
        syncURL(false);
        renderTimeline();
        return;
      }
      var viewButton = event.target.closest("[data-work-view]");
      if (viewButton) {
        state.view = viewButton.getAttribute("data-work-view") || "narrative";
        state.inspectorTab = state.view === "raw" ? "raw" : "summary";
        syncButtons("[data-work-view]", "data-work-view", state.view);
        syncURL(false);
        renderTimeline();
        renderInspector();
        return;
      }
      var noiseButton = event.target.closest("[data-work-noise]");
      if (noiseButton) {
        state.noise = noiseButton.getAttribute("data-work-noise") || "grouped";
        syncButtons("[data-work-noise]", "data-work-noise", state.noise);
        syncURL(false);
        renderTimeline();
        return;
      }
      var statusButton = event.target.closest("[data-status-filter]");
      if (statusButton) {
        state.statusFilter = statusButton.getAttribute("data-status-filter") || "all";
        syncButtons("[data-status-filter]", "data-status-filter", state.statusFilter);
        renderSessions();
        return;
      }
      var filterToggle = event.target.closest("[data-work-filter-toggle]");
      if (filterToggle) {
        app.classList.toggle("filters-open");
        return;
      }
      var queueToggle = event.target.closest("[data-work-queue-toggle]");
      if (queueToggle) {
        app.classList.toggle("queue-open");
      }
    });

    var search = document.getElementById("work-search");
    if (search) {
      search.addEventListener("input", function () {
        state.sessionQuery = search.value || "";
        renderSessions();
      });
    }

    var older = document.getElementById("work-load-older");
    if (older) {
      older.addEventListener("click", loadOlder);
    }

    window.addEventListener("popstate", function () {
      var params = new URLSearchParams(window.location.search || "");
      state.filter = params.get("filter") || "all";
      state.view = params.get("view") || "narrative";
      state.noise = params.get("noise") || "grouped";
      state.selectedEventSeq = params.get("event") || "";
      syncButtons("[data-work-filter]", "data-work-filter", state.filter);
      syncButtons("[data-work-view]", "data-work-view", state.view);
      syncButtons("[data-work-noise]", "data-work-noise", state.noise);
      selectEventFromState();
      renderTimeline();
      renderInspector();
    });
  }

  function init() {
    syncButtons("[data-work-filter]", "data-work-filter", state.filter);
    syncButtons("[data-work-view]", "data-work-view", state.view);
    syncButtons("[data-work-noise]", "data-work-noise", state.noise);
    syncButtons("[data-status-filter]", "data-status-filter", state.statusFilter);
    attachEvents();
    refreshSessions().then(function () {
      if (state.sessionID) {
        loadSession(state.sessionID, true);
      }
    });
  }

  init();
}());
