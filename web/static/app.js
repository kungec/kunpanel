/* ============ 坤面板 - 应用入口:路由 / 登录(验证码) / 响应式布局 ============ */
"use strict";

const MENU = [
  { key: "dashboard", icon: "grid", label: "概览" },
  { key: "websites", icon: "globe", label: "网站" },
  { key: "databases", icon: "database", label: "数据库" },
  { key: "apps", icon: "store", label: "应用商店" },
  { key: "docker", icon: "cube", label: "容器" },
  { key: "files", icon: "folder", label: "文件" },
  { key: "cron", icon: "clock", label: "计划任务" },
  { key: "securityhub", icon: "shield", label: "安全" },
  { key: "monitorhub", icon: "pulse", label: "监控" },
  { key: "terminal", icon: "terminal", label: "终端" },
  { key: "settings", icon: "gear", label: "面板设置" },
];

/* 旧路由 → 新页面(历史书签与深链兼容) */
const ROUTE_ALIAS = {
  plugins: "securityhub",
  firewall: "securityhub",
  logs: "securityhub",
  upmon: "monitorhub",
  processes: "monitorhub",
  services: "monitorhub",
  nettools: "monitorhub",
};

const App = {
  panelName: "坤面板",
  version: "",
  monitorTimer: null,
  captchaID: "",

  isLogged() { return !!localStorage.getItem(TOKEN_KEY); },

  boot() {
    if (!this.isLogged()) { this.renderLogin(); return; }
    this.renderLayout();
    this.bindRouter();
    this.route();
    this.startMonitor();
  },

  /* 绑定路由事件(仅一次) */
  bindRouter() {
    if (this._routerBound) return;
    this._routerBound = true;
    window.addEventListener("hashchange", () => this.route());
  },

  /* ---------- 登录(含图形验证码) ---------- */
  renderLogin() {
    clearPageTimers();
    this.stopMonitor();
    const app = qs("#app");
    app.innerHTML = `
      <div class="login-page">
        <div class="login-decor d1"></div><div class="login-decor d2"></div><div class="login-decor d3"></div>
        <div class="login-card page-fade">
          <div class="login-logo">
            <div class="logo-badge">${logoSVG(64)}</div>
            <h1 id="login-title">${esc(this.panelName)}</h1>
            <p>KunPanel · 轻量级服务器运维管理面板</p>
          </div>
          <div class="login-form">
            <div class="form-item"><label>用户名</label><input class="input" id="login-user" autocomplete="username" placeholder="请输入用户名"></div>
            <div class="form-item"><label>密码</label><input class="input" id="login-pass" type="password" autocomplete="current-password" placeholder="请输入密码"></div>
            <div class="form-item"><label>验证码</label>
              <div class="captcha-row">
                <input class="input" id="login-captcha" placeholder="请输入验证码" maxlength="4" autocomplete="off">
                <div class="captcha-img" id="captcha-img" title="点击刷新验证码"></div>
              </div>
            </div>
            <div class="form-item" style="margin-top:24px"><button class="btn btn-primary btn-block" id="login-btn" style="padding:12px">登 录</button></div>
          </div>
          <div class="login-tip">首次安装的初始账号密码,请查看安装完成时的输出提示</div>
        </div>
      </div>`;

    const loadCaptcha = async () => {
      const box = qs("#captcha-img", app);
      try {
        const d = await api("/auth/captcha");
        this.captchaID = d.captcha_id;
        box.innerHTML = '<img src="data:image/png;base64,' + d.image + '" alt="验证码,点击刷新">';
        box.classList.remove("loading");
      } catch (e) {
        box.innerHTML = '<span style="font-size:12px;color:#909399">加载失败</span>';
      }
    };
    loadCaptcha();
    qs("#captcha-img", app).onclick = loadCaptcha;

    var otpToken = "";

    const loginSuccess = (d) => {
      localStorage.setItem(TOKEN_KEY, d.token);
      localStorage.setItem(USER_KEY, d.username);
      if (d.panel) this.panelName = d.panel;
      toast("欢迎回来, " + d.username);
      this.renderLayout();
      this.bindRouter();
      this.route();
      this.startMonitor();
    };

    const doOtp = async () => {
      const box = qs("#login-otp", app);
      const code = box ? box.value.trim() : "";
      if (!code) { toast("请输入动态码", "error"); return; }
      try {
        const d = await api("/auth/otp", { method: "POST", body: { otp_token: otpToken, code: code } });
        loginSuccess(d);
      } catch (e) {
        toast(e.message, "error");
        box.value = "";
        box.focus();
      }
    };

    const doLogin = async () => {
      const u = qs("#login-user", app).value.trim(),
            p = qs("#login-pass", app).value,
            c = qs("#login-captcha", app).value.trim();
      if (!u || !p) { toast("请输入用户名和密码", "error"); return; }
      if (!c) { toast("请输入验证码", "error"); return; }
      const btn = qs("#login-btn", app);
      btn.disabled = true; btn.textContent = "登录中...";
      try {
        const d = await api("/auth/login", { method: "POST", body: { username: u, password: p, captcha_id: this.captchaID, captcha_code: c } });
        if (d.need_otp) {
          otpToken = d.otp_token;
          if (!qs("#login-otp-row", app)) {
            var row = document.createElement("div");
            row.className = "form-item";
            row.id = "login-otp-row";
            row.innerHTML = '<label>两步验证码</label><input class="input" id="login-otp" placeholder="6 位动态码" maxlength="6" autocomplete="one-time-code">';
            qs(".login-form", app).insertBefore(row, qs("#login-btn", app).parentNode);
            qs("#login-otp", app).onkeydown = (e) => { if (e.key === "Enter") doOtp(); };
          }
          qs("#login-otp", app).focus();
          btn.disabled = false; btn.textContent = "登 录";
          return;
        }
        loginSuccess(d);
      } catch (e) {
        toast(e.message, "error");
        qs("#login-captcha", app).value = "";
        loadCaptcha();
      }
      btn.disabled = false; btn.textContent = "登 录";
    };
    qs("#login-btn", app).onclick = doLogin;
    qs("#login-captcha", app).onkeydown = (e) => { if (e.key === "Enter") doLogin(); };
    qs("#login-pass", app).onkeydown = (e) => { if (e.key === "Enter") qs("#login-captcha", app).focus(); };
    qs("#login-user", app).onkeydown = (e) => { if (e.key === "Enter") qs("#login-pass", app).focus(); };
  },

  /* ---------- 主布局(桌面侧栏 / 移动端抽屉) ---------- */
  renderLayout() {
    const user = localStorage.getItem(USER_KEY) || "admin";
    const token = encodeURIComponent(localStorage.getItem(TOKEN_KEY) || "");
    const av = (cls) => '<div class="user-avatar ' + cls + '">' +
      '<img class="avatar-fallback-img" src="/static/logo.png" alt="默认头像">' +
      '<img class="avatar-img" alt="头像" src="/api/v1/avatar?token=' + token + '&t=' + Date.now() + '" onerror="this.remove()"></div>';
    const app = qs("#app");
    app.innerHTML = `
      <div class="layout ${localStorage.getItem("kp_sidebar_collapse") === "1" ? "collapsed-main" : ""}" id="layout">
        <div class="sidebar-mask" id="sidebar-mask"></div>
        <aside class="sidebar ${localStorage.getItem("kp_sidebar_collapse") === "1" ? "collapsed" : ""}" id="sidebar">
          <div class="sidebar-logo"><div class="logo-badge">${logoSVG(34)}</div><span class="logo-text">${esc(this.panelName)}</span></div>
          <nav class="sidebar-menu">
            ${MENU.map((m) => '<div class="menu-item" data-key="' + m.key + '">' + I(m.icon) + '<span class="menu-label">' + m.label + "</span></div>").join("")}
          </nav>
          <div class="sidebar-foot">
            <div class="collapse-btn" id="btn-collapse">${I("menuFold")}<span class="menu-label">收起菜单</span></div>
            <div class="ver">KunPanel · 轻量服务器面板</div>
          </div>
        </aside>
        <div class="main">
          <header class="topbar">
            <div style="display:flex;align-items:center;gap:12px">
              <button class="btn btn-icon btn-plain mobile-only" id="btn-menu" aria-label="菜单">${I("menu")}</button>
              <div class="topbar-title" id="page-title">概览</div>
            </div>
            <div class="topbar-right">
              <button class="btn btn-icon btn-plain" id="btn-tasks" title="后台任务" style="position:relative">${I("stack")}<span id="tasks-badge" class="notify-badge" style="display:none"></span></button>
              <div class="dropdown" id="notify-dd">
                <button class="btn btn-icon btn-plain" id="btn-notify" title="消息中心" style="position:relative">${I("inbox")}<span id="notify-badge" style="display:none"></span></button>
                <div class="dropdown-menu notify-panel">
                  <div class="dd-header" style="display:flex;justify-content:space-between;align-items:center"><b>消息中心</b>
                    <span style="display:flex;gap:10px"><span style="color:var(--primary);cursor:pointer;font-size:12px" id="notify-clear">清空</span></span></div>
                  <div class="notify-list" id="notify-list"><div class="help" style="padding:14px">加载中...</div></div>
                </div>
              </div>
              <button class="btn btn-icon btn-plain" id="btn-theme" title="切换深色模式">${I("moon")}</button>
              <div class="monitor-chip" id="monitor-chip"><span>CPU <b id="mc-cpu">-</b></span><span>内存 <b id="mc-mem">-</b></span><span class="mc-net">↓ <b id="mc-rx">-</b></span><span class="mc-net">↑ <b id="mc-tx">-</b></span></div>
              <div class="dropdown" id="user-dd">
                <div class="user-chip" title="账号菜单">${av("")}<span class="user-name">${esc(user)}</span>${I("chevronDown")}</div>
                <div class="dropdown-menu">
                  <div class="dd-header">${av("dd-avatar")}<div class="dd-user"><b>${esc(user)}</b><span>管理员 · ${esc(this.panelName)}</span></div></div>
                  <div class="dd-divider"></div>
                  <div class="dropdown-item" id="dd-avatar-change">${I("upload")} 修改头像</div>
                  <div class="dropdown-item" id="dd-avatar-remove">${I("refresh")} 恢复默认头像</div>
                  <div class="dd-divider"></div>
                  <div class="dropdown-item" id="dd-settings">${I("gear")} 面板设置</div>
                  <div class="dropdown-item danger" id="dd-logout">${I("logout")} 退出登录</div>
                </div>
              </div>
            </div>
          </header>
          <main class="content" id="content"></main>
        </div>
      </div>
      <input type="file" id="avatar-input" accept="image/png,image/jpeg,image/gif" style="display:none">`;

    const sidebar = qs("#sidebar", app), mask = qs("#sidebar-mask", app);
    const closeDrawer = () => { sidebar.classList.remove("open"); mask.classList.remove("show"); };
    qs("#btn-menu", app).onclick = () => { sidebar.classList.add("open"); mask.classList.add("show"); };
    mask.onclick = closeDrawer;

    qsa(".menu-item", app).forEach((n) => (n.onclick = () => {
      closeDrawer();
      location.hash = "#/" + n.dataset.key;
    }));
    qs("#btn-collapse", app).onclick = () => {
      if (window.innerWidth <= 768) { closeDrawer(); return; }
      const collapsed = qs("#sidebar", app).classList.toggle("collapsed");
      qs("#layout", app).classList.toggle("collapsed-main", collapsed);
      localStorage.setItem("kp_sidebar_collapse", collapsed ? "1" : "0");
    };
    qs("#user-dd .user-chip", app).onclick = (e) => {
      e.stopPropagation();
      qs("#user-dd .dropdown-menu", app).classList.toggle("show");
    };
    document.addEventListener("click", () => { const dd = qs("#user-dd .dropdown-menu"); if (dd) dd.classList.remove("show"); });

    // ---- 消息中心 ----
    const seenKey = "kp_notify_seen";
    const markSeen = () => localStorage.setItem(seenKey, String(Date.now()));
    async function refreshNotifyBadge() {
      try {
        const after = Number(localStorage.getItem(seenKey) || 0);
        const r = await fetch("/api/v1/notifications?after=" + after, { headers: { "Authorization": "Bearer " + localStorage.getItem("kp_token") } });
        const d = (await r.json()).data || {};
        const badge = document.getElementById("notify-badge");
        if (!badge) return;
        badge.style.display = d.unread > 0 ? "flex" : "none";
        badge.textContent = d.unread > 99 ? "99+" : (d.unread || "");
      } catch (e) { /* ignore */ }
    }
    function renderNotifyPanel(items) {
      const box = document.getElementById("notify-list");
      if (!box) return;
      const kindMap = { alert: ["告警", "#f56c6c"], cron: ["任务", "#e6a23c"], login: ["登录", "#409eff"], cert: ["证书", "#a78bfa"], system: ["系统", "#909399"] };
      if (!items || !items.length) { box.innerHTML = '<div class="help" style="padding:18px;text-align:center">暂无消息,告警/任务/登录事件会出现在这里</div>'; return; }
      box.innerHTML = items.map((it) => {
        const km = kindMap[it.kind] || kindMap.system;
        return '<div class="notify-item"><div class="ni-head"><span class="tag" style="background:' + km[1] + '18;color:' + km[1] + '">' + km[0] + '</span><b>' + esc(it.title) + "</b></div>" +
          (it.body ? '<div class="ni-body">' + esc(it.body.slice(0, 160)) + "</div>" : "") +
          '<div class="ni-time">' + esc(it.time) + "</div></div>";
      }).join("");
    }
    qs("#btn-notify", app).onclick = async (e) => {
      e.stopPropagation();
      const panel = qs("#notify-dd .notify-panel");
      panel.classList.toggle("show");
      if (panel.classList.contains("show")) {
        try {
          const r = await fetch("/api/v1/notifications?n=30", { headers: { "Authorization": "Bearer " + localStorage.getItem("kp_token") } });
          const d = (await r.json()).data || {};
          renderNotifyPanel(d.items);
        } catch (err) { renderNotifyPanel([]); }
        markSeen();
        const badge = document.getElementById("notify-badge");
        if (badge) badge.style.display = "none";
      }
    };
    qs("#notify-dd .notify-panel", app).onclick = (e) => e.stopPropagation();
    document.addEventListener("click", () => { const p = qs("#notify-dd .notify-panel"); if (p) p.classList.remove("show"); });
    const clearBtn = qs("#notify-clear", app);
    if (clearBtn) clearBtn.onclick = async function(e) {
      e.stopPropagation();
      await fetch("/api/v1/notifications/clear", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem("kp_token") } });
      markSeen();
      renderNotifyPanel([]);
      refreshNotifyBadge();
    };
    refreshNotifyBadge();
    setInterval(refreshNotifyBadge, 60000);

    // 后台任务中心:徽标显示运行中任务数,点击打开任务列表
    const refreshTasksBadge = async () => {
      try {
        const list = (await api("/tasks")) || [];
        const n = list.filter((t) => t.status === "running").length;
        const b = document.getElementById("tasks-badge");
        const btn = document.getElementById("btn-tasks");
        if (!b || !btn) return;
        if (n > 0) { b.textContent = n; b.style.display = ""; btn.classList.add("tc-active"); }
        else { b.style.display = "none"; btn.classList.remove("tc-active"); }
      } catch (e) { /* 未登录等场景忽略 */ }
    };
    qs("#btn-tasks", app).onclick = () => taskCenter();
    refreshTasksBadge();
    setInterval(refreshTasksBadge, 30000);
    // 回到前台立即刷新一次,避免后台节流期间的陈旧角标
    document.addEventListener("visibilitychange", () => { if (!document.hidden) { refreshTasksBadge(); refreshNotifyBadge(); } });

    // 面板更新检测:服务端版本与页面启动时不同(说明部署了新版)则提示刷新,避免旧标签页一直跑旧 JS
    const verPoll = async () => {
      try {
        const r = await fetch("/api/v1/system/quick", { headers: { "Authorization": "Bearer " + localStorage.getItem("kp_token") } });
        const d = (await r.json()).data || {};
        const v = d.version || "";
        if (!v) return;
        const boot = sessionStorage.getItem("kp_boot_ver");
        if (!boot) { sessionStorage.setItem("kp_boot_ver", v); return; }
        if (boot !== v) {
          sessionStorage.setItem("kp_boot_ver", v);
          toast("面板已更新到 " + v + ",请刷新页面(Ctrl/Cmd+R)加载新版", "info", 10000);
        }
      } catch (e) {}
    };
    verPoll();
    setInterval(verPoll, 120000);

    // 深色模式切换
    const syncThemeIcon = () => {
      const dark = document.documentElement.getAttribute("data-theme") === "dark";
      qs("#btn-theme", app).innerHTML = I(dark ? "sun" : "moon");
    };
    syncThemeIcon();
    qs("#btn-theme", app).onclick = () => {
      const dark = document.documentElement.getAttribute("data-theme") === "dark";
      if (dark) { document.documentElement.removeAttribute("data-theme"); localStorage.setItem("kp_theme", "light"); }
      else { document.documentElement.setAttribute("data-theme", "dark"); localStorage.setItem("kp_theme", "dark"); }
      syncThemeIcon();
    };
    qs("#dd-settings", app).onclick = () => (location.hash = "#/settings");

    // 头像上传 / 恢复默认
    const avatarInput = qs("#avatar-input", app);
    const refreshAvatars = () => {
      const ts = Date.now();
      const token = encodeURIComponent(localStorage.getItem(TOKEN_KEY) || "");
      qsa(".user-avatar", app).forEach((box) => {
        let img = box.querySelector(".avatar-img");
        if (!img) {
          img = document.createElement("img");
          img.className = "avatar-img";
          img.alt = "头像";
          box.appendChild(img);
        }
        img.src = "/api/v1/avatar?token=" + token + "&t=" + ts;
      });
    };
    qs("#dd-avatar-change", app).onclick = () => avatarInput.click();
    avatarInput.onchange = async () => {
      const f = avatarInput.files[0];
      if (!f) return;
      if (f.size > 3 * 1024 * 1024) { toast("图片不能超过 3MB", "error"); avatarInput.value = ""; return; }
      if (!/^image\//.test(f.type)) { toast("请选择图片文件", "error"); avatarInput.value = ""; return; }
      const fd = new FormData();
      fd.append("avatar", f);
      toast("正在上传头像...", "info");
      try {
        await api("/settings/avatar", { method: "POST", body: fd });
        refreshAvatars();
        toast("头像已更新");
      } catch (e) { toast(e.message, "error"); }
      avatarInput.value = "";
    };
    qs("#dd-avatar-remove", app).onclick = async () => {
      try {
        const r = await api("/settings/avatar", { method: "DELETE" });
        if (r && r.removed) {
          qsa(".avatar-img", app).forEach((img) => img.remove());
          toast("已恢复默认头像");
        } else {
          toast("当前未设置自定义头像", "info");
        }
      } catch (e) { toast(e.message, "error"); }
    };

    qs("#dd-logout", app).onclick = async () => {
      try { await api("/auth/logout", { method: "POST", body: {} }); } catch (e) {}
      localStorage.removeItem(TOKEN_KEY);
      location.hash = "#/login";
      this.renderLogin();
    };
  },

  /* ---------- 路由(带切换动效) ---------- */
  route() {
    if (!this.isLogged()) { this.renderLogin(); return; }
    clearPageTimers();
    PAGES.terminal.cleanup && PAGES.terminal.cleanup();

    const rawKey = (location.hash || "#/dashboard").replace(/^#\//, "").split("?")[0] || "dashboard";
    const key = ROUTE_ALIAS[rawKey] || rawKey;
    const page = PAGES[key] || PAGES.dashboard;
    qsa(".menu-item").forEach((n) => n.classList.toggle("active", n.dataset.key === key));
    const titleEl = qs("#page-title");
    if (titleEl) titleEl.textContent = page.title;

    const content = qs("#content");
    content.classList.toggle("no-padding", key === "terminal");
    if (key === "terminal") content.style.padding = "0"; else content.style.padding = "";
    // 切换动效:重置动画;并打上路由标记,供异步页面丢弃过期渲染
    content.dataset.route = key;
    content.innerHTML = "";
    content.classList.remove("route-enter");
    void content.offsetWidth;
    content.classList.add("route-enter");
    Promise.resolve(page.render(content)).catch(function(e) { toast(e.message || "渲染出错", "error"); });
  },

  /* ---------- 顶栏监控 ---------- */
  startMonitor() {
    this.stopMonitor();
    let busy = false;
    const tick = async () => {
      if (busy || document.visibilityState === "hidden") return; // 后台标签页暂停轮询;上一拍未返回不叠加
      // 概览页有自己的 timer 在拉 /dashboard,避免同一时刻双倍请求
      if (location.hash === "#/" || location.hash === "#/dashboard") return;
      busy = true;
      try {
        const d = await api("/system/quick");
        const s = d.system;
        const cpu = qs("#mc-cpu"), mem = qs("#mc-mem");
        if (!cpu) { this.stopMonitor(); return; }
        cpu.textContent = (s.cpu_percent || 0).toFixed(1) + "%";
        mem.textContent = (s.mem_percent || 0).toFixed(1) + "%";
        const rx = qs("#mc-rx"), tx = qs("#mc-tx");
        if (rx) rx.textContent = fmtSpeed(s.net_rx_speed || 0);
        if (tx) tx.textContent = fmtSpeed(s.net_tx_speed || 0);
        if (d.panel_name) {
          this.panelName = d.panel_name;
          const lt = qs(".logo-text");
          if (lt && lt.textContent !== d.panel_name) lt.textContent = d.panel_name;
        }
      } catch (e) { /* silent */ } finally { busy = false; }
    };
    tick();
    this.monitorTimer = setInterval(tick, 5000);
  },
  stopMonitor() { if (this.monitorTimer) { clearInterval(this.monitorTimer); this.monitorTimer = null; } },
};

document.addEventListener("DOMContentLoaded", () => App.boot());
