(function () {
  var body = document.body;
  var app = document.querySelector(".chat-app");
  if (!body || !app || app.getAttribute("data-chat-enabled") !== "true") {
    return;
  }

  var root = body.getAttribute("data-chat-root") || "/chat";
  var adminRoot = body.getAttribute("data-admin-root") || "/admin";
  var initialSession = (body.getAttribute("data-initial-session") || "").trim();

  var state = {
    sessions: [],
    sessionID: initialSession,
    detail: null,
    source: null,
    refreshTimer: 0,
    reconnectTimer: 0,
    sending: false,
    query: ""
  };

  var sessionList = document.getElementById("chat-session-list");
  var sessionTitle = document.getElementById("chat-session-title");
  var sessionMeta = document.getElementById("chat-session-meta");
  var transcript = document.getElementById("chat-transcript");
  var compose = document.getElementById("chat-compose");
  var textarea = document.getElementById("chat-message");
  var statusNote = document.getElementById("chat-status-note");
  var inspectLink = document.getElementById("chat-inspect-link");
  var newSessionBtn = document.getElementById("chat-new-session");
  var searchInput = document.getElementById("chat-search");

  function api(path) {
    return root + path;
  }

  function escapeHTML(value) {
    return String(value || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/\"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function fetchJSON(path, init) {
    return fetch(path, init).then(function (response) {
      if (!response.ok) {
        return response.text().then(function (text) {
          throw new Error(text || ("Request failed: " + response.status));
        });
      }
      return response.json();
    });
  }

  function setStatus(text) {
    statusNote.textContent = text;
  }

  function updateInspectLink() {
    var href = adminRoot + "/work";
    if (state.sessionID) {
      href += "/" + encodeURIComponent(state.sessionID);
    }
    inspectLink.setAttribute("href", href);
  }

  function relativeTime(value) {
    if (window.GopherChatTime && typeof window.GopherChatTime.formatRelative === "function") {
      return window.GopherChatTime.formatRelative(value);
    }
    return value || "-";
  }

  function renderSessions() {
    var items = state.sessions.filter(function (item) {
      if (!state.query) return true;
      var haystack = (item.title + " " + (item.preview || "") + " " + item.status).toLowerCase();
      return haystack.indexOf(state.query) >= 0;
    });
    if (!items.length) {
      sessionList.innerHTML = '<p class="empty-state">No local chats match this view.</p>';
      return;
    }
    sessionList.innerHTML = items.map(function (item) {
      var active = item.session_id === state.sessionID ? " is-active" : "";
      var preview = item.preview ? '<span>' + escapeHTML(item.preview) + '</span>' : '<span>No messages yet.</span>';
      var working = item.working ? '<small class="chat-session-working">Working</small>' : '<small>' + escapeHTML(item.status) + '</small>';
      return '' +
        '<button type="button" class="chat-session-item' + active + '" data-session-id="' + escapeHTML(item.session_id) + '">' +
        '<strong>' + escapeHTML(item.title) + '</strong>' +
        preview +
        '<div class="chat-session-meta-row">' +
        working +
        '<time datetime="' + escapeHTML(item.updated_at) + '">' + escapeHTML(relativeTime(item.updated_at)) + '</time>' +
        '</div>' +
        '</button>';
    }).join("");
  }

  function renderTranscript() {
    updateInspectLink();
    if (!state.detail) {
      sessionTitle.textContent = "Select a local chat";
      sessionMeta.textContent = "Choose a thread or start a new one.";
      transcript.innerHTML = '<p class="empty-state">Messages will appear here.</p>';
      setStatus("Idle.");
      return;
    }

    var detail = state.detail;
    sessionTitle.textContent = detail.session.title || detail.session.session_id;
    sessionMeta.textContent = detail.session.working
      ? "Agent is responding locally."
      : detail.session.status + " • updated " + relativeTime(detail.session.updated_at);
    setStatus(detail.session.working ? "Working locally…" : "Ready.");

    if (!detail.messages || !detail.messages.length) {
      transcript.innerHTML = '<p class="empty-state">This thread is empty. Send the first message when ready.</p>';
      return;
    }

    transcript.innerHTML = detail.messages.map(function (message) {
      var role = String(message.role || "system");
      var tone = "system";
      if (role === "user") tone = "user";
      if (role === "agent") tone = "agent";
      if (role === "error") tone = "error";
      return '' +
        '<article class="chat-bubble tone-' + tone + '">' +
        '<header class="chat-bubble-meta">' +
        '<span>' + escapeHTML(role.toUpperCase()) + '</span>' +
        '<time datetime="' + escapeHTML(message.timestamp) + '">' + escapeHTML(relativeTime(message.timestamp)) + '</time>' +
        '</header>' +
        '<div class="chat-bubble-body">' + escapeHTML(message.content) + '</div>' +
        '</article>';
    }).join("");
    transcript.scrollTop = transcript.scrollHeight;
  }

  function clearStream() {
    if (state.source) {
      state.source.close();
      state.source = null;
    }
    if (state.reconnectTimer) {
      window.clearTimeout(state.reconnectTimer);
      state.reconnectTimer = 0;
    }
  }

  function scheduleRefresh() {
    if (state.refreshTimer || !state.sessionID) {
      return;
    }
    state.refreshTimer = window.setTimeout(function () {
      state.refreshTimer = 0;
      Promise.all([loadCurrentSession(true), loadSessions(true)]).catch(function (error) {
        setStatus(error.message || "Refresh failed.");
      });
    }, 180);
  }

  function startStream() {
    clearStream();
    if (!state.sessionID) {
      return;
    }
    var lastSeq = state.detail && state.detail.session ? state.detail.session.last_seq || 0 : 0;
    state.source = new EventSource(api("/api/session/" + encodeURIComponent(state.sessionID) + "/stream?after_seq=" + encodeURIComponent(lastSeq)));
    state.source.addEventListener("session-event", function () {
      scheduleRefresh();
    });
    state.source.onerror = function () {
      clearStream();
      state.reconnectTimer = window.setTimeout(startStream, 1500);
    };
  }

  function selectSession(sessionID) {
    sessionID = String(sessionID || "").trim();
    if (!sessionID) {
      return;
    }
    state.sessionID = sessionID;
    renderSessions();
    loadCurrentSession(false).then(function () {
      startStream();
    }).catch(function (error) {
      state.detail = null;
      renderTranscript();
      setStatus(error.message || "Failed to load chat.");
    });
  }

  function loadSessions(preserveSelection) {
    return fetchJSON(api("/api/sessions")).then(function (payload) {
      state.sessions = payload.sessions || [];
      renderSessions();
      if (state.sessionID) {
        var exists = state.sessions.some(function (item) { return item.session_id === state.sessionID; });
        if (!exists) {
          state.sessionID = "";
          state.detail = null;
          clearStream();
          renderTranscript();
        }
      }
      if (!preserveSelection && !state.sessionID && state.sessions.length) {
        selectSession(state.sessions[0].session_id);
      }
    });
  }

  function loadCurrentSession(silent) {
    if (!state.sessionID) {
      state.detail = null;
      renderTranscript();
      return Promise.resolve();
    }
    return fetchJSON(api("/api/session/" + encodeURIComponent(state.sessionID))).then(function (payload) {
      state.detail = payload;
      renderTranscript();
    }).catch(function (error) {
      if (!silent) {
        throw error;
      }
    });
  }

  function createSession(initialMessage) {
    setStatus("Starting local chat…");
    return fetchJSON(api("/api/sessions"), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ message: initialMessage || "" })
    }).then(function (payload) {
      state.detail = payload;
      state.sessionID = payload.session.session_id;
      textarea.value = "";
      renderTranscript();
      return loadSessions(true).then(function () {
        renderSessions();
        startStream();
      });
    });
  }

  function sendMessage() {
    if (state.sending) {
      return;
    }
    var value = String(textarea.value || "").trim();
    if (!value) {
      return;
    }
    state.sending = true;
    setStatus("Sending…");
    var op = state.sessionID
      ? fetchJSON(api("/api/session/" + encodeURIComponent(state.sessionID) + "/messages"), {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ message: value })
        }).then(function () {
          textarea.value = "";
          setStatus("Waiting for local agent…");
          scheduleRefresh();
        })
      : createSession(value);
    op.catch(function (error) {
      setStatus(error.message || "Send failed.");
    }).finally(function () {
      state.sending = false;
    });
  }

  sessionList.addEventListener("click", function (event) {
    var trigger = event.target.closest("[data-session-id]");
    if (!trigger) {
      return;
    }
    selectSession(trigger.getAttribute("data-session-id"));
  });

  compose.addEventListener("submit", function (event) {
    event.preventDefault();
    sendMessage();
  });

  textarea.addEventListener("keydown", function (event) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      sendMessage();
    }
  });

  newSessionBtn.addEventListener("click", function () {
    createSession("").catch(function (error) {
      setStatus(error.message || "Could not create chat.");
    });
  });

  searchInput.addEventListener("input", function () {
    state.query = String(searchInput.value || "").trim().toLowerCase();
    renderSessions();
  });

  loadSessions(false).catch(function (error) {
    setStatus(error.message || "Failed to load local chats.");
  });
}());
