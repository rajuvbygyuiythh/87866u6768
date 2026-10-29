// =============================================================================
//  mock-backend.js — in-page fake backend so the UI runs with zero dependencies
// -----------------------------------------------------------------------------
//  Same method names as the Go DesktopApp binding, so app.js cannot tell the
//  difference. Useful for design review and UI-only testing.
// =============================================================================
(function () {
  const settings = {
    connections: {
      enabled: false,
      scheme: "http",
      entries: "1.2.3.4:8080\nuser:pass@1.2.3.5:1080",
    },
    credentials: {
      base_url: "https://api.featherless.ai/v1",
      api_key: "",
      model: "deepseek-ai/DeepSeek-V3-0324",
      temperature: 0.9,
    },
    actions: {
      strategy: "folder",
      sequence: "hey {f|girl} 21\nwyd",
      targets: "@demo_one\n@demo_two",
      folder_path: "D:\\sms",
    },
    timings: {
      reply_min: 2, reply_max: 5,
      silent_min: 10, silent_max: 15,
      reroll_min: 6, reroll_max: 12,
      instant_skip: 5, work_time: 600, no_match: 75,
      sends_first: false,
    },
    profile: { name: "default", backend: "chromedp", launch_mode: "auto" },
  };

  const stats = {
    accounts: 5, running: 0, chats: 0, replies: 0,
    snap_shares: 0, telegram_shares: 0, banned: 0,
  };

  let entries = [];
  let version = 0;
  let running = false;
  let timer = null;
  let step = 0;

  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

  function push(kind, text, meta) {
    version += 1;
    entries.push({
      id: version,
      time: new Date().toLocaleTimeString([], { hour12: false }),
      kind, text,
      meta: meta || "",
    });
    if (entries.length > 400) entries = entries.slice(-400);
  }

  const script = [
    () => push("system", "worker picked up the job", "mode: visible"),
    () => push("out", "hey, wyd", "opener"),
    () => push("in", "hey, who is this?", "[partner]"),
    () => push("out", "just chillin, hbu", "reply · ok"),
    () => push("skip", "partner silent 14s — skipping", "silent_min 10 / silent_max 15"),
    () => push("system", "re-rolling after 8s pause", "reroll 6–12s"),
    () => push("error", "connection refused (demo error)", "retry in 10s"),
    () => push("system", "job finished: 3 chats, 1 share", "cleanup done"),
  ];

  function startTicker() {
    if (timer) return;
    timer = setInterval(() => {
      script[step % script.length]();
      step += 1;
      if (step % 4 === 0) { stats.chats += 1; stats.replies += 2; }
      if (step % 6 === 0) stats.telegram_shares += 1;
      stats.running = Math.min(3, stats.running || 1);
    }, 1100);
  }

  function stopTicker() {
    if (timer) clearInterval(timer);
    timer = null;
    stats.running = 0;
  }

  const mock = {
    async GetSnapshot() {
      await sleep(40);
      return {
        settings,
        strategy: settings.actions.strategy,
        stats: { ...stats, running: running ? 2 : 0 },
        runtime: {
          running,
          kind: "mock",
          data_dir: "/mock/data",
          version: "design-kit-starter",
        },
      };
    },

    async SaveSettings(section, values) {
      await sleep(120);
      if (settings[section] && values && typeof values === "object") {
        settings[section] = { ...settings[section], ...values };
      }
      return this.GetSnapshot();
    },

    async TestConnection(payload) {
      await sleep(420);
      const raw = String((payload && payload.entries) || "").split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
      return {
        results: raw.map((entry, index) => ({
          entry,
          ok: index % 3 !== 2,
          latency_ms: 40 + ((index * 37) % 180),
          message: index % 3 === 2 ? "timeout" : "ok",
        })),
      };
    },

    async TestAPI(credentials) {
      await sleep(520);
      if (!String((credentials && credentials.api_key) || "usb").trim()) {
        throw new Error("API key is empty (demo backend never calls a real API)");
      }
      return "Demo: 200 OK · 1 choice · 12 tokens";
    },

    async StartRun(payload) {
      await sleep(280);
      if (running) return { ok: false, message: "already running" };
      running = true;
      step = 0;
      push("system", `run started (${(payload && payload.mode) || "visible"})`, "detail: demo mode");
      startTicker();
      return { ok: true, message: "started (demo)" };
    },

    async StopRun() {
      await sleep(120);
      running = false;
      stopTicker();
      push("system", "run stopped by user", "browsers closed automatically");
      return { ok: true };
    },

    async RegisterAccounts(count) {
      await sleep(300);
      push("system", `registration requested (${count || 1} account(s))`, "detail: demo mode");
      stats.accounts += count || 1;
      return { ok: true, message: "started (demo)" };
    },

    async LaunchLogin(payload) {
      await sleep(200);
      push("system", `login window requested (${(payload && payload.launch_mode) || "auto"})`, "detail: demo mode");
      return { ok: true, message: "login requested" };
    },

    async GetBrowserStatus() {
      await sleep(160);
      return { problem: "" };
    },

    async CloseBrowsers() {
      await sleep(160);
      push("system", "every browser closed", "demo");
      return { ok: true };
    },

    async GetLogs(after) {
      await sleep(30);
      const from = Number(after || 0);
      return {
        version,
        entries: entries.filter((entry) => entry.id > from),
        cleared: from > version,
        running,
        stats: { ...stats },
      };
    },

    async ClearLogs() {
      await sleep(60);
      entries = [];
      return { ok: true };
    },

    async OpenDataFolder() {
      await sleep(60);
      return "/mock/data (demo path — no folder is opened)";
    },

    async LoadFolder() {
      await sleep(360);
      return {
        count: 3,
        names: ["sequence-a.txt", "sequence-b.txt", "sequence-c.txt"],
      };
    },

    async LoadMessageFolder(payload) {
      await sleep(360);
      const path = String((payload && payload.path) || "").trim();
      if (!path) throw new Error("empty folder path");
      settings.actions.folder_path = path;
      return { ok: true, count: 3 };
    },

    async BrowseMessageFolder() {
      await sleep(160);
      return "D:\\sms (demo — the real dialog opens in the desktop app)";
    },

    async CleanLoadedMessages() {
      await sleep(160);
      return { ok: true };
    },
  };

  window.__consoleMock = mock;
})();