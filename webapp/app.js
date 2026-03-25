const state = {
  providers: [],
  examples: [],
  conversations: [],
  activeId: null,
  mermaid: null,
  mathJaxReady: null,
  renderTimer: null,
  pendingChat: new Set(),
};

const els = {
  conversationList: document.getElementById("conversationList"),
  conversationTitle: document.getElementById("conversationTitle"),
  providerSelect: document.getElementById("providerSelect"),
  modelSelect: document.getElementById("modelSelect"),
  messages: document.getElementById("messages"),
  chatForm: document.getElementById("chatForm"),
  chatInput: document.getElementById("chatInput"),
  newConversation: document.getElementById("newConversation"),
  exampleSelect: document.getElementById("exampleSelect"),
  loadExample: document.getElementById("loadExample"),
  renderStatus: document.getElementById("renderStatus"),
  sourceEditor: document.getElementById("sourceEditor"),
  renderOutput: document.getElementById("renderOutput"),
  messageTemplate: document.getElementById("messageTemplate"),
};

init().catch((error) => {
  console.error(error);
  alert(error.message);
});

async function init() {
  bindTabs();
  bindActions();
  await Promise.all([loadProviders(), loadExamples()]);
  loadConversations();
  if (!state.conversations.length) {
    addConversation();
  }
  renderConversations();
  renderActiveConversation();
}

function bindTabs() {
  document.querySelectorAll(".tab").forEach((tab) => {
    tab.addEventListener("click", () => {
      document.querySelectorAll(".tab").forEach((node) => node.classList.remove("is-active"));
      document.querySelectorAll(".tab-panel").forEach((node) => node.classList.remove("is-active"));
      tab.classList.add("is-active");
      document.getElementById(`${tab.dataset.tab}Tab`).classList.add("is-active");
    });
  });
}

function bindActions() {
  els.newConversation.addEventListener("click", () => {
    addConversation();
    renderConversations();
    renderActiveConversation();
  });

  els.providerSelect.addEventListener("change", () => {
    const conversation = activeConversation();
    conversation.provider = els.providerSelect.value;
    conversation.model = firstModelForProvider(conversation.provider);
    saveConversations();
    renderActiveConversation();
  });

  els.modelSelect.addEventListener("change", () => {
    const conversation = activeConversation();
    conversation.model = els.modelSelect.value;
    saveConversations();
  });

  els.loadExample.addEventListener("click", () => {
    const conversation = activeConversation();
    const example = state.examples.find((item) => item.title === els.exampleSelect.value);
    if (!example) {
      return;
    }
    conversation.source = example.source;
    if (!conversation.messages.length) {
      conversation.title = example.title;
    }
    saveConversations();
    renderConversations();
    renderActiveConversation();
  });

  els.sourceEditor.addEventListener("input", () => {
    const conversation = activeConversation();
    if (!conversation) {
      return;
    }
    conversation.source = els.sourceEditor.value;
    saveConversations();
    scheduleInterpretationRender();
  });
  els.chatForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    await sendChat();
  });
}

async function loadProviders() {
  const response = await fetch("/api/providers");
  const payload = await response.json();
  state.providers = payload.providers;
}

async function loadExamples() {
  const response = await fetch("/api/examples");
  state.examples = await response.json();
  els.exampleSelect.innerHTML = state.examples.map((item) => `<option>${escapeHTML(item.title)}</option>`).join("");
}

function loadConversations() {
  try {
    state.conversations = JSON.parse(localStorage.getItem("goctl2.conversations") || "[]");
  } catch {
    state.conversations = [];
  }
  state.conversations.forEach(normalizeConversationTurns);
  state.activeId = localStorage.getItem("goctl2.activeId");
}

function saveConversations() {
  localStorage.setItem("goctl2.conversations", JSON.stringify(state.conversations));
  localStorage.setItem("goctl2.activeId", state.activeId || "");
}

function addConversation() {
  const provider = state.providers.find((item) => item.available)?.id || "builtin";
  const conversation = {
    id: crypto.randomUUID(),
    title: "New Requirement",
    provider,
    model: firstModelForProvider(provider),
    source: state.examples[0]?.source || "(model\n  )",
    messages: [],
    renderMarkdown: "",
    renderError: "",
  };
  state.conversations.unshift(conversation);
  state.activeId = conversation.id;
  saveConversations();
}

function normalizeConversationTurns(conversation) {
  if (typeof conversation.renderMarkdown !== "string") {
    conversation.renderMarkdown = "";
  }
  if (typeof conversation.renderError !== "string") {
    conversation.renderError = "";
  }
  let turn = 1;
  for (const message of conversation.messages || []) {
    if (!message.turn) {
      message.turn = turn;
    }
    if (message.role === "assistant") {
      turn = Math.max(turn, Number(message.turn) || turn) + 1;
    } else {
      turn = Math.max(turn, Number(message.turn) || turn);
    }
  }
}

function activeConversation() {
  return state.conversations.find((item) => item.id === state.activeId) || state.conversations[0];
}

function renderConversations() {
  els.conversationList.innerHTML = "";
  state.conversations.forEach((item) => {
    const button = document.createElement("button");
    button.className = `conversation-item${item.id === state.activeId ? " active" : ""}`;
    button.textContent = item.title;
    button.addEventListener("click", () => {
      state.activeId = item.id;
      saveConversations();
      renderConversations();
      renderActiveConversation();
    });
    els.conversationList.appendChild(button);
  });
}

function renderActiveConversation() {
  const conversation = activeConversation();
  if (!conversation) {
    return;
  }
  state.activeId = conversation.id;
  els.conversationTitle.textContent = conversation.title;
  els.sourceEditor.value = conversation.source;
  renderProviderSelectors(conversation);
  renderMessages(conversation.messages, state.pendingChat.has(conversation.id));
  renderCachedInterpretation(conversation);
  scheduleInterpretationRender();
}

function renderProviderSelectors(conversation) {
  els.providerSelect.innerHTML = state.providers.map((item) => {
    const suffix = item.available ? "" : " (configure key)";
    return `<option value="${escapeHTML(item.id)}">${escapeHTML(item.label + suffix)}</option>`;
  }).join("");
  els.providerSelect.value = conversation.provider;
  const provider = state.providers.find((item) => item.id === conversation.provider) || state.providers[0];
  els.modelSelect.innerHTML = (provider?.models || []).map((model) => `<option value="${escapeHTML(model)}">${escapeHTML(model)}</option>`).join("");
  if (!provider?.models.includes(conversation.model)) {
    conversation.model = provider?.models[0] || "";
    saveConversations();
  }
  els.modelSelect.value = conversation.model;
}

function renderMessages(messages, pending = false) {
  els.messages.innerHTML = "";
  messages.forEach((message) => els.messages.appendChild(renderMessageNode(message)));
  let pendingNode = null;
  if (pending) {
    pendingNode = renderPendingMessageNode(messages);
    els.messages.appendChild(pendingNode);
  }
  scrollMessagesToBottom(pendingNode);
}

function renderMessageNode(message) {
  const node = els.messageTemplate.content.firstElementChild.cloneNode(true);
  node.classList.add(message.role);
  const turnLabel = turnTag(message);
  node.querySelector(".message-role").textContent = `${turnLabel} ${message.role}`;
  const body = node.querySelector(".message-body");
  body.appendChild(renderMarkdown(message.content));
  hydrateRichContent(body).catch((error) => console.error(error));
  return node;
}

function renderPendingMessageNode(messages) {
  const node = els.messageTemplate.content.firstElementChild.cloneNode(true);
  node.classList.add("assistant", "pending");
  const nextTurn = nextAssistantTurnNumber(messages);
  node.querySelector(".message-role").textContent = `A${nextTurn} assistant`;
  const body = node.querySelector(".message-body");
  const spinner = document.createElement("div");
  spinner.className = "pending-spinner";
  spinner.setAttribute("aria-label", "Assistant is responding");
  spinner.innerHTML = "<span></span><span></span><span></span>";
  body.appendChild(spinner);
  return node;
}

function scrollMessagesToBottom(targetNode = null) {
  requestAnimationFrame(() => {
    if (targetNode?.isConnected) {
      targetNode.scrollIntoView({ block: "end", behavior: "auto" });
      return;
    }
    els.messages.scrollTop = els.messages.scrollHeight;
  });
}

function scheduleInterpretationRender() {
  const conversation = activeConversation();
  if (!conversation) {
    return;
  }
  if (state.renderTimer) {
    clearTimeout(state.renderTimer);
  }
  setRenderStatus("Rendering interpretation...");
  state.renderTimer = setTimeout(() => {
    state.renderTimer = null;
    renderInterpretation().catch((error) => {
      console.error(error);
      const current = activeConversation();
      if (current) {
        current.renderError = error.message;
        saveConversations();
        renderCachedInterpretation(current);
      }
      setRenderStatus("Render failed.");
    });
  }, 350);
}

async function renderInterpretation() {
  const conversation = activeConversation();
  if (!conversation) {
    return;
  }
  const conversationID = conversation.id;
  conversation.source = els.sourceEditor.value;
  saveConversations();
  const response = await fetch("/api/render", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ source: conversation.source, format: "markdown" }),
  });
  const payload = await response.json();
  if (!response.ok) {
    conversation.renderMarkdown = "";
    conversation.renderError = payload.error || "Render failed.";
    saveConversations();
    if (activeConversation()?.id === conversationID) {
      renderCachedInterpretation(conversation);
      setRenderStatus("Render failed.");
    }
    return;
  }
  conversation.renderMarkdown = payload.markdown;
  conversation.renderError = "";
  saveConversations();
  if (activeConversation()?.id === conversationID) {
    renderCachedInterpretation(conversation);
    setRenderStatus("Interpretation is up to date.");
  }
}

function renderCachedInterpretation(conversation) {
  els.renderOutput.innerHTML = "";
  if (conversation.renderError) {
    const error = document.createElement("div");
    error.className = "render-error";
    error.innerHTML = `<strong>Render error</strong><pre><code>${escapeHTML(conversation.renderError)}</code></pre>`;
    els.renderOutput.appendChild(error);
    return;
  }
  if (!conversation.renderMarkdown) {
    const empty = document.createElement("div");
    empty.className = "render-placeholder";
    empty.textContent = "The compiled interpretation appears here as the Lisp changes.";
    els.renderOutput.appendChild(empty);
    return;
  }
  els.renderOutput.appendChild(renderMarkdown(conversation.renderMarkdown));
  hydrateRichContent(els.renderOutput).catch((error) => console.error(error));
}

function setRenderStatus(message) {
  if (els.renderStatus) {
    els.renderStatus.textContent = message;
  }
}

async function sendChat() {
  const conversation = activeConversation();
  const content = els.chatInput.value.trim();
  if (!content) {
    return;
  }
  conversation.source = els.sourceEditor.value;
  conversation.provider = els.providerSelect.value;
  conversation.model = els.modelSelect.value;
  const turn = nextTurnNumber(conversation);
  conversation.messages.push({ role: "user", content, turn });
  if (conversation.title === "New Requirement") {
    conversation.title = content.slice(0, 48);
  }
  els.chatInput.value = "";
  state.pendingChat.add(conversation.id);
  saveConversations();
  renderConversations();
  renderMessages(conversation.messages, true);

  try {
    const response = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        provider: conversation.provider,
        model: conversation.model,
        source: conversation.source,
        messages: conversation.messages,
      }),
    });
    const payload = await response.json();
    if (!response.ok) {
      conversation.messages.push({ role: "assistant", content: `Error:\n\n\`\`\`text\n${payload.error}\n\`\`\``, turn });
    } else {
      conversation.messages.push({ role: "assistant", content: payload.content, turn });
    }
  } catch (error) {
    conversation.messages.push({ role: "assistant", content: `Error:\n\n\`\`\`text\n${error.message}\n\`\`\``, turn });
  } finally {
    state.pendingChat.delete(conversation.id);
  }
  saveConversations();
  if (activeConversation()?.id === conversation.id) {
    renderMessages(conversation.messages, false);
  }
}

function firstModelForProvider(providerId) {
  return state.providers.find((item) => item.id === providerId)?.models?.[0] || "";
}

function renderMarkdown(markdown) {
  const root = document.createElement("div");
  const parts = markdown.split(/```/);
  parts.forEach((part, index) => {
    if (index % 2 === 1) {
      const newline = part.indexOf("\n");
      const lang = (newline >= 0 ? part.slice(0, newline) : part).trim();
      const body = newline >= 0 ? part.slice(newline + 1) : "";
      root.appendChild(renderCodeBlock(lang, body));
      return;
    }
    const block = document.createElement("div");
    block.innerHTML = renderInlineMarkdown(part);
    if (block.innerHTML.trim()) {
      const wrapper = document.createElement("p");
      wrapper.innerHTML = block.innerHTML;
      root.appendChild(wrapper);
    }
  });
  return root;
}

function renderInlineMarkdown(text) {
  return escapeHTML(text)
    .replace(/!\[([^\]]*)\]\((https?:\/\/[^)\s]+)\)/g, '<img class="inline-image" alt="$1" src="$2">')
    .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>')
    .replace(/\n{2,}/g, "</p><p>")
    .replace(/\n/g, "<br>")
    .replace(/`([^`]+)`/g, "<code>$1</code>");
}

function renderCodeBlock(lang, body) {
  if (lang === "mermaid") {
    const wrapper = document.createElement("div");
    wrapper.className = "rendered-mermaid";
    wrapper.dataset.mermaid = body.trim();
    wrapper.textContent = body.trim();
    return wrapper;
  }
  if (lang === "latex" || lang === "tex" || lang === "math") {
    const wrapper = document.createElement("div");
    wrapper.className = "rendered-math";
    wrapper.textContent = `$$\n${body.trim()}\n$$`;
    return wrapper;
  }
  if (lang === "html") {
    const wrapper = document.createElement("div");
    wrapper.className = "rendered-html";
    wrapper.innerHTML = body;
    return wrapper;
  }
  if (lang === "svg") {
    const wrapper = document.createElement("div");
    wrapper.className = "rendered-svg";
    wrapper.innerHTML = body;
    return wrapper;
  }
  const pre = document.createElement("pre");
  const code = document.createElement("code");
  if (lang) {
    code.dataset.lang = lang;
  }
  code.textContent = body.trim();
  pre.appendChild(code);
  return pre;
}

async function hydrateRichContent(root) {
  await renderMermaidBlocks(root);
  await renderMath(root);
}

async function ensureMermaid() {
  if (state.mermaid) {
    return state.mermaid;
  }
  const module = await import("https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs");
  module.default.initialize({
    startOnLoad: false,
    theme: "dark",
    securityLevel: "loose",
  });
  state.mermaid = module.default;
  return state.mermaid;
}

async function renderMermaidBlocks(root) {
  const blocks = Array.from(root.querySelectorAll("[data-mermaid]"));
  if (!blocks.length) {
    return;
  }
  let mermaid;
  try {
    mermaid = await ensureMermaid();
  } catch (error) {
    console.error(error);
    return;
  }
  for (const block of blocks) {
    const source = block.dataset.mermaid;
    const id = `mermaid-${crypto.randomUUID()}`;
    try {
      const { svg } = await mermaid.render(id, source);
      block.innerHTML = svg;
      delete block.dataset.mermaid;
    } catch (error) {
      block.innerHTML = "";
      const pre = document.createElement("pre");
      const code = document.createElement("code");
      code.textContent = source;
      pre.appendChild(code);
      block.appendChild(pre);
    }
  }
}

async function ensureMathJax() {
  if (window.MathJax?.typesetPromise) {
    return window.MathJax;
  }
  if (state.mathJaxReady) {
    return state.mathJaxReady;
  }
  state.mathJaxReady = new Promise((resolve, reject) => {
    window.MathJax = {
      tex: {
        inlineMath: [["$", "$"], ["\\(", "\\)"]],
        displayMath: [["$$", "$$"], ["\\[", "\\]"]],
      },
      svg: {
        fontCache: "global",
      },
      startup: {
        pageReady: () => {
          resolve(window.MathJax);
          return window.MathJax.startup.defaultPageReady();
        },
      },
    };
    const script = document.createElement("script");
    script.src = "https://cdn.jsdelivr.net/npm/mathjax@3/es5/tex-svg.js";
    script.async = true;
    script.onerror = reject;
    document.head.appendChild(script);
  });
  return state.mathJaxReady;
}

async function renderMath(root) {
  if (!root.textContent.includes("$") && !root.querySelector(".rendered-math")) {
    return;
  }
  let mathJax;
  try {
    mathJax = await ensureMathJax();
  } catch (error) {
    console.error(error);
    return;
  }
  try {
    await mathJax.typesetPromise([root]);
  } catch (error) {
    console.error(error);
  }
}

function escapeHTML(value) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function nextTurnNumber(conversation) {
  return conversation.messages.reduce((max, message) => Math.max(max, Number(message.turn) || 0), 0) + 1;
}

function nextAssistantTurnNumber(messages) {
  return messages.reduce((max, message) => {
    if (message.role !== "assistant") {
      return max;
    }
    return Math.max(max, Number(message.turn) || 0);
  }, 0) + 1;
}

function turnTag(message) {
  const prefix = message.role === "assistant" ? "A" : "U";
  const turn = Number(message.turn) || 0;
  return `${prefix}${turn || "?"}`;
}
