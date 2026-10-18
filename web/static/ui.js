/* ============ 坤面板 - UI 基础库:API / Toast / Modal / SVG 图标 / 骨架屏 ============ */
"use strict";

/* ---------- 常量 ---------- */
const TOKEN_KEY = "kp_token";
const USER_KEY = "kp_user";

/* ---------- API ---------- */
/* timeout:请求超时毫秒数,默认 30s;传 0 关闭超时(大文件上传等)。超时立即中断,
   防止挂起请求占满浏览器同域 6 连接池导致整个面板假死。 */
async function api(path, opts = {}) {
  const { method = "GET", body = null, raw = false, timeout = 30000 } = opts;
  const headers = {};
  const token = localStorage.getItem(TOKEN_KEY);
  if (token) headers["Authorization"] = "Bearer " + token;
  if (body !== null && !(body instanceof FormData)) headers["Content-Type"] = "application/json";
  const ctrl = timeout ? new AbortController() : null;
  const tid = ctrl ? setTimeout(() => ctrl.abort(), timeout) : null;
  let res;
  try {
    res = await fetch("/api/v1" + path, {
      method, headers, signal: ctrl ? ctrl.signal : undefined,
      body: body instanceof FormData ? body : (body !== null ? JSON.stringify(body) : undefined),
    });
  } catch (e) {
    if (e && e.name === "AbortError") throw new Error("请求超时,请检查网络或稍后重试");
    throw e;
  } finally {
    if (tid) clearTimeout(tid);
  }
  let json = null;
  try { json = await res.json(); } catch (e) { /* ignore */ }
  checkPanelBuild(res);
  if (res.status === 401) {
    if (typeof App !== "undefined" && App.isLogged && App.isLogged()) {
      localStorage.removeItem(TOKEN_KEY);
      location.hash = "#/login";
      if (App.renderLayout) App.renderLogin();
    }
    throw new Error((json && json.message) || "登录已过期");
  }
  if (!res.ok || !json || json.code !== 0) {
    throw new Error((json && json.message) || ("请求失败 (" + res.status + ")"));
  }
  return raw ? json : json.data;
}

/* ---------- 面板升级提示 ---------- */
/* 后端每个响应都带 X-Panel-Build(构建版本)。长驻标签页在面板升级后仍跑旧版前端,
   若不提示,用户会把"新功能看不到"当成 bug。检测到不一致只提示一次,点击即刷新。 */
let buildWarned = "";
function checkPanelBuild(res) {
  try {
    const build = res.headers.get("X-Panel-Build");
    if (!build) return;
    const myScript = document.querySelector('script[src*="pages.js"]');
    const m = myScript && /[?&]v=([0-9a-f]+)/.exec(myScript.src);
    const mine = m ? m[1] : "";
    if (!mine || mine === build || buildWarned === build) return;
    buildWarned = build;
    toast("面板已更新到新版本,点击刷新页面以启用新功能", "info", 15000, function() { location.reload(); });
  } catch (e) { /* 忽略 */ }
}

/* ---------- 工具 ---------- */
function esc(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}
function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return (n % 1 === 0 ? n : n.toFixed(1)) + " B";
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return n.toFixed(2) + " " + units[i];
}
function fmtSpeed(nPerSec) { return fmtBytes(nPerSec) + "/s"; }
function fmtUptime(sec) {
  sec = Number(sec) || 0;
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d > 0) return d + " 天 " + h + " 小时 " + m + " 分";
  if (h > 0) return h + " 小时 " + m + " 分";
  return m + " 分钟";
}
function basename(p) { p = String(p || ""); const i = Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\")); return i >= 0 ? p.slice(i + 1) : p; }
function qs(sel, root) { return (root || document).querySelector(sel); }
function qsa(sel, root) { return Array.from((root || document).querySelectorAll(sel)); }

/* ---------- SVG 图标库 ---------- */
/* 用法: I("globe") 输出 24x24 stroke SVG */
const I = (name, cls) => {
  const svg = ICONS[name] || ICONS.box;
  return '<svg class="' + (cls || "") + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + svg + "</svg>";
};
const ICONS = {
  grid: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.6 3.8 5.7 3.8 9s-1.3 6.4-3.8 9c-2.5-2.6-3.8-5.7-3.8-9S9.5 5.6 12 3z"/>',
  store: '<path d="M4 7l1.2-3h13.6L20 7"/><path d="M4 7h16v3a2.8 2.8 0 0 1-5.6 0 2.9 2.9 0 0 1-4.8 0A2.8 2.8 0 0 1 4 10V7z"/><path d="M5 12.8V20h14v-7.2M9.5 20v-4.5h5V20"/>',
  cube: '<path d="M21 7.5v9L12 21l-9-4.5v-9L12 3l9 4.5z"/><path d="M3.5 7.7L12 12l8.5-4.3M12 12v8.7"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3.2 2.2"/>',
  folder: '<path d="M3.5 6.5A1.5 1.5 0 0 1 5 5h4.5l2 2.5H19a1.5 1.5 0 0 1 1.5 1.5v8.5A1.5 1.5 0 0 1 19 19H5a1.5 1.5 0 0 1-1.5-1.5v-11z"/>',
  folderOpen: '<path d="M3.5 6.5A1.5 1.5 0 0 1 5 5h4.5l2 2.5H19a1.5 1.5 0 0 1 1.5 1.5V11H3.7"/><path d="M3.7 11l-1.4 6.2A1.4 1.4 0 0 0 3.7 19h15.2a1.4 1.4 0 0 0 1.4-1.1L21.8 12a1 1 0 0 0-1-1.2H3.7z"/>',
  terminal: '<rect x="3" y="4.5" width="18" height="15" rx="2"/><path d="M7 9.5l3 2.8-3 2.8M12.5 15.5H17"/>',
  maximize: '<path d="M4 9V5.5A1.5 1.5 0 0 1 5.5 4H9M15 4h3.5A1.5 1.5 0 0 1 20 5.5V9M20 15v3.5a1.5 1.5 0 0 1-1.5 1.5H15M9 20H5.5A1.5 1.5 0 0 1 4 18.5V15"/>',
  logs: '<path d="M7 3.5h7.5L19 8v11a1.5 1.5 0 0 1-1.5 1.5h-10A1.5 1.5 0 0 1 6 19V5A1.5 1.5 0 0 1 7.5 3.5z" transform="translate(-.5 0)"/><path d="M9 12h6M9 15.5h6M9 8.5h2.5"/>',
  gear: '<circle cx="12" cy="12" r="3.2"/><path d="M19.2 12a7.2 7.2 0 0 0-.14-1.4l2-1.55-2-3.46-2.36.95a7.3 7.3 0 0 0-2.42-1.4L14 2.6h-4l-.28 2.54a7.3 7.3 0 0 0-2.42 1.4l-2.36-.95-2 3.46 2 1.55a7.2 7.2 0 0 0 0 2.8l-2 1.55 2 3.46 2.36-.95a7.3 7.3 0 0 0 2.42 1.4L10 21.4h4l.28-2.54a7.3 7.3 0 0 0 2.42-1.4l2.36.95 2-3.46-2-1.55c.09-.46.14-.93.14-1.4z"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4.5 20.5c1.3-3.4 4.1-5 7.5-5s6.2 1.6 7.5 5"/>',
  logout: '<path d="M9 4.5H6A1.5 1.5 0 0 0 4.5 6v12A1.5 1.5 0 0 0 6 19.5h3M15.5 15.5L19 12l-3.5-3.5M19 12H9.5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  refresh: '<path d="M20 11a8 8 0 1 0-2.34 6.34M20 5v6h-6"/>',
  play: '<path d="M7.5 5.5l11 6.5-11 6.5v-13z"/>',
  stop: '<rect x="6.5" y="6.5" width="11" height="11" rx="1.5"/>',
  restart: '<path d="M4.5 12a7.5 7.5 0 1 1 2.2 5.3M4.5 13v4.5H9"/>',
  trash: '<path d="M4.5 6.5h15M9.5 6V4.5h5V6M6.5 6.5l1 13h9l1-13M10 10.5v5.5M14 10.5v5.5"/>',
  edit: '<path d="M4 20h4.5L20 8.5a2.1 2.1 0 0 0-3-3L5.5 17 4 20z"/><path d="M14.5 7l3 3"/>',
  download: '<path d="M12 4v11M7.5 11L12 15.5 16.5 11M4.5 19.5h15"/>',
  upload: '<path d="M12 15V4M7.5 8.5L12 4l4.5 4.5M4.5 19.5h15"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  chevronRight: '<path d="M9 5.5l6.5 6.5L9 18.5"/>',
  chevronLeft: '<path d="M15 5.5L8.5 12 15 18.5"/>',
  chevronDown: '<path d="M6 9.5l6 6 6-6"/>',
  menuFold: '<path d="M4 6.5h16M4 12h10M4 17.5h16M18.5 9.5l2.5 2.5-2.5 2.5"/>',
  menu: '<path d="M4 6.5h16M4 12h16M4 17.5h16"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><rect x="10" y="10" width="4" height="4"/><path d="M9 3v3M15 3v3M9 18v3M15 18v3M3 9h3M3 15h3M18 9h3M18 15h3"/>',
  memory: '<rect x="3.5" y="7" width="17" height="10" rx="2"/><path d="M7.5 10.5v3M12 10.5v3M16.5 10.5v3"/>',
  disk: '<circle cx="12" cy="12" r="8.5"/><circle cx="12" cy="12" r="2.5"/><path d="M12 3.5A8.5 8.5 0 0 1 20.5 12"/>',
  netUp: '<path d="M3 17l6-6 4 4 8-8M15 7h6v6"/>',
  netDown: '<path d="M3 7l6 6 4-4 8 8M15 17h6v-6"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5.5M12 7.6v.4"/>',
  warn: '<path d="M12 3.5L2.5 20h19L12 3.5z"/><path d="M12 9.5v5M12 17.2v.4"/>',
  file: '<path d="M7 3.5h7.5L19 8v11a1.5 1.5 0 0 1-1.5 1.5h-10A1.5 1.5 0 0 1 6 19V5A1.5 1.5 0 0 1 7.5 3.5z"/><path d="M14 3.5V8h4.5"/>',
  archive: '<rect x="3.5" y="4" width="17" height="5" rx="1"/><path d="M5.5 9v9.5A1.5 1.5 0 0 0 7 20h10a1.5 1.5 0 0 0 1.5-1.5V9M10 13h4"/>',
  eye: '<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>',
  key: '<circle cx="8" cy="14.5" r="4"/><path d="M11 11.5L20 3M16 6.5l2.5 2.5M13.5 9l2 2"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="1.5"/><path d="M5 15H4.5A1.5 1.5 0 0 1 3 13.5v-9A1.5 1.5 0 0 1 4.5 3h9A1.5 1.5 0 0 1 15 4.5V5"/>',
  external: '<path d="M14 4.5h5.5V10M19.5 4.5L11 13M9 5H6a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2v-3"/>',
  database: '<ellipse cx="12" cy="5.5" rx="8" ry="3"/><path d="M4 5.5v13c0 1.66 3.58 3 8 3s8-1.34 8-3v-13"/><path d="M4 12c0 1.66 3.58 3 8 3s8-1.34 8-3"/>',
  stack: '<path d="M12 3l9 4.5-9 4.5L3 7.5 12 3z"/><path d="M3 12l9 4.5 9-4.5M3 16.5L12 21l9-4.5"/>',
  layout: '<rect x="3.5" y="3.5" width="17" height="17" rx="2"/><path d="M3.5 9.5h17M9.5 9.5V20.5"/>',
  pulse: '<path d="M3 12h4l2.5-7 4 14L16 12h5"/>',
  feather: '<path d="M20 4c-6 0-11 4-13 10l-3 6M6.5 14.5H13M10 8.5h7"/><path d="M4 20l3.5-3.5"/>',
  tool: '<path d="M13.5 10.5L4.6 19.4a1.9 1.9 0 0 0 2.7 2.7l8.9-8.9"/><path d="M14 6a4.2 4.2 0 0 1 5.6-3.4l-2.9 2.9.9 2.9 2.9.9A4.2 4.2 0 0 1 14 6z"/>',
  shuffle: '<path d="M3 6.5h3.5c5 0 6 8 11 8H21M3 14.5h3.5c1.6 0 2.8-.8 3.9-1.9M21 6.5h-3.5c-1.7 0-2.9.9-4 2"/><path d="M18.5 4l2.5 2.5L18.5 9M18.5 12l2.5 2.5-2.5 2.5"/>',
  box: '<path d="M4 8l8-4.5L20 8v8l-8 4.5L4 16V8z"/><path d="M4.3 8.2L12 12.4l7.7-4.2M12 12.4V20"/>',
  server: '<rect x="3.5" y="4" width="17" height="7" rx="1.5"/><rect x="3.5" y="13" width="17" height="7" rx="1.5"/><path d="M7 7.5h.4M7 16.5h.4"/>',
  shield: '<path d="M12 3l7.5 2.8V11c0 5-3.2 8.3-7.5 10-4.3-1.7-7.5-5-7.5-10V5.8L12 3z"/><path d="M9 11.8l2.2 2.2 4-4.2"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20.5 20.5L16 16"/>',
  home: '<path d="M4 11l8-7 8 7"/><path d="M6 9.5V20h12V9.5"/>',
  bolt: '<path d="M13 2.5L4.5 13.5H11L9.5 21.5 19 10h-6.5L13 2.5z"/>',
  inbox: '<path d="M3.5 13.5L6 5h12l2.5 8.5v4a1.5 1.5 0 0 1-1.5 1.5H5a1.5 1.5 0 0 1-1.5-1.5v-4z"/><path d="M3.5 13.5H9a3 3 0 0 0 6 0h5.5"/>',
  puzzle: '<path d="M9.5 4.5a2 2 0 1 1 4 0V6H16a1.5 1.5 0 0 1 1.5 1.5v2.8h1.3a2 2 0 1 1 0 4H17.5V19a1.5 1.5 0 0 1-1.5 1.5h-3.2v-1.6a2 2 0 1 0-4 0v1.6H5.5A1.5 1.5 0 0 1 4 19v-3.2h1.4a2 2 0 1 0 0-4H4V7.5A1.5 1.5 0 0 1 5.5 6H9.5V4.5z"/>',
  shield2: '<path d="M12 3l7.5 2.8V11c0 5-3.2 8.3-7.5 10-4.3-1.7-7.5-5-7.5-10V5.8L12 3z"/><path d="M12 8v5M12 16.2v.4"/>',
  moon: '<path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z"/>',
  sun: '<circle cx="12" cy="12" r="4.5"/><path d="M12 2.5v2.5M12 19v2.5M2.5 12H5M19 12h2.5M4.9 4.9l1.8 1.8M17.3 17.3l1.8 1.8M19.1 4.9l-1.8 1.8M6.7 17.3l-1.8 1.8"/>',
};

/* 应用图标配色(icon name -> 渐变色) */
const ICON_GRAD = {
  globe: ["#38bdf8", "#0369a1"],
  database: ["#60a5fa", "#1d4ed8"],
  stack: ["#f87171", "#b91c1c"],
  layout: ["#4ade80", "#15803d"],
  cube: ["#67e8f9", "#0e7490"],
  pulse: ["#fb923c", "#c2410c"],
  feather: ["#a78bfa", "#6d28d9"],
  tool: ["#facc15", "#a16207"],
  shuffle: ["#f472b6", "#be185d"],
  server: ["#94a3b8", "#334155"],
};

/* 彩色渐变图标盒 */
function iconBox(name, size) {
  const [c1, c2] = ICON_GRAD[name] || ["#60a5fa", "#1d4ed8"];
  size = size || 44;
  return '<div class="icon-box" style="width:' + size + "px;height:" + size + "px;background:linear-gradient(135deg," + c1 + "," + c2 + ')">' + I(name) + "</div>";
}

/* 应用商店专属图标:品牌渐变底 + 应用标识字母(每个应用可区分) */
const APP_BRAND = {
  nginx: { g: ["#22c55e", "#15803d"], t: "N" },
  openresty: { g: ["#10b981", "#065f46"], t: "OR" },
  php: { g: ["#8b5cf6", "#5b21b6"], t: "PHP" },
  mysql: { g: ["#38bdf8", "#0369a1"], t: "MY" },
  mariadb: { g: ["#b45309", "#78350f"], t: "MD" },
  postgresql: { g: ["#3b82f6", "#1e3a8a"], t: "PG" },
  phpmyadmin: { g: ["#f59e0b", "#b45309"], t: "PA" },
  redis: { g: ["#ef4444", "#991b1b"], t: "R" },
  wordpress: { g: ["#3b82f6", "#1d4ed8"], t: "W" },
  portainer: { g: ["#0ea5e9", "#0c4a6e"], t: "PT" },
  "uptime-kuma": { g: ["#22c55e", "#065f46"], t: "UM" },
  memos: { g: ["#0ea5e9", "#1d4ed8"], t: "M" },
  "it-tools": { g: ["#64748b", "#1e293b"], t: "IT" },
  npm: { g: ["#ef4444", "#7f1d1d"], t: "NPM" },
};
function appIcon(t, size) {
  size = size || 44;
  var b = APP_BRAND[t.key] || null;
  if (!b) return iconBox(t.icon || "box", size);
  var fs = Math.max(11, Math.round(size * 0.32));
  return '<div class="icon-box" style="width:' + size + 'px;height:' + size + 'px;display:flex;align-items:center;justify-content:center;background:linear-gradient(135deg,' + b.g[0] + "," + b.g[1] + ')"><span style="color:#fff;font-weight:700;font-size:' + fs + 'px;letter-spacing:-.02em">' + b.t + "</span></div>";
}

/* 品牌 Logo(坤坤鸡吉祥物) */
function logoSVG(size) {
  size = size || 34;
  return '<img src="/static/logo.png" width="' + size + '" height="' + size + '" style="display:block;border-radius:' + Math.round(size * 0.26) + 'px" alt="logo">';
}

/* ---------- 骨架屏 ---------- */
function skeletonCards(n) {
  let html = '<div class="grid-4">';
  for (let i = 0; i < (n || 4); i++) {
    html += '<div class="card stat-card"><div class="sk sk-circle" style="width:108px;height:108px;border-radius:50%"></div>' +
      '<div class="stat-meta">' + skeletonBlock(15, "60%") + skeletonBlock(12, "80%") + skeletonBlock(12, "45%") + "</div></div>";
  }
  return html + "</div>";
}
function skeletonBlock(h, w, extra) {
  return '<div class="sk" style="height:' + (h || 16) + "px;" + (w ? "width:" + w + ";" : "") + (extra || "") + '"></div>';
}
function skeletonTable(rows, cols, titleW) {
  rows = rows || 5; cols = cols || 5;
  let html = '<div class="card"><div class="sk" style="height:18px;width:' + (titleW || 180) + 'px;margin-bottom:18px"></div>';
  for (let r = 0; r < rows; r++) {
    html += '<div class="sk-row">';
    for (let c = 0; c < cols; c++) html += skeletonBlock(13, (60 + ((r + c) * 17) % 90) + "px");
    html += "</div>";
  }
  return html + "</div>";
}

/* 空状态(SVG) */
function emptyState(icon, text, sub) {
  return '<div class="empty-tip page-fade"><div class="empty-ico">' + I(icon || "inbox") + "</div>" +
    '<div class="empty-text">' + esc(text || "暂无数据") + "</div>" +
    (sub ? '<div class="empty-sub">' + esc(sub) + "</div>" : "") + "</div>";
}

/* ---------- Toast ---------- */
// 危险操作按钮的全局即时反馈:删除/卸载等按钮点击后立即转圈(实际执行常
// 有 1~3 秒 API 延迟,此前完全无反馈像没点上);完成或失败都会 toast,
// toast 出现时统一清场,15 秒兜底防泄漏
function clearBusyBtns() {
  document.querySelectorAll(".btn-busy").forEach((b) => {
    b.classList.remove("btn-busy");
    b.style.pointerEvents = "";
    const s = b.querySelector(".spin-sm");
    if (s) s.remove();
    delete b.dataset.busy;
  });
}
document.addEventListener(
  "click",
  (e) => {
    const b = e.target.closest(".btn");
    if (!b) return;
    const t = (b.textContent || "").trim();
    if (!/删除|卸载|清空|恢复|解绑|回滚|停用/.test(t)) return;
    if (b.dataset.busy) {
      // 已在执行中:拦截重复点击,防双发请求
      e.stopPropagation();
      e.preventDefault();
      return;
    }
    b.dataset.busy = "1";
    b.classList.add("btn-busy");
    b.style.pointerEvents = "none";
    b.insertAdjacentHTML("beforeend", ' <span class="spin-sm"></span>');
    setTimeout(clearBusyBtns, 15000);
  },
  true
);

function toast(msg, type = "success", ms = 2600, onclick) {
  clearBusyBtns();
  const wrap = qs("#toast-wrap");
  const el = document.createElement("div");
  el.className = "toast " + type;
  const icon = type === "success" ? I("play") : type === "error" ? I("close") : I("info");
  el.innerHTML = '<span class="toast-ico">' + icon + '</span><span>' + esc(msg) + "</span>";
  // 「任务[x]正在执行中」类提示自动变成可点击,直达该任务的实时日志
  const busy = typeof msg === "string" && msg.match(/任务\[(.+?)\]正在执行中/);
  if (busy || onclick) {
    el.classList.add("toast-link");
    el.title = "点击查看任务进度";
    el.onclick = async () => {
      el.remove();
      if (onclick) { onclick(); return; }
      try {
        const list = (await api("/tasks")) || [];
        const t = list.find((x) => x.name === busy[1] && x.status === "running");
        if (t) taskModal(t.id);
        else toast("该任务已结束,可在顶栏「后台任务」中查看结果", "info", 4000);
      } catch (e) { toast(e.message, "error"); }
    };
  }
  wrap.appendChild(el);
  setTimeout(() => { el.style.opacity = "0"; el.style.transition = "opacity .3s"; setTimeout(() => el.remove(), 320); }, ms);
}

/* ---------- Modal ---------- */
let _modalStack = [];
function modal({ title, body, footer, width = 540, onClose }) {
  clearBusyBtns(); // 弹窗打开本身就是反馈,清掉触发按钮上的转圈
  const mask = qs("#modal-mask");
  const box = qs("#modal-box");
  box.style.width = Math.min(width, window.innerWidth - 24) + "px";
  box.innerHTML =
    '<div class="modal-header"><h3>' + esc(title) + '</h3><span class="modal-actions">' +
    '<span class="modal-maxbtn" title="最大化 / 还原(双击标题栏同效)">' + I("maximize") + "</span>" +
    '<span class="modal-close">' + I("close") + "</span></span></div>" +
    '<div class="modal-body">' + (body || "") + "</div>" +
    (footer ? '<div class="modal-footer">' + footer + "</div>" : "");
  mask.style.display = "flex";
  const toggleMax = () => {
    box.classList.toggle("modal-max");
    // 尺寸变化后刷新 CodeMirror 等感知尺寸的组件
    box.querySelectorAll(".CodeMirror").forEach((el) => { if (el.CodeMirror) el.CodeMirror.refresh(); });
  };
  qs(".modal-maxbtn", box).onclick = toggleMax;
  qs(".modal-header h3", box).ondblclick = toggleMax;
  let vetoClose = null; // 返回 true 表示阻止本次关闭(未保存改动保护)
  const doClose = () => {
    mask.classList.add("closing");
    setTimeout(() => {
      mask.style.display = "none";
      mask.classList.remove("closing");
      box.innerHTML = "";
    }, 140);
    _modalStack = _modalStack.filter((m) => m !== close);
    if (onClose) onClose();
  };
  const close = () => {
    if (vetoClose && vetoClose()) return;
    doClose();
  };
  qs(".modal-close", box).onclick = close;
  // 遮罩点击不再关闭:填一半的表单/编辑内容因误触丢失的代价远大于多点一次 ×
  _modalStack.push(close);
  return { el: box, close, doClose, setVeto: (fn) => (vetoClose = fn) };
}
/* ---------- 独立弹层:可叠加在已打开的 modal 之上 ----------
   modal() 复用同一个 #modal-box,二次开弹窗会顶掉前一个的 DOM;
   确认/输入类弹窗(常从列表弹窗内触发)必须用独立浮层。 */
let _layerSeq = 0;
function openLayer({ title, body, footer, width = 480, onClose }) {
  _layerSeq++;
  const mask = document.createElement("div");
  mask.className = "modal-mask";
  mask.style.zIndex = String(2000 + _layerSeq * 10);
  mask.innerHTML =
    '<div class="modal-box" style="width:' + Math.min(width, window.innerWidth - 24) + 'px">' +
    '<div class="modal-header"><h3>' + esc(title) + '</h3><span class="modal-close">' + I("close") + "</span></div>" +
    '<div class="modal-body">' + (body || "") + "</div>" +
    (footer ? '<div class="modal-footer">' + footer + "</div>" : "") +
    "</div>";
  document.body.appendChild(mask);
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    mask.classList.add("closing");
    setTimeout(() => mask.remove(), 140);
    if (onClose) onClose();
  };
  qs(".modal-close", mask).onclick = close;
  // 遮罩点击不关闭(与 modal 一致,防误触丢数据)
  return { el: mask, close };
}

function confirmDlg(msg, opts = {}) {
  return new Promise((resolve) => {
    const danger = opts.danger !== false;
    // close() 会同步触发 onClose;必须先 resolve 结果,否则 null/false 覆盖真实选择(点确定等于取消)
    let done = false;
    const finish = (v) => { if (!done) { done = true; resolve(v); } };
    const m = openLayer({
      title: opts.title || "操作确认",
      width: 430,
      body: '<div class="confirm-body">' + msg + "</div>",
      footer:
        '<button class="btn btn-plain" data-act="no">取 消</button>' +
        '<button class="btn ' + (danger ? "btn-danger" : "btn-primary") + '" data-act="yes">确 定</button>',
      onClose: () => finish(false),
    });
    qs('[data-act="no"]', m.el).onclick = () => { finish(false); m.close(); };
    qs('[data-act="yes"]', m.el).onclick = () => { finish(true); m.close(); };
  });
}

/* prompt 风格弹窗:返回输入值或 null */
function promptDlg(title, fields) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (v) => { if (!done) { done = true; resolve(v); } }; // 同上:先 resolve 再 close
    const body = fields.map((f, i) =>
      '<div class="form-item ' + (f.required ? "required" : "") + '" style="margin-bottom:14px">' +
      "<label>" + esc(f.label) + "</label>" +
      (f.type === "textarea"
        ? '<textarea class="textarea" data-i="' + i + '" rows="4">' + esc(f.value || "") + "</textarea>"
        : '<input class="input" data-i="' + i + '" type="' + (f.type || "text") + '" value="' + esc(f.value || "") + '" placeholder="' + esc(f.placeholder || "") + '">') +
      "</div>"
    ).join("");
    const m = openLayer({
      title, width: 480, body,
      footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">确 定</button>',
      onClose: () => finish(null),
    });
    qs('[data-act="no"]', m.el).onclick = () => { finish(null); m.close(); };
    qs('[data-act="yes"]', m.el).onclick = () => {
      const out = {};
      fields.forEach((f, i) => { out[f.key] = qs('[data-i="' + i + '"]', m.el).value; });
      for (const f of fields) {
        if (f.required && !String(out[f.key] || "").trim()) { toast(f.label + " 不能为空", "error"); return; }
      }
      finish(out);
      m.close();
    };
  });
}

/* ---------- 任务进度弹窗(轮询后端任务日志) ---------- */
function taskModal(taskId, onDone) {
  // 必须用独立浮层:调用方常常刚 close 掉上一个 modal(),
  // 而 modal() 复用同一个 #modal-box 且 close 是 140ms 延迟清理,会把这个刚创建的任务弹窗清空
  let fired = false;
  const fire = (status) => { if (fired) return; fired = true; if (onDone) onDone(status); };
  const m = openLayer({
    title: "任务执行中",
    width: 680,
    onClose: async function() {
      // 任意关闭路径(后台运行/×/完成)都回传任务最新状态,页面据此刷新:
      // 此前「后台运行」不触发 onDone,页面不刷新导致依赖最新数据的表单(如
      // PHP 多版本端口)用到过期数据
      stopped = true;
      if (fired) return;
      try {
        const t = await api("/tasks/" + taskId);
        if (t.status === "failed") toast("任务失败: " + (t.error || ""), "error", 5000);
        fire(t.status);
      } catch (e) { fire("unknown"); }
    },
    body: '<div id="task-progress" style="display:none;margin-bottom:10px">' +
      '<div style="display:flex;justify-content:space-between;font-size:12.5px;margin-bottom:6px">' +
      '<span id="task-stage"></span><span class="mono" id="task-pct"></span></div>' +
      '<div style="height:8px;border-radius:6px;background:var(--line,#e5e7eb33);overflow:hidden">' +
      '<div id="task-bar" style="height:100%;width:0;border-radius:6px;background:var(--primary,#2563eb);transition:width .5s"></div></div></div>' +
      '<div class="log-view" id="task-log"></div>',
    footer: '<span class="help" style="margin-right:auto">关闭弹窗任务继续在后台运行,顶栏「后台任务」可随时查看</span>' +
      '<button class="btn btn-plain" data-act="bg">后台运行</button><button class="btn btn-primary" data-act="done" disabled>完 成</button>',
  });
  const logEl = qs("#task-log", m.el);
  const doneBtn = qs('[data-act="done"]', m.el);
  const barEl = qs("#task-bar", m.el);
  const stageEl = qs("#task-stage", m.el);
  const pctEl = qs("#task-pct", m.el);
  const wrapEl = qs("#task-progress", m.el);
  qs('[data-act="bg"]', m.el).onclick = () => m.close();
  doneBtn.onclick = () => m.close();
  let stopped = false;
  const poll = async () => {
    if (stopped) return;
    try {
      const t = await api("/tasks/" + taskId);
      logEl.textContent = t.log || "";
      logEl.scrollTop = logEl.scrollHeight;
      qs(".modal-header h3", m.el).textContent = "任务: " + t.name + (t.status === "running" ? "(执行中)" : t.status === "success" ? "(成功)" : "(失败)");
      // 结构化进度条(后端通过 taskx P(w) 汇报当前步/总步/阶段)
      if (t.progress && t.progress.total > 0) {
        wrapEl.style.display = "";
        const pct = Math.min(100, Math.round((t.progress.cur || 0) * 100 / t.progress.total));
        barEl.style.width = pct + "%";
        pctEl.textContent = t.progress.cur + " / " + t.progress.total + "  (" + pct + "%)";
        stageEl.textContent = t.progress.stage || "";
      }
      if (t.status !== "running") {
        doneBtn.disabled = false;
        if (t.progress && t.progress.total > 0) { barEl.style.width = "100%"; stageEl.textContent = t.status === "success" ? "全部完成" : "失败"; }
        if (t.status === "failed") toast("任务失败: " + (t.error || ""), "error", 5000);
        fire(t.status);
        return;
      }
    } catch (e) { /* ignore */ }
    setTimeout(poll, 1200);
  };
  poll();
  return m;
}

/* ---------- 后台任务中心(全部任务列表 + 实时日志入口) ---------- */
let _tcTimer = null;
function taskCenter() {
  const m = openLayer({
    title: "后台任务",
    width: 720,
    body: '<div id="tc-list"><div class="boot-loading" style="height:60px"><div class="spinner"></div></div></div>',
    footer: '<button class="btn btn-primary" data-act="close">关 闭</button>',
    onClose: () => { if (_tcTimer) { clearInterval(_tcTimer); _tcTimer = null; } },
  });
  qs('[data-act="close"]', m.el).onclick = m.close;
  const fmtDur = (a, b) => {
    const s = Math.max(0, Math.floor(((b ? new Date(b).getTime() : Date.now()) - new Date(a).getTime()) / 1000));
    return s < 60 ? s + " 秒" : Math.floor(s / 60) + " 分 " + (s % 60) + " 秒";
  };
  const fmtT = (t) => { const d = new Date(t); return isNaN(d) ? "-" : d.toLocaleString("zh-CN", { hour12: false }); };
  const render = async () => {
    let list = [];
    try { list = (await api("/tasks")) || []; } catch (e) { /* 弹窗已关等 */ }
    const box = qs("#tc-list", m.el);
    if (!box) { if (_tcTimer) { clearInterval(_tcTimer); _tcTimer = null; } return; }
    if (!list.length) { box.innerHTML = emptyState("inbox", "暂无后台任务", "安装应用 / 装扩展 / 备份等长任务会在这里显示进度"); return; }
    const stTag = (s) => s === "running" ? '<span class="tag tag-info"><span class="tc-dot"></span>执行中</span>' : s === "success" ? '<span class="tag tag-success">成功</span>' : '<span class="tag tag-danger">失败</span>';
    box.innerHTML = list.map((t) =>
      '<div class="tc-item' + (t.status === "running" ? " running" : "") + '">' +
      '<div class="tc-head"><b class="mono">' + esc(t.name) + "</b>" + stTag(t.status) + "</div>" +
      '<div class="tc-meta">开始 ' + fmtT(t.started_at) + " · 已用时 " + fmtDur(t.started_at, t.ended_at) +
      (t.error ? ' · <span style="color:var(--danger)">' + esc(t.error) + "</span>" : "") + "</div>" +
      '<pre class="tc-tail">' + esc((t.log_tail || "").split("\n").filter(Boolean).slice(-3).join("\n") || "(等待输出…)") + "</pre>" +
      '<button class="btn btn-sm btn-plain" data-id="' + esc(t.id) + '">' + I("eye") + " 查看完整日志</button></div>"
    ).join("");
    qsa(".tc-item button", box).forEach((b) => (b.onclick = () => taskModal(b.dataset.id)));
  };
  render();
  _tcTimer = setInterval(render, 3000);
}

/* ---------- 环形仪表 ---------- */
function ring(pct, label) {
  const size = 108, sw = 9, r = (size - sw) / 2 - 1, c = 2 * Math.PI * r;
  const p = Math.max(0, Math.min(100, Number(pct) || 0));
  const color = p >= 90 ? "#f56c6c" : p >= 75 ? "#e6a23c" : "#0066ff";
  return '<svg class="ring" width="' + size + '" height="' + size + '" viewBox="0 0 ' + size + " " + size + '">' +
    '<circle cx="' + size / 2 + '" cy="' + size / 2 + '" r="' + r + '" fill="none" stroke="#eef1f6" stroke-width="' + sw + '"/>' +
    '<circle cx="' + size / 2 + '" cy="' + size / 2 + '" r="' + r + '" fill="none" stroke="' + color + '" stroke-width="' + sw + '" stroke-linecap="round" stroke-dasharray="' + c + '" stroke-dashoffset="' + (c * (1 - p / 100)) + '" transform="rotate(-90 ' + size / 2 + " " + size / 2 + ')"/>' +
    '<text x="50%" y="49%" text-anchor="middle" dominant-baseline="middle" font-size="17" font-weight="600" fill="#303133">' + p.toFixed(1) + "%" + "</text>" +
    '<text x="50%" y="66%" text-anchor="middle" dominant-baseline="middle" font-size="10" fill="#909399">' + (label || "") + "</text></svg>";
}

/* ---------- 状态标签 ---------- */
function stateTag(on, onText, offText) {
  return on
    ? '<span class="tag tag-success"><span class="dot on"></span>' + (onText || "运行中") + "</span>"
    : '<span class="tag tag-info"><span class="dot off"></span>' + (offText || "已停止") + "</span>";
}

/* ---------- 通用表格构造 ---------- */
function tableHTML(cols, rows, emptyTip, emptyIcon) {
  if (!rows || !rows.length) return emptyState(emptyIcon || "inbox", emptyTip || "暂无数据");
  let html = '<div class="table-wrap"><table class="table"><thead><tr>';
  for (const c of cols) html += "<th" + (c.width ? ' style="width:' + c.width + '"' : "") + ">" + c.title + "</th>";
  html += "</tr></thead><tbody>";
  for (const row of rows) {
    html += "<tr>";
    for (const c of cols) html += "<td>" + (c.render(row) != null ? c.render(row) : "") + "</td>";
    html += "</tr>";
  }
  html += "</tbody></table></div>";
  return html;
}

/* 开关点击 */
function bindSwitch(sel, onChange) {
  qsa(sel).forEach((el) => {
    el.addEventListener("click", async () => {
      const target = el.classList.contains("on") ? false : true;
      el.classList.toggle("on", target);
      try { await onChange(el.dataset.id, target, el); }
      catch (e) { el.classList.toggle("on", !target); toast(e.message, "error"); }
    });
  });
}

/* 防抖 */
function debounce(fn, ms) {
  let t = null;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}
