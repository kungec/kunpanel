/* ============ 坤面板 - 页面渲染(骨架屏 + 渐入动效 + 全 SVG 图标) ============ */
"use strict";

const PAGES = {};
const _pageTimers = [];

function setPageTimer(fn, ms) { const id = setInterval(fn, ms); _pageTimers.push(id); return id; }
function clearPageTimers() { _pageTimers.forEach(clearInterval); _pageTimers.length = 0; }

/* ================= 概览 ================= */
PAGES.dashboard = {
  title: "概览",
  async render(el) {
    el.innerHTML = '<div class="kpi-strip">' + Array(6).fill('<div class="card kpi-card"><div class="sk" style="height:40px"></div></div>').join("") + '</div>' +
      '<div class="grid-2" style="margin-top:14px">' + skeletonTable(6, 3) + skeletonTable(6, 3) + "</div>";
    var results = await Promise.all([
      api("/dashboard"),
      api("/websites").catch(function() { return []; }),
      api("/db/instances").catch(function() { return []; }),
      api("/crons").catch(function() { return []; }),
      api("/uptime/monitors").catch(function() { return []; }),
      api("/ssl/certs").catch(function() { return []; }),
    ]);
    if (el.dataset.route !== "dashboard") return;
    var d = results[0], sites = results[1] || [], dbs = results[2] || [], crons = results[3] || [],
        ups = results[4] || [], certs = results[5] || [];
    var s = d.system, p = d.panel, dk = d.docker;
    var disk = s.disks && s.disks[0] ? s.disks[0] : { used: 0, total: 0, free: 0, usage_percent: 0 };
    var upDown = 0;
    ups.forEach(function(m) { if (m.enabled && !m.up) upDown++; });

    function kpi(icon, color, num, label, go) {
      return '<div class="card kpi-card card-hover" data-go="' + go + '"><span class="kpi-ico" style="color:' + color + '">' + I(icon) + "</span>" +
        '<div class="kpi-num"><b>' + num + "</b><span>" + label + "</span></div></div>";
    }
    var expiring = 0;
    certs.forEach(function(c) {
      if (!c.expire) return;
      var days = Math.round((new Date(c.expire) - new Date()) / 86400000);
      // shortlived 短期证书(IP 证书)约 6 天且自动续期,按半衰期判定临期
      if (days <= (c.profile === "shortlived" ? 3 : 30)) expiring++;
    });
    var memPct = s.mem_percent || 0, cpuPct = s.cpu_percent || 0;
    // 数据库 KPI = 各运行实例的业务库总数(与数据库页口径一致;databases 由后端统计)
    var dbTotal = dbs.reduce(function(a, r) { return a + (r.running ? r.databases || 0 : 0); }, 0);

    function bar(label, pct, sub, color, liveId) {
      var pctCls = pct >= 90 ? "danger" : pct >= 75 ? "warn" : "";
      return '<div class="mon-row"><div class="mon-head"><span>' + label + (liveId ? ' <b id="' + liveId + '">' + pct.toFixed(1) + "%</b>" : "<b>" + pct.toFixed(1) + "%</b>") + "</span>" +
        '<span class="mon-sub">' + sub + "</span></div>" +
        '<div class="mon-bar"><i class="' + pctCls + '"' + (liveId ? ' id="' + liveId + '-bar"' : "") + ' style="width:' + Math.min(100, pct) + '%"></i></div></div>';
    }
    var diskRows = (s.disks || []).slice(0, 3).map(function(dk2, i) {
      return bar("磁盘 " + dk2.path, dk2.usage_percent || 0, "已用 " + fmtBytes(dk2.used) + " / " + fmtBytes(dk2.total), "", i === 0 ? "live-disk" : "");
    }).join("");

    var html = '<div class="kpi-strip">' +
      kpi("globe", "#38bdf8", sites.length, "网站", "websites") +
      kpi("database", "#60a5fa", dbTotal, "数据库", "databases") +
      kpi("cube", "#0ea5e9", dk.running ? dk.runningCount + "/" + dk.containers : "-", "容器", "docker") +
      kpi("clock", "#fb923c", crons.length, "计划任务", "cron") +
      kpi("key", "#a78bfa", certs.length + (expiring ? '<i class="kpi-warn">' + expiring + " 项临期</i>" : ""), "SSL 证书", "websites") +
      kpi("pulse", upDown ? "#f56c6c" : "#4ade80", ups.length ? (ups.length - upDown) + "/" + ups.length : "-", "拨测正常", "monitorhub") +
      "</div>" +

      '<div class="dash-grid" style="margin-top:14px">' +
      '<div class="card card-hover"><div class="card-title">系统监控<span class="mon-net">' +
      '<span class="net-line down">' + I("netDown") + ' <b id="live-rx">' + fmtSpeed(s.net_rx_speed) + '</b></span>' +
      '<span class="net-line up">' + I("netUp") + ' <b id="live-tx">' + fmtSpeed(s.net_tx_speed) + "</b></span></span></div>" +
      bar("CPU 使用率", cpuPct, (s.cpu_cores || "-") + " 核 · " + esc((s.cpu_model || "-").split("@")[0].trim().slice(0, 24)), "", "live-cpu") +
      bar("内存使用", memPct, fmtBytes(s.mem_used) + " / " + fmtBytes(s.mem_total), "", "live-mem") +
      bar("系统负载", Math.min(100, (s.load1 / Math.max(1, s.cpu_cores)) * 100), "1/5/15 分钟: " + s.load1 + " / " + s.load5 + " / " + s.load15, "", "live-load") +
      diskRows +
      "</div>" +

      '<div class="card card-hover"><div class="card-title">服务器信息</div><table class="info-table">' +
      "<tr><td>主机名</td><td>" + esc(s.hostname) + "</td></tr>" +
      "<tr><td>操作系统</td><td>" + esc(s.platform) + " " + esc(s.platform_v) + " (" + esc(s.os) + ")</td></tr>" +
      "<tr><td>内核 / 架构</td><td>" + esc(s.kernel) + ' <span class="help">· ' + esc(s.arch) + "</span></td></tr>" +
      "<tr><td>IP 地址</td><td><span class=\"help\">内网</span> <span class=\"mono\">" + esc(s.ip) + "</span>" +
      (s.public_ip ? ' &nbsp;<span class="help">公网</span> <b class="mono">' + esc(s.public_ip) + "</b>" : ' &nbsp;<span class="help">公网获取中…</span>') + "</td></tr>" +
      '<tr><td>累计流量</td><td>收 ' + fmtBytes(s.net_rx_total) + " / 发 " + fmtBytes(s.net_tx_total) + "</td></tr>" +
      "</table></div></div>" +

      '<div class="grid-2" style="margin-top:14px">' +
      '<div class="card card-hover"><div class="card-title">面板信息</div><table class="info-table">' +
      "<tr><td>面板版本</td><td>" + esc(p.version) + "</td></tr>" +
      "<tr><td>启动时间</td><td>" + esc(p.started) + "</td></tr>" +
      "<tr><td>运行时长</td><td>" + fmtUptime(s.uptime) + "</td></tr>" +
      "<tr><td>容器状态</td><td>" + (dk.installed
        ? stateTag(dk.running, "Docker " + esc(dk.version) + " · " + dk.runningCount + "/" + dk.containers + " 运行中", "Docker 已安装未运行")
        : '<span class="tag tag-warning">未安装 Docker</span>') + "</td></tr>" +
      '<tr><td id="ssl-alert-td">证书状态</td><td id="ssl-alert-box"><span class="help">检查中...</span></td></tr>' +
      "</table></div>" +

      '<div class="card card-hover"><div class="card-title">快捷入口</div><div class="quick-grid">' +
      [["globe", "网站", "websites", "#38bdf8"], ["store", "应用商店", "apps", "#a78bfa"], ["cube", "容器", "docker", "#0ea5e9"],
       ["clock", "计划任务", "cron", "#fb923c"], ["folder", "文件", "files", "#4ade80"], ["shield", "安全中心", "securityhub", "#f87171"],
       ["pulse", "监控中心", "monitorhub", "#f59e0b"], ["terminal", "终端", "terminal", "#64748b"]]
        .map(function(x) { return '<div class="quick-item" data-go="' + x[2] + '"><span class="qi-icon" style="color:' + x[3] + '">' + I(x[0]) + "</span><span>" + x[1] + "</span></div>"; }).join("") +
      "</div></div></div>";

    el.innerHTML = html;
    el.querySelectorAll("[data-go]").forEach(function(n) {
      n.onclick = function() { location.hash = "#/" + n.getAttribute("data-go"); };
    });

    // 证书状态
    (function() {
      var box = document.getElementById("ssl-alert-box");
      if (!box) return;
      var warns = [];
      certs.forEach(function(c) {
        if (!c.expire) return;
        var days = Math.round((new Date(c.expire) - new Date()) / 86400000);
        if (days < 0) warns.push('<span style="color:var(--danger)">' + esc(c.name) + " 已过期</span>");
        else if (days < 30) warns.push('<span style="color:var(--warning)">' + esc(c.name) + " 剩余 " + days + " 天</span>");
      });
      box.innerHTML = warns.length ? warns.join("<br>") : '<span style="color:var(--success)">正常(LE 证书自动续期)</span>';
    })();

    // 24h 趋势
    fetch("/api/v1/mon/history?n=180", { headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY) } })
      .then(function(r) { return r.json() }).then(function(hist) {
        if (el.dataset.route !== "dashboard") return;
        hist = hist || [];
        if (hist.length < 2) return;
        function spark(data, label, color) {
          var w = 200, h = 50, max = Math.max.apply(null, data) || 1;
          var pts = data.map(function(v, i) { return (i/(data.length-1)*w).toFixed(1)+","+(h-v/max*(h-4)-2).toFixed(1); }).join(" ");
          var last = data[data.length-1];
          return '<div><div style="font-size:12px;color:var(--text-3);margin-bottom:4px">'+esc(label)+' <b style="color:'+color+'">'+(last<10?last.toFixed(1):Math.round(last))+"</b></div>" +
            '<svg width="100%" height="'+h+'" viewBox="0 0 '+w+" "+h+'" preserveAspectRatio="none"><polyline points="'+pts+'" fill="none" stroke="'+color+'" stroke-width="1.5"/></svg></div>';
        }
        var grid = el.querySelector(".dash-grid");
        if (!grid) return;
        grid.insertAdjacentHTML("afterend", '<div class="card card-hover" style="margin-top:14px"><div class="card-title">24 小时监控趋势</div><div style="display:grid;grid-template-columns:repeat(3,1fr);gap:16px">' +
          spark(hist.map(function(p2){return p2.cpu}), "CPU %", "#0066ff") +
          spark(hist.map(function(p2){return p2.mem}), "内存 %", "#22c55e") +
          spark(hist.map(function(p2){return Math.min(p2.net_rx/1024, 9999)}), "下行 KB/s", "#e6a23c") +
          spark(hist.map(function(p2){return Math.min(p2.net_tx/1024, 9999)}), "上行 KB/s", "#f59e0b") +
          spark(hist.map(function(p2){return Math.min((p2.disk_r||0)/1024, 9999)}), "磁盘读 KB/s", "#8b5cf6") +
          spark(hist.map(function(p2){return Math.min((p2.disk_w||0)/1024, 9999)}), "磁盘写 KB/s", "#ec4899") +
          "</div></div>");
      });

    // 实时字段局部刷新(不重建页面,避免闪烁)
    setPageTimer(async function() {
      if (el.dataset.route !== "dashboard" || document.visibilityState === "hidden") return;
      if (window.__dashFetching) return;
      window.__dashFetching = true;
      try {
        var d2 = await api("/dashboard");
        if (el.dataset.route !== "dashboard") return;
        var s2 = d2.system;
        var pairs = [["live-cpu", (s2.cpu_percent || 0).toFixed(1) + "%"], ["live-mem", (s2.mem_percent || 0).toFixed(1) + "%"],
          ["live-load", Math.min(100, (s2.load1 / Math.max(1, s2.cpu_cores)) * 100).toFixed(1) + "%"],
          ["live-rx", fmtSpeed(s2.net_rx_speed)], ["live-tx", fmtSpeed(s2.net_tx_speed)]];
        pairs.forEach(function(pr) {
          var n = document.getElementById(pr[0]);
          if (n) n.textContent = pr[1];
        });
        [["live-cpu", s2.cpu_percent || 0], ["live-mem", s2.mem_percent || 0],
         ["live-load", Math.min(100, (s2.load1 / Math.max(1, s2.cpu_cores)) * 100)],
         ["live-disk", s2.disks && s2.disks[0] ? s2.disks[0].usage_percent : 0]].forEach(function(pr) {
          var b = document.getElementById(pr[0] + "-bar");
          if (b) {
            b.style.width = Math.min(100, pr[1]) + "%";
            b.className = pr[1] >= 90 ? "danger" : pr[1] >= 75 ? "warn" : "";
          }
        });
      } catch (e) {}
      finally { window.__dashFetching = false; }
    }, 5000);
  },
};

/* ================= 网站 ================= */
PAGES.websites = {
  title: "网站",
  async render(el) {
    var self = this;
    el.innerHTML = skeletonTable(4, 5);
    let data = [], nginx = { mode: "none" }, certs = [];
    try {
      [data, nginx, certs] = await Promise.all([api("/websites"), api("/websites/nginx"), api("/ssl/certs").catch(() => [])]);
    } catch (e) { el.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    data = data || [];
    certs = certs || [];
    // PHP 实例键 → 版本映射(站点列表显示所用版本)
    const phpVerMap = {};
    try {
      const phps = (await api("/apps/instances").catch(() => [])) || [];
      phps.forEach((x) => { if (x.key === "php" || /^php[0-9]+$/.test(x.key)) phpVerMap[x.key] = (x.params && x.params.Version) || x.key.replace("php", ""); });
    } catch (e) {}
    window.__phpVerMap = phpVerMap;
    const certMap = {};
    certs.forEach((c) => (certMap[c.id] = c));
    const daysLeft = (c) => {
      if (!c || !c.expire) return null;
      return Math.round((new Date(c.expire) - new Date()) / 86400000);
    };

    const nginxAlert = nginx.mode === "none"
      ? '<div class="alert alert-warning page-fade"><span class="alert-ico">' + I("warn") + '</span>未检测到 Nginx / OpenResty,网站功能依赖它。<button class="btn btn-sm btn-primary" id="btn-install-openresty">去应用商店安装</button></div>'
      : '<div class="alert alert-info page-fade"><span class="alert-ico">' + I("info") + "</span>当前使用 " + (nginx.mode === "container" ? (nginx.target === "kun-nginx" ? "面板管理的 Nginx 容器 (kun-nginx)" : "面板管理的 OpenResty 容器 (kun-openresty)") : "宿主机 Nginx") + "</div>";

    el.innerHTML = nginxAlert + `
      <div class="card page-fade" style="animation-delay:.08s">
        <div class="card-title">网站列表<div style="display:flex;gap:8px;align-items:center">
            <button class="btn btn-sm btn-plain" id="site-btmig" style="display:none">${I("shuffle")} 宝塔迁移</button><button class="btn btn-sm btn-plain" id="site-p1mig">${I("shuffle")} 1Panel 迁移</button>
            <button class="btn btn-sm btn-plain" id="site-global">${I("gear")} 全局设置</button>
            <button class="btn btn-sm btn-plain" id="site-logs">${I("logs")} 站点日志</button>
            <button class="btn btn-sm btn-plain" id="site-ssl">${I("key")} SSL 证书</button><button class="btn btn-sm btn-plain" id="site-stats">${I("pulse")} 访问统计</button><button class="btn btn-sm btn-danger-plain" id="site-batch-del" style="display:none">${I("trash")} 批量删除</button><button class="btn btn-sm btn-primary" id="btn-add-site">${I("plus")} 创建网站</button></div></div>
        <div id="site-table">${tableHTML([
          { title: '<input type="checkbox" id="site-sel-all" title="全选">', width: "36px", render: (r) => '<input type="checkbox" class="site-sel" data-id="' + r.id + '">' },
          { title: "域名", render: (r) => '<span class="ellipsis" title="' + esc(r.domain) + '">' + esc(r.domain) + "</span>" },
          { title: "类型", width: "100px", render: (r) => (r.type === "proxy" ? '<span class="tag tag-primary">反向代理</span>' : r.type === "php" ? '<span class="tag tag-purple">PHP 站点</span>' : '<span class="tag tag-success">静态站点</span>') },
          { title: "端口", width: "70px", render: (r) => r.port },
          { title: "目标", render: (r) => esc(r.proxy_to || r.root || "-") },
          { title: "状态", width: "110px", render: (r) => '<span class="switch ' + (r.enabled ? "on" : "") + '" data-id="' + r.id + '"></span>' },
          { title: "HTTPS", width: "150px", render: (r) => {
              if (!r.ssl_cert_id) return '<span class="tag tag-info">未启用</span>';
              const c = certMap[r.ssl_cert_id];
              if (!c) return '<span class="tag tag-warning">证书缺失</span>';
              const days = daysLeft(c);
              if (days === null) return '<span class="tag tag-success">已启用</span>';
              if (days < 0) return '<span class="tag tag-danger">已过期</span>';
              if (days < 30) return '<span class="tag tag-warning">' + days + " 天后到期</span>";
              return '<span class="tag tag-success">' + days + " 天</span>";
            } },
          { title: "创建时间", width: "110px", render: (r) => { var d = new Date(r.created_at); return isNaN(d) ? "-" : (d.getMonth()+1 + "/" + d.getDate() + " " + String(d.getHours()).padStart(2,"0") + ":" + String(d.getMinutes()).padStart(2,"0")); } },
          { title: "操作", width: "330px", render: (r) =>
            '<div class="ops"><button class="btn btn-xs btn-primary" data-files="' + r.id + '" title="管理网站文件">' + I("folderOpen") + ' 文件</button>' +
            '<button class="btn btn-xs btn-plain" data-visit="' + r.id + '">访问</button>' +
            '<button class="btn btn-xs btn-plain" data-ssl="' + r.id + '">HTTPS</button>' +
            '<button class="btn btn-xs btn-plain" data-edit="' + r.id + '">设置</button>' +
            '<button class="btn btn-xs btn-plain" data-backup="' + r.id + '">备份</button>' +
            '<button class="btn btn-xs btn-danger-plain" data-del="' + r.id + '">删除</button></div>' },
        ], data, "还没有网站,点击右上角创建", "globe")}</div>
      </div>`;

    bindSwitch("#site-table .switch", async (id, on) => {
      await api("/websites/" + id + "/toggle", { method: "POST", body: { enabled: on } });
      toast(on ? "已启用" : "已禁用");
    });
    // 全局设置弹窗
    qs("#site-p1mig", el).onclick = () => this.p1MigrateDialog(el);
    qs("#site-global", el).onclick = () => this.globalDialog(el);
    // 宝塔迁移入口(检测到宝塔环境才显示)
    api("/migration/bt/scan").then(function(res) {
        if (res && res.found) {
            var b = qs("#site-btmig", el);
            if (b) { b.style.display = ""; b.onclick = () => self.btMigrateDialog(res, el); }
        }
    }).catch(function() {});
    const site = (id) => data.find((x) => x.id === id);
    // 批量删除:勾选 → 按钮浮现 → 确认后一次请求(服务端单次 reload)
    const syncBatchBtn = () => {
      const n = qsa(".site-sel", el).filter((c) => c.checked).length;
      qs("#site-batch-del", el).style.display = n ? "" : "none";
      // 必须用 innerHTML:textContent 会把 SVG 图标渲染成原始字符串
      qs("#site-batch-del", el).innerHTML = I("trash") + " 批量删除(" + n + ")";
    };
    const selAll = qs("#site-sel-all", el);
    if (selAll) {
      selAll.onclick = () => qsa(".site-sel", el).forEach((c) => (c.checked = selAll.checked));
      qsa(".site-sel", el).forEach((c) => (c.onclick = syncBatchBtn));
    }
    qs("#site-batch-del", el).onclick = async () => {
      const ids = qsa(".site-sel", el).filter((c) => c.checked).map((c) => c.getAttribute("data-id"));
      if (!ids.length) return;
      const domains = ids.map((id) => { const s = site(id); return s ? s.domain.split(" ")[0] : id; });
      if (!(await confirmDlg("确定删除选中的 <b>" + ids.length + "</b> 个网站吗?<br><span style='font-size:12px;color:var(--text-3)'>" + esc(domains.join("、")) + "<br>网站根目录数据将保留。</span>", { danger: true }))) return;
      try {
        const r = await api("/websites/batch-delete", { method: "POST", body: { ids: ids } });
        toast("已删除 " + (r.deleted || 0) + " 个网站" + (r.warning ? "(" + r.warning + ")" : ""), r.warning ? "info" : "success", 6000);
        this.render(el);
      } catch (e) { toast(e.message, "error"); }
    };
    qsa("[data-del]", el).forEach((b) => (b.onclick = async () => {
      if (await confirmDlg("确定删除网站 <b>" + esc(site(b.dataset.del).domain) + "</b> 吗?<br>网站根目录数据将保留。")) {
        try { await api("/websites/" + b.dataset.del, { method: "DELETE" }); toast("已删除"); this.render(el); }
        catch (e) { toast(e.message, "error"); }
      }
    }));
    qsa("[data-files]", el).forEach((b) => (b.onclick = function() {
      var st = site(b.dataset.files);
      location.hash = "#/files?path=" + encodeURIComponent(st.root) + "&from=%23%2Fwebsites";
    }));
    qsa("[data-ssl]", el).forEach((b) => (b.onclick = () => this.sslDialog(site(b.dataset.ssl), el)));
    qsa("[data-edit]", el).forEach((b) => (b.onclick = () => this.settingsDialog(site(b.dataset.edit), el)));
    qsa("[data-backup]", el).forEach((b) => (b.onclick = () => this.siteBackupDialog(site(b.dataset.backup), el)));
    qsa("[data-visit]", el).forEach((b) => (b.onclick = () => {
      const s = site(b.dataset.visit);
      window.open("http://" + s.domain.split(" ")[0] + (s.port !== 80 ? ":" + s.port : ""), "_blank");
    }));
    qs("#btn-add-site", el).onclick = () => this.addDialog(el);
    qs("#site-logs", el).onclick = () => this.logsDialog();
    qs("#site-ssl", el).onclick = () => this.sslManagerDialog(el);
    qs("#site-stats", el).onclick = () => this.statsDialog();
    const io = qs("#btn-install-openresty", el);
    if (io) io.onclick = () => { location.hash = "#/apps"; };
  },
  /* SSL 证书管理(全局,独立于站点) */
  async sslManagerDialog(el) {
    var m = modal({
      title: "SSL 证书管理", width: 760,
      body: '<div id="ssl-mgr-body">' + skeletonTable(4, 4) + "</div>",
      footer: '<button class="btn btn-plain" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    var body = qs("#ssl-mgr-body", m.el); // 修复:此前误写 #am-body(从 PHP 管理弹窗复制的残留),写入 null 静默失败,弹窗永远停在骨架屏
    var self = this;
    var origAmClose = m.close;
    m.close = function() {
      if (self._amPollTimer) { clearInterval(self._amPollTimer); self._amPollTimer = null; }
      origAmClose();
    };

    async function load() {
      var certs = [], sites = [];
      try {
        var rs = await Promise.all([api("/ssl/certs"), api("/websites").catch(function() { return []; })]);
        certs = rs[0] || []; sites = rs[1] || [];
      } catch (e) { body.innerHTML = emptyState("warn", "加载失败", e.message); return; }
      var bound = {};
      sites.forEach(function(s) { if (s.ssl_cert_id) bound[s.ssl_cert_id] = s.domain; });
      var rows = certs.map(function(c) {
        var days = c.expire ? Math.round((new Date(c.expire) - new Date()) / 86400000) : null;
        var due = c.profile === "shortlived" ? 3 : 30;
        var expTag = days === null ? "" : days < 0 ? '<span class="tag tag-danger">已过期</span>'
          : days <= due ? '<span class="tag tag-warning">剩 ' + days + " 天</span>"
          : '<span class="tag tag-success">剩 ' + days + " 天</span>";
        return "<tr><td><b>" + esc(c.name) + "</b>" + (c.profile === "shortlived" ? '<span class="tag tag-info" style="margin-left:6px" title="Let\'s Encrypt IP 证书,6 天有效期,自动续期">短期</span>' : "") + "</td><td class=\"mono\">" + esc(c.domains) + "</td>" +
          "<td>" + esc(c.expire || "-") + "</td><td>" + expTag + "</td>" +
          "<td>" + (bound[c.id] ? esc(bound[c.id]) : '<span class="help" style="margin:0">未绑定</span>') + "</td>" +
          '<td><div class="ops"><button class="btn btn-xs btn-danger-plain" data-delcert="' + esc(c.id) + '" data-name="' + esc(c.name) + '">删除</button></div></td></tr>';
      }).join("");
      var certRows = certs.map(function(c) {
        var days = c.expire ? Math.round((new Date(c.expire) - new Date()) / 86400000) : null;
        var due = c.profile === "shortlived" ? 3 : 30;
        var expTag = days === null ? '<span class="tag tag-info">-</span>' : days < 0 ? '<span class="tag tag-danger">已过期</span>'
          : days <= due ? '<span class="tag tag-warning">剩 ' + days + " 天</span>"
          : '<span class="tag tag-success">剩 ' + days + " 天</span>";
        return '<tr><td><input type="checkbox" class="cert-sel" data-id="' + esc(c.id) + '"></td><td><b>' + esc(c.name) + '</b>' + (c.profile === "shortlived" ? '<span class="tag tag-info" style="margin-left:6px" title="Let\'s Encrypt IP 证书,6 天有效期,自动续期">短期</span>' : "") + '<span class="tag tag-purple" style="margin-left:6px">SSL</span></td>' +
          '<td class="mono">' + esc(c.domains) + "</td><td>" + esc(c.expire || "-") + "</td><td>" + expTag + "</td>" +
          "<td>" + (bound[c.id] ? '<span class="mono">' + esc(bound[c.id]) + "</span>" : '<span class="help" style="margin:0">未绑定</span>') + "</td>" +
          '<td><button class="btn btn-xs btn-danger-plain" data-delcert="' + esc(c.id) + '" data-name="' + esc(c.name) + '">删除</button></td></tr>';
      }).join("");
      body.innerHTML =
        '<div class="ssl-grid">' +
          '<div class="ssl-card"><div class="ssl-card-title">' + I("upload") + ' 上传证书(PEM)</div>' +
            '<input class="input" id="ssl-up-name" placeholder="证书名称,如 demo.com">' +
            '<textarea class="textarea mono" id="ssl-up-cert" rows="4" placeholder="证书内容(BEGIN CERTIFICATE ...)"></textarea>' +
            '<textarea class="textarea mono" id="ssl-up-key" rows="3" placeholder="私钥内容(BEGIN PRIVATE KEY ...)"></textarea>' +
            '<button class="btn btn-primary btn-sm" id="ssl-up-btn" style="margin-top:10px;width:100%">上传证书</button>' +
          '</div>' +
          '<div class="ssl-card"><div class="ssl-card-title">' + I("key") + ' 自签证书</div>' +
            '<p class="help" style="margin:4px 0 8px">一键生成本地测试证书(10 年)。浏览器会提示不安全,生产环境请上传正式证书或在站点 HTTPS 中申请 Let\'s Encrypt。</p>' +
            '<input class="input" id="ssl-ss-domain" placeholder="域名,如 demo.com">' +
            '<button class="btn btn-sm btn-plain" id="ssl-ss-btn" style="margin-top:10px;width:100%">生成自签证书(10 年)</button>' +
            '<div class="help" style="margin-top:10px">生成的证书同样出现在下方列表,可在各站点「HTTPS」设置中绑定。</div>' +
          '</div>' +
        '</div>' +
        '<div class="ssl-card" style="margin-top:16px"><div class="ssl-card-title">' + I("shield") + ' 证书列表(' + certs.length + ')<span style="margin-left:auto"></span><button class="btn btn-sm btn-danger-plain" id="cert-batch-del" style="display:none">批量删除</button></div>' +
        (certs.length
          ? '<div class="table-wrap"><table class="table"><thead><tr><th style="width:36px"><input type="checkbox" id="cert-sel-all" title="全选"></th><th>名称</th><th>域名</th><th style="width:110px">到期</th><th style="width:120px">状态</th><th style="width:160px">绑定站点</th><th style="width:70px">操作</th></tr></thead><tbody>' + certRows + "</tbody></table></div>"
          : '<div class="ssl-empty">' + I("key") + '<p>暂无证书</p><p>上传 PEM 证书或生成自签证书后,可在各站点的「HTTPS」设置中绑定</p></div>') +
        '</div>';
      // 批量删除:被站点占用的证书逐项报错,其余照删
      var selAllC = qs("#cert-sel-all", body);
      if (selAllC) {
        selAllC.onclick = function() { qsa(".cert-sel", body).forEach(function(c) { c.checked = selAllC.checked; }); syncCertBatch(); };
        qsa(".cert-sel", body).forEach(function(c) { c.onclick = syncCertBatch; });
      }
      function syncCertBatch() {
        var n = qsa(".cert-sel", body).filter(function(c) { return c.checked; }).length;
        var btn = qs("#cert-batch-del", body);
        btn.style.display = n ? "" : "none";
        btn.textContent = "批量删除(" + n + ")";
      }
      qs("#cert-batch-del", body).onclick = async function() {
        var ids = qsa(".cert-sel", body).filter(function(c) { return c.checked; }).map(function(c) { return c.getAttribute("data-id"); });
        if (!ids.length) return;
        var names = ids.map(function(id) { var c = certs.find(function(x) { return x.id === id; }); return c ? c.name : id; });
        if (!(await confirmDlg("确定删除选中的 <b>" + ids.length + "</b> 张证书吗?<br><span style='font-size:12px;color:var(--text-3)'>" + esc(names.join("、")) + "<br>被站点绑定的证书会删除失败并逐项提示。</span>", { danger: true }))) return;
        try {
          var r = await api("/ssl/certs/batch-delete", { method: "POST", body: { ids: ids } });
          var fails = Object.keys(r.failed || {});
          if (fails.length) {
            toast("已删除 " + (r.deleted || 0) + " 张;" + fails.length + " 张失败:" + esc(fails.map(function(id) { return (certs.find(function(x) { return x.id === id; }) || {}).name || id; }).join("、")), "error", 8000);
          } else {
            toast("已删除 " + (r.deleted || 0) + " 张证书");
          }
          load();
        } catch (e) { toast(e.message, "error"); }
      };
      qs("#ssl-up-btn", body).onclick = async function() {
        var name = qs("#ssl-up-name", body).value.trim(),
            cert = qs("#ssl-up-cert", body).value.trim(),
            key = qs("#ssl-up-key", body).value.trim();
        if (!name || !cert || !key) { toast("请填写名称、证书和私钥", "error"); return; }
        try {
          await api("/ssl/upload", { method: "POST", body: { name: name, cert: cert, key: key } });
          toast("证书上传成功");
          load();
        } catch (e) { toast(e.message, "error"); }
      };
      qs("#ssl-ss-btn", body).onclick = async function() {
        var d = qs("#ssl-ss-domain", body).value.trim();
        if (!d) { toast("请填写域名", "error"); return; }
        try {
          await api("/ssl/selfsign", { method: "POST", body: { domain: d, days: 3650 } });
          toast("自签证书已生成");
          load();
        } catch (e) { toast(e.message, "error"); }
      };
      qsa("[data-delcert]", body).forEach(function(b) {
        b.onclick = async function() {
          if (!(await confirmDlg("确定删除证书 <b>" + esc(b.getAttribute("data-name")) + "</b> 吗?", { danger: true }))) return;
          try { await api("/ssl/certs/" + b.getAttribute("data-delcert"), { method: "DELETE" }); toast("已删除"); load(); }
          catch (e) { toast(e.message, "error", 5000); }
        };
      });
    }
    load();
  },
  /* 宝塔面板一键迁移向导 */
  /* 1Panel 迁移(独立于宝塔迁移) */
  p1MigrateDialog(el) {
    var self = this;
    var m = modal({
      title: "1Panel 迁移 ⇄ 坤面板", width: 880,
      body: '<div class="hub-tabs" style="margin-bottom:14px">' +
        '<div class="hub-tab active" data-dir="p1in">1Panel → 坤面板(本机)</div>' +
        '<div class="hub-tab" data-dir="p1remote">远程 1Panel → 本面板(输入 IP 密码)</div>' +
        '<div class="hub-tab" data-dir="p1out">坤面板 → 1Panel</div></div>' +
        '<div id="p1mig-body"><div class="boot-loading" style="height:80px"><div class="spinner"></div></div></div>',
      footer: '<span class="help" style="margin-right:auto">迁移不动 1Panel 原有站点与数据,可随时回退</span>' +
        '<button class="btn btn-plain" data-act="no">取 消</button>' +
        '<button class="btn btn-primary" data-act="go" style="display:none">开始迁移</button>',
    });
    var body = qs("#p1mig-body", m.el);
    var goBtn = qs('[data-act="go"]', m.el);
    qs('[data-act="no"]', m.el).onclick = m.close;

    var p1cred = { host: "", port: 22, user: "root", pass: "", fingerprint: "" };

    function switchP1Dir(dir) {
      qsa(".hub-tab", m.el).forEach(function(x) { x.classList.toggle("active", x.getAttribute("data-dir") === dir); });
      if (dir === "p1in") { drawP1In(); return; }
      if (dir === "p1remote") { drawP1Remote(); return; }
      if (dir === "p1out") { drawP1Out(); return; }
    }
    qsa(".hub-tab", m.el).forEach(function(t) {
      t.onclick = function() { switchP1Dir(t.getAttribute("data-dir")); };
    });

    /* ---------- 方向四:1Panel → 坤面板(本机) ---------- */
    function drawP1In() {
      goBtn.style.display = "none";
      body.innerHTML = '<div class="boot-loading" style="height:80px"><div class="spinner"></div><p>读取 1Panel 信息...</p></div>';
      api("/migration/p1/scan").then(function(res) {
        if (!res.found) {
          body.innerHTML = '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + '</span>未检测到本机 1Panel(需已安装在本服务器 /opt/1panel)。1Panel 与本面板不在同一台机器时暂不支持远程迁移。</div>';
          return;
        }
        goBtn.textContent = "开始迁移";
        goBtn.style.display = "";
        var rows = (res.sites || []).map(function(s2) {
          var typeTxt = s2.type === "php" ? "PHP" + (s2.fpm_unix ? "(unix socket)" : "") : s2.type === "proxy" ? "反向代理" : "静态";
          var warn = s2.warnings ? '<div class="help" style="color:var(--warning)">' + esc(s2.warnings) + "</div>" : "";
          return '<tr><td><label class="radio-line"><input type="checkbox" checked data-p1site="' + esc(s2.domain) + '"> <b>' + esc(s2.domain) + "</b>" + warn + "</label></td>" +
            '<td><span class="tag tag-info">' + typeTxt + "</span>" + (s2.ssl ? '<span class="tag tag-success">SSL</span>' : "") + "</td>" +
            '<td class="mono fm-ellipsis" style="max-width:200px" title="' + esc(s2.root || s2.proxy_to || "") + '">' + esc(s2.root || s2.proxy_to || "-") + "</td></tr>";
        }).join("");
        var dbChecks = (res.dbs || []).map(function(d0) {
          return '<label class="radio-line" style="display:inline-flex;margin:2px 18px 2px 0"><input type="checkbox" data-p1db="' + esc(d0.name) + '"> <span class="mono">' + esc(d0.name) + "</span>" + (d0.host ? '<span class="help" style="margin:0 0 0 6px">' + esc(d0.host) + "</span>" : "") + "</label>";
        }).join("");
        body.innerHTML =
          '<div class="alert alert-info page-fade"><span class="alert-ico">' + I("info") + "</span>" +
          "迁移 1Panel 站点的<b>域名、目录/反代目标、SSL 证书</b>;静态站直接服务原目录,PHP 站按 fastcgi 上游迁移(unix socket 上游会明确提示),数据库从 1Panel MySQL 容器导出导入。" +
          "迁移不动 1Panel 原站点与数据,可随时回退。</div>" +
          '<div class="settings-section"><div class="settings-section-title">站点(' + (res.sites || []).length + ')<span class="help" style="margin:0">勾选要迁移的站点</span></div>' +
          '<div class="table-wrap"><table class="table"><thead><tr><th>域名</th><th>类型</th><th>根目录/目标</th></tr></thead><tbody>' + (rows || '<tr><td colspan=3 class=help>未检测到站点</td></tr>') + "</tbody></table></div></div>" +
          '<div class="settings-section"><div class="settings-section-title">数据库(' + (res.dbs || []).length + ')<span class="help" style="margin:0">默认不迁移;勾选则从 1Panel MySQL 容器导出并导入面板 MySQL,之后站点需改连接串</span></div>' +
          (dbChecks || '<div class="help">1Panel 暂无 MySQL 数据库</div>') + "</div>" +
          ((res.warnings || []).length ? '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + "</span>" + res.warnings.map(function(x) { return esc(x); }).join("<br>") + "</div>" : "");
        goBtn.onclick = async function() {
          var sites = [], dbs = [];
          qsa("[data-p1site]", m.el).forEach(function(cb) { if (cb.checked) sites.push(cb.getAttribute("data-p1site")); });
          qsa("[data-p1db]", m.el).forEach(function(cb) { if (cb.checked) dbs.push(cb.getAttribute("data-p1db")); });
          if (!sites.length) { toast("请至少勾选一个站点", "error"); return; }
          if (!(await confirmDlg("开始迁移所选 <b>" + sites.length + "</b> 个站点?" + (dbs.length ? "<br>同时迁移 " + dbs.length + " 个数据库到面板 MySQL。" : "") + "<br><span style='font-size:12px;color:var(--text-3)'>过程中 1Panel 站点继续运行,不影响现网。</span>"))) return;
          m.close();
          try {
            var d = await api("/migration/p1/apply", { method: "POST", body: { sites: sites, import_certs: true, databases: dbs } });
            taskModal(d.task_id, async function(st) {
              if (st !== "success") return;
              confirmDlg("迁移完成!<br><span style='font-size:12.5px'>现在将停止 1Panel OpenResty(并禁自启),由坤面板接管 80/443 流量。如遇问题可一键回滚到 1Panel。</span>", { danger: true }).then(async function(ok) {
                if (!ok) { toast("未切换流量。1Panel 网关仍在服务,可稍后切换", "info", 8000); self.render(el); return; }
                try {
                  var d2 = await api("/migration/p1/switch", { method: "POST", body: {} });
                  taskModal(d2.task_id, function(st2) {
                    if (st2 === "success") { toast("流量已切换到坤面板", "success", 8000); self.render(el); }
                  });
                } catch (e2) { toast(e2.message, "error", 8000); self.render(el); }
              });
            });
          } catch (e) { toast(e.message, "error", 8000); }
        };
      }).catch(function(e) {
        body.innerHTML = emptyState("warn", "扫描失败", e.message);
        goBtn.style.display = "none";
      });
    }

    /* ---------- 方向五:坤面板 → 1Panel ---------- */
    function drawP1Out() {
      goBtn.style.display = "none";
      body.innerHTML = '<div class="boot-loading" style="height:80px"><div class="spinner"></div><p>读取 1Panel 信息...</p></div>';
      api("/migration/p1/reverse-plan").then(function(plan) {
        if (!plan.p1_ready) {
          body.innerHTML = '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + '</span>未检测到本机 1Panel(需已安装在本服务器 /opt/1panel)。</div>';
          return;
        }
        goBtn.textContent = "导出到 1Panel";
        goBtn.style.display = "";
        var rows = (plan.sites || []).map(function(s2) {
          var typeTxt = s2.type === "php" ? "PHP(容器)" : s2.type === "phpfpm" ? "PHP-FPM" : s2.type === "proxy" ? "反向代理" : "静态";
          return '<tr><td><label class="radio-line"><input type="checkbox" data-pnsite1="' + esc(s2.id) + '"> <b>' + esc(s2.domain) + "</b></label></td>" +
            '<td><span class="tag tag-info">' + typeTxt + "</span></td></tr>";
        }).join("");
        body.innerHTML =
          '<div class="alert alert-info page-fade"><span class="alert-ico">' + I("info") + "</span>" +
          "把坤面板站点导出为 1Panel OpenResty 可直接生效的形态(目录拷贝 + conf + 证书),并重载 1Panel OpenResty。<b>注意:这些站点不会出现在 1Panel 面板界面里</b>(由外部写入,功能不受影响;1Panel 重装 OpenResty 时会丢失)。</div>" +
          '<div class="settings-section"><div class="settings-section-title">面板站点(' + (plan.sites || []).length + ')</div>' +
          '<div class="table-wrap"><table class="table"><thead><tr><th>域名</th><th>类型</th></tr></thead><tbody>' + (rows || '<tr><td colspan=2 class=help>面板暂无站点</td></tr>') + "</tbody></table></div></div>" +
          ((plan.warnings || []).length ? '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + "</span>" + plan.warnings.map(function(x) { return esc(x); }).join("<br>") + "</div>" : "");
        goBtn.onclick = async function() {
          var sites = [];
          qsa("[data-pnsite1]", m.el).forEach(function(cb) { if (cb.checked) sites.push(cb.getAttribute("data-pnsite1")); });
          if (!sites.length) { toast("请至少勾选一个站点", "error"); return; }
          if (!(await confirmDlg("开始导出所选 <b>" + sites.length + "</b> 个站点到 1Panel?<br><span style='font-size:12px;color:var(--text-3)'>1Panel 现有站点不受影响。</span>"))) return;
          m.close();
          try {
            var d = await api("/migration/p1/reverse-apply", { method: "POST", body: { sites: sites } });
            taskModal(d.task_id, function(st) {
              if (st === "success") { toast("已导出到 1Panel", "success", 8000); self.render(el); }
            });
          } catch (e) { toast(e.message, "error", 8000); }
        };
      }).catch(function(e) {
        body.innerHTML = emptyState("warn", "预检失败", e.message);
        goBtn.style.display = "none";
      });
    }

    function switchDir(dir) {
      curDir = dir;
      qsa(".hub-tab", m.el).forEach(function(x) { x.classList.toggle("active", x.getAttribute("data-dir") === dir); });
      if (dir === "in") { drawIn(); return; }
      if (dir === "remote") { goBtn.textContent = "开始迁移"; goBtn.style.display = "none"; drawRemote(); return; }
      body.innerHTML = '<div class="boot-loading" style="height:80px"><div class="spinner"></div><p>读取面板与宝塔信息...</p></div>';
      goBtn.style.display = "none";
      api("/migration/bt/reverse-plan").then(function(plan) {
        if (curDir !== "out") return;
        if (!plan.bt_ready) { body.innerHTML = '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + "</span>未检测到宝塔面板,无法导出</div>"; return; }
        drawOut(plan);
      }).catch(function(e) { body.innerHTML = emptyState("warn", "加载失败", e.message); });
    }

    /* --- 远程 1Panel:凭据表单 + 扫描 + 迁移 --- */
    function drawP1Remote() {
      goBtn.style.display = "none";
      body.innerHTML =
        '<div class="alert alert-info page-fade"><span class="alert-ico">' + I("info") + "</span>" +
        "输入装有 <b>1Panel</b> 的远程服务器 IP 和 root 密码,面板通过 SSH 自动对接:探测站点/SSL 证书/MySQL 数据库,流式搬运站点文件,反代与 PHP 上游回源远程机(DNS 切换前零停机),数据库从远程 MySQL 容器导出导入。</div>" +
        '<div class="alert alert-warning page-fade"><span class="alert-ico">' + I("warn") + "</span><b>防拉黑提醒:</b>长时间 SSH 迁移可能触发远程服务器防爆破,开始前请把本服务器出口 IP 加入远程白名单(1Panel → 主机安全 → SSH 防爆破白名单,或 fail2ban 忽略列表)。</div>" +
        '<div class="settings-section"><div class="settings-section-title">第一步 · 连接远程服务器</div>' +
        '<div class="waf-params-row" style="gap:10px">' +
        '<span class="waf-param" style="flex:1;min-width:150px">服务器 IP<input class="input" id="p1r-host" placeholder="如 123.45.67.89"></span>' +
        '<span class="waf-param" style="width:110px">SSH 端口<input class="input" id="p1r-port" type="number" value="22"></span>' +
        '<span class="waf-param" style="width:130px">用户名<input class="input" id="p1r-user" value="root"></span>' +
        '<span class="waf-param" style="flex:1;min-width:150px">root 密码<input class="input" type="password" id="p1r-pass" placeholder="SSH 密码"></span>' +
        '<button class="btn btn-primary btn-sm" id="p1r-check" style="margin-top:18px">连接检测</button></div>' +
        '<div class="help" style="margin-top:8px">首次连接采用指纹信任机制:连接成功后回显服务器指纹,迁移时自动校验,防中间人劫持。需要远程服务器允许密码登录。</div>' +
        '<div id="p1r-fp" class="help mono" style="margin-top:6px"></div></div>' +
        '<div id="p1r-result"></div>';
      qs("#p1r-check", body).onclick = async function() {
        p1cred.host = qs("#p1r-host", body).value.trim();
        p1cred.port = Number(qs("#p1r-port", body).value) || 22;
        p1cred.user = qs("#p1r-user", body).value.trim() || "root";
        p1cred.pass = qs("#p1r-pass", body).value;
        if (!p1cred.host || !p1cred.pass) { toast("请填写服务器 IP 与密码", "error"); return; }
        this.disabled = true;
        try {
          var r = await api("/migration/p1/remote/scan", { method: "POST", body: p1cred });
          p1cred.fingerprint = r.fingerprint || "";
          qs("#p1r-fp", body).innerHTML = "✓ 指纹: <span class='mono'>" + esc(p1cred.fingerprint) + "</span>";
          drawP1RemoteResult(r);
        } catch (e) {
          toast(e.message, "error", 6000);
          qs("#p1r-fp", body).innerHTML = '<span style="color:var(--danger)">✗ 连接失败: ' + esc(e.message) + "</span>";
        }
        this.disabled = false;
      };
      function drawP1RemoteResult(res) {
        var rows = (res.sites || []).map(function(s2) {
          var typeTxt = s2.type === "php" ? "PHP" + (s2.fpm_unix ? "(unix socket)" : "") : s2.type === "proxy" ? "反向代理" : "静态";
          var warn = s2.warnings ? '<div class="help" style="color:var(--warning)">' + esc(s2.warnings) + "</div>" : "";
          return '<tr><td><label class="radio-line"><input type="checkbox" checked data-p1site="' + esc(s2.domain) + '"> <b>' + esc(s2.domain) + "</b>" + warn + "</label></td>" +
            '<td><span class="tag tag-info">' + typeTxt + "</span>" + (s2.ssl ? '<span class="tag tag-success">SSL</span>' : "") + "</td></tr>";
        }).join("");
        var dbChecks = (res.dbs || []).map(function(d0) {
          return '<label class="radio-line" style="display:inline-flex;margin:2px 18px 2px 0"><input type="checkbox" checked data-p1db="' + esc(d0.name) + '"> <span class="mono">' + esc(d0.name) + "</span></label>";
        }).join("");
        qs("#p1r-result", body).innerHTML =
          '<div class="settings-section"><div class="settings-section-title">第二步 · 选择要迁移的站点(' + (res.sites || []).length + ')</div>' +
          '<div class="table-wrap"><table class="table"><thead><tr><th>域名</th><th>类型</th></tr></thead><tbody>' + (rows || '<tr><td colspan=2 class=help>远程未检测到站点</td></tr>') + "</tbody></table></div></div>" +
          '<div class="settings-section"><div class="settings-section-title">数据库(' + (res.dbs || []).length + ')</div>' +
          (dbChecks || '<div class="help">远程暂无 MySQL 数据库</div>') + "</div>";
        goBtn.onclick = async function() {
          var sites = [], dbs = [];
          qsa("[data-p1site]", body).forEach(function(cb) { if (cb.checked) sites.push(cb.getAttribute("data-p1site")); });
          qsa("[data-p1db]", body).forEach(function(cb) { if (cb.checked) dbs.push(cb.getAttribute("data-p1db")); });
          if (!sites.length) { toast("请至少勾选一个站点", "error"); return; }
          if (!(await confirmDlg("开始迁移远程 1Panel 的 <b>" + sites.length + "</b> 个站点?" + (dbs.length ? "<br>同时迁移 " + dbs.length + " 个数据库。" : "")))) return;
          m.close();
          try {
            var d = await api("/migration/p1/remote/apply", { method: "POST", body: {
              host: p1cred.host, port: p1cred.port, user: p1cred.user, pass: p1cred.pass, fingerprint: p1cred.fingerprint,
              sites: sites, import_certs: true, databases: dbs,
            }});
            taskModal(d.task_id, async function(st) {
              if (st !== "success") return;
              confirmDlg("远程迁移完成!<br><span style='font-size:12.5px'>反代/PHP 上游当前回源远程机(零停机);确认站点正常后,将停止远程 1Panel OpenResty 并由坤面板接管 80/443。</span>", { danger: true }).then(async function(ok) {
                if (!ok) { toast("未切换流量", "info", 8000); self.render(el); return; }
                try {
                  var d2 = await api("/migration/p1/switch", { method: "POST", body: {} });
                  taskModal(d2.task_id, function(st2) {
                    if (st2 === "success") { toast("流量已切换到坤面板", "success", 8000); self.render(el); }
                  });
                } catch (e2) { toast(e2.message, "error", 8000); self.render(el); }
              });
            });
          } catch (e) { toast(e.message, "error", 8000); }
        };
      }
    }

    drawP1In();
  },

  btMigrateDialog(scan, el) {
    var self = this;
    var m = modal({
      title: "面板互通迁移 · 宝塔 ⇄ 坤面板", width: 880,
      body: '<div class="hub-tabs" style="margin-bottom:14px">' +
        '<div class="hub-tab active" data-dir="in">宝塔 → 坤面板(本机)</div>' +
        '<div class="hub-tab" data-dir="out">坤面板 → 宝塔</div>' +
        '<div id="btmig-body"><div class="boot-loading" style="height:80px"><div class="spinner"></div></div></div>',
      footer: '<span class="help" style="margin-right:auto">随时可一键回滚;迁移不动宝塔原有站点与数据</span>' +
        '<button class="btn btn-plain" data-act="no">取 消</button>' +
        '<button class="btn btn-primary" data-act="go" style="display:none">开始迁移</button>',
    });
    var body = qs("#btmig-body", m.el);
    var goBtn = qs('[data-act="go"]', m.el);
    qs('[data-act="no"]', m.el).onclick = m.close;
    var curDir = "in";

    /* ---------- 方向一:宝塔 → 坤面板 ---------- */
    function drawIn() {
      goBtn.textContent = "开始迁移";
      goBtn.style.display = "";
      var rows = (scan.sites || []).map(function(s2) {
        var typeTxt = s2.type === "php" ? "PHP " + (s2.php_ver || "") : s2.type === "proxy" ? "反向代理" : "静态";
        var sslTxt = s2.ssl ? '<span class="tag tag-success">SSL</span>' : "";
        return '<tr><td><label class="radio-line"><input type="checkbox" checked data-btsite="' + esc(s2.domain) + '"> <b>' + esc(s2.domain) + "</b></label></td>" +
          '<td><span class="tag tag-info">' + typeTxt + "</span> " + sslTxt + "</td>" +
          '<td class="mono fm-ellipsis" style="max-width:200px" title="' + esc(s2.root) + '">' + esc(s2.root) + "</td></tr>";
      }).join("");
      var cronRows = (scan.crons || []).map(function(c0) {
        var tag;
        if (c0.internal) tag = '<span class="tag tag-info" title="依赖宝塔面板自身(/www/server/panel 脚本或 database/site/path 专属类型),迁移出去大概率不可用">宝塔内部</span>';
        else if (c0.mig) tag = '<span class="tag tag-success">可迁移</span>';
        else tag = '<span class="tag tag-info">仅展示</span>';
        var cb = c0.mig
          ? '<input type="checkbox" data-btcron="' + esc(c0.echo || c0.name) + '"' + (c0.internal ? "" : " checked") + '> '
          : "";
        return "<tr><td>" + cb + esc(c0.name) + "</td><td>" + esc(c0.cycle) + "</td><td>" + tag + "</td></tr>";
      }).join("");
      var dbChecks = (scan.dbs || []).map(function(d0) {
        return '<label class="radio-line" style="display:inline-flex;margin:2px 18px 2px 0"><input type="checkbox" data-btdb="' + esc(d0.name) + '"> <span class="mono">' + esc(d0.name) + "</span>" + (d0.ps ? '<span class="help" style="margin:0 0 0 6px">' + esc(d0.ps) + "</span>" : "") + "</label>";
      }).join("");
      body.innerHTML =
        '<div class="alert alert-info page-fade"><span class="alert-ico">' + I("info") + "</span>" +
        "迁移宝塔站点的<b>域名、证书、伪静态、自定义反代/子目录规则(保真保留)</b>;PHP 站点 FastCGI 直连宿主机 php-fpm,版本不变;" +
        "迁移完成后自动<b>接管证书续期</b>(面板重签并自动续期),流量切换与回滚走系统服务,重启不抢 80/443。</div>" +
        '<div class="settings-section"><div class="settings-section-title">站点(' + (scan.sites || []).length + ')<span class="help" style="margin:0">勾选要迁移的站点</span></div>' +
        '<div class="table-wrap"><table class="table"><thead><tr><th>域名</th><th>类型</th><th>原目录</th></tr></thead><tbody>' + (rows || '<tr><td colspan=3 class=help>未检测到站点</td></tr>') + "</tbody></table></div></div>" +
        '<div class="settings-section"><div class="settings-section-title">计划任务(' + (scan.crons || []).length + ')<span class="help" style="margin:0">默认勾选「可迁移」项;宝塔内部任务(依赖宝塔自身)默认不勾选;迁移后自动注释宝塔原 crontab,不会双跑</span></div>' +
        (cronRows ? '<div class="table-wrap"><table class="table"><thead><tr><th>名称</th><th>周期</th><th>状态</th></tr></thead><tbody>' + cronRows + "</tbody></table></div>" : '<div class="help">无</div>') + "</div>" +
        '<div class="settings-section"><div class="settings-section-title">数据库(' + (scan.dbs || []).length + ')<span class="help" style="margin:0">默认不迁移:站点继续连宿主机 MySQL(连接串零改动)。勾选则<b>复制一份到面板 MySQL</b>,之后站点需自行改连接串,两边是独立数据</span></div>' +
        (dbChecks || '<div class="help">宝塔暂无数据库</div>') + "</div>" +
        '<div id="btmig-warn"></div>';
      var warnBox = qs("#btmig-warn", m.el);
      if (warnBox && (scan.warnings || []).length) {
        warnBox.innerHTML = '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + "</span>" + (scan.warnings || []).map(function(x) { return esc(x); }).join("<br>") + "</div>";
      }
      goBtn.onclick = async function() {
        var sites = [], dbs = [], crons = [];
        qsa("[data-btsite]", m.el).forEach(function(cb) { if (cb.checked) sites.push(cb.getAttribute("data-btsite")); });
        qsa("[data-btdb]", m.el).forEach(function(cb) { if (cb.checked) dbs.push(cb.getAttribute("data-btdb")); });
        qsa("[data-btcron]", m.el).forEach(function(cb) { if (cb.checked) crons.push(cb.getAttribute("data-btcron")); });
        if (!sites.length) { toast("请至少勾选一个站点", "error"); return; }
        var dbNote = dbs.length ? "<br><span style='color:var(--warning)'>将复制 " + dbs.length + " 个数据库到面板 MySQL,完成后站点需改连接串。</span>" : "";
        if (!(await confirmDlg("开始迁移所选 <b>" + sites.length + "</b> 个站点、<b>" + crons.length + "</b> 条计划任务?" + dbNote + "<br><span style='font-size:12px;color:var(--text-3)'>过程中原宝塔站点继续运行,不影响现网;完成后需确认切换流量。</span>"))) return;
        m.close();
        try {
          var d = await api("/migration/bt/apply", { method: "POST", body: { sites: sites, import_certs: true, migrate_cron: crons.length > 0, crons: crons, databases: dbs } });
          taskModal(d.task_id, async function(st) {
            if (st !== "success") return;
            confirmDlg("迁移完成!<br><span style='font-size:12.5px'>现在将停止宝塔 Nginx(并禁用其自启),由坤面板接管 80/443 流量;<br>切换后面板会自动为迁移站点重新签发证书接管续期。如遇问题可一键回滚到宝塔。</span>", { danger: true }).then(async function(ok) {
              if (!ok) { toast("未切换流量。原宝塔 Nginx 仍在服务,可稍后在网站页执行切换", "info", 8000); self.render(el); return; }
              try {
                var d2 = await api("/migration/bt/switch", { method: "POST", body: {} });
                taskModal(d2.task_id, function(st2) {
                  if (st2 === "success") { toast("流量已切换到坤面板,证书续期已接管", "success", 8000); self.render(el); }
                });
              } catch (e2) { toast(e2.message, "error", 8000); self.render(el); }
            });
          });
        } catch (e) { toast(e.message, "error", 8000); }
      };
    }

    /* ---------- 方向二:坤面板 → 宝塔 ---------- */
    function drawOut(plan) {
      goBtn.textContent = "导出到宝塔";
      goBtn.style.display = "";
      var rows = (plan.sites || []).map(function(s2) {
        var typeTxt = s2.type === "php" ? "PHP(容器)" : s2.type === "phpfpm" ? "PHP-FPM" : s2.type === "proxy" ? "反向代理" : "静态";
        var sslTxt = s2.ssl_cert_id ? '<span class="tag tag-success">SSL</span>' : "";
        var rootTxt = s2.type === "proxy" ? "-" : s2.root;
        return '<tr><td><label class="radio-line"><input type="checkbox" data-pnsite="' + esc(s2.id) + '"> <b>' + esc(s2.domain) + "</b></label></td>" +
          '<td><span class="tag tag-info">' + typeTxt + "</span> " + sslTxt + "</td>" +
          '<td class="mono fm-ellipsis" style="max-width:200px" title="' + esc(rootTxt) + '">' + esc(rootTxt) + "</td></tr>";
      }).join("");
      var sysDbs = { information_schema: 1, mysql: 1, performance_schema: 1, sys: 1 };
      var dbs = (plan.dbs || []).filter(function(d) { return !sysDbs[d]; });
      var dbChecks = dbs.map(function(d) {
        return '<label class="radio-line" style="display:inline-flex;margin:2px 18px 2px 0"><input type="checkbox" data-pndb="' + esc(d) + '"> <span class="mono">' + esc(d) + "</span></label>";
      }).join("");
      body.innerHTML =
        '<div class="alert alert-info page-fade"><span class="alert-ico">' + I("info") + "</span>" +
        "把坤面板的站点(域名/目录/SSL/伪静态/自定义规则)与数据库导出到本机宝塔;宝塔 Nginx 运行状态不变,导出后在宝塔里重载即可生效。</div>" +
        '<div class="settings-section"><div class="settings-section-title">面板站点(' + (plan.sites || []).length + ')<span class="help" style="margin:0">宝塔未装的 PHP 版本站点将以静态方式导出并提示</span></div>' +
        '<div class="table-wrap"><table class="table"><thead><tr><th>域名</th><th>类型</th><th>目录</th></tr></thead><tbody>' + (rows || '<tr><td colspan=3 class=help>面板暂无站点</td></tr>') + "</tbody></table></div></div>" +
        '<div class="settings-section"><div class="settings-section-title">面板数据库(' + dbs.length + ')<span class="help" style="margin:0">导出到宝塔宿主机 MySQL;勾选才迁移</span></div>' +
        (dbChecks || '<div class="help">面板 MySQL 暂无业务库或未安装</div>') + "</div>" +
        ((plan.warnings || []).length ? '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + "</span>" + plan.warnings.map(function(x) { return esc(x); }).join("<br>") + "</div>" : "");
      goBtn.onclick = async function() {
        var sites = [], dbs2 = [];
        qsa("[data-pnsite]", m.el).forEach(function(cb) { if (cb.checked) sites.push(cb.getAttribute("data-pnsite")); });
        qsa("[data-pndb]", m.el).forEach(function(cb) { if (cb.checked) dbs2.push(cb.getAttribute("data-pndb")); });
        if (!sites.length && !dbs2.length) { toast("请至少勾选一项", "error"); return; }
        if (!(await confirmDlg("开始导出所选 <b>" + sites.length + "</b> 个站点、<b>" + dbs2.length + "</b> 个数据库到宝塔?<br><span style='font-size:12px;color:var(--text-3)'>宝塔现有站点不受影响;同名站点目录/证书将被覆盖。</span>"))) return;
        m.close();
        try {
          var d = await api("/migration/bt/reverse-apply", { method: "POST", body: { sites: sites, dbs: dbs2 } });
          taskModal(d.task_id, function(st) {
            if (st === "success") { toast("已导出到宝塔", "success", 8000); self.render(el); }
          });
        } catch (e) { toast(e.message, "error", 8000); }
      };
    }

    /* ---------- 方向三:远程宝塔 → 本面板(SSH 对接) ---------- */
    function drawRemote() {
      goBtn.style.display = "none";
      body.innerHTML =
        '<div class="alert alert-info page-fade"><span class="alert-ico">' + I("info") + "</span>" +
        "输入装有宝塔的远程服务器的 <b>IP 和 root 密码</b>,面板通过 SSH 自动对接:探测站点/证书/伪静态/自定义规则/计划任务/数据库," +
        "流式搬运网站文件,PHP 按远程版本自动安装对应运行时(版本不变),证书导入后自动重签接管续期,数据库自动导入面板 MySQL 并重建账号授权。</div>" +
        '<div class="alert alert-warning page-fade" id="rm-whitelist" style="display:none"><span class="alert-ico">' + I("warn") + "</span>" +
        "<b>防拉黑提醒:</b>长时间 SSH 迁移可能触发远程宝塔的 SSH 防爆破。开始前请先把本服务器出口 IP " +
        '<b class="mono" id="rm-myip"></b> 加入远程服务器的白名单(宝塔面板 → SSH 防爆破 → 白名单,或 fail2ban 忽略列表)。</div>' +
        '<div class="settings-section"><div class="settings-section-title">第一步 · 连接远程服务器</div>' +
        '<div style="display:grid;grid-template-columns:1.4fr .7fr .8fr 1.2fr auto;gap:10px;align-items:end">' +
        '<div><label>服务器 IP</label><input class="input" id="rm-host" placeholder="如 123.45.67.89"></div>' +
        '<div><label>SSH 端口</label><input class="input" id="rm-port" type="number" value="22"></div>' +
        '<div><label>用户名</label><input class="input" id="rm-user" value="root"></div>' +
        '<div><label>root 密码</label><input class="input" id="rm-pass" type="password" placeholder="SSH 密码"></div>' +
        '<button class="btn btn-primary" id="rm-scan" style="height:38px">连接检测</button></div>' +
        '<div class="help" style="margin-top:8px">首次连接采用指纹信任机制:连接成功后回显服务器指纹,迁移时自动校验,防中间人劫持。需要远程服务器允许密码登录。</div>' +
        '<div id="rm-fp" class="help" style="margin-top:4px"></div></div>' +
        '<div id="rm-result"></div>';
      // 白名单提醒:带出本服务器出口 IP(/system/quick 嵌套在 system 里)
      api("/system/quick").then(function(q) {
        var ip = q && q.system && q.system.public_ip;
        if (ip) {
          qs("#rm-myip", body).textContent = ip;
          qs("#rm-whitelist", body).style.display = "";
        }
      }).catch(function() {});
      qs("#rm-scan", body).onclick = async function() {
        var host = qs("#rm-host", body).value.trim();
        var pass = qs("#rm-pass", body).value;
        if (!host || !pass) { toast("请填写 IP 与密码", "error"); return; }
        this.disabled = true; this.textContent = "连接检测中...";
        try {
          var scan = await api("/migration/bt/remote/scan", { method: "POST", body: {
            host: host, port: Number(qs("#rm-port", body).value) || 22,
            user: qs("#rm-user", body).value.trim() || "root", pass: pass } });
          qs("#rm-fp", body).innerHTML = '服务器指纹: <span class="mono">' + esc(scan.fingerprint) + "</span>(已记录,迁移时自动校验)";
          drawRemoteResult(scan);
        } catch (e) {
          qs("#rm-result", body).innerHTML = '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + "</span>" + esc(e.message) + "</div>";
        }
        this.disabled = false; this.textContent = "连接检测";
      };
      function drawRemoteResult(scan) {
        // 白名单提醒补全出口 IP(连接检测后端已回传)
        if (scan.local_ip) {
          var ipEl = qs("#rm-myip", body);
          if (ipEl && !ipEl.textContent) ipEl.textContent = scan.local_ip;
          var wl = qs("#rm-whitelist", body);
          if (wl) wl.style.display = "";
        }
        var rows = (scan.sites || []).map(function(s2) {
          var typeTxt = s2.type === "php" ? "PHP " + (s2.php_ver ? phpDirDisp(s2.php_ver) : "") : s2.type === "proxy" ? "反向代理" : "静态";
          var sslTxt = s2.ssl ? '<span class="tag tag-success">SSL</span>' : "";
          return '<tr><td><label class="radio-line"><input type="checkbox" checked data-rmsite="' + esc(s2.domain) + '"> <b>' + esc(s2.domain) + "</b></label></td>" +
            '<td><span class="tag tag-info">' + typeTxt + "</span> " + sslTxt + "</td>" +
            '<td class="mono fm-ellipsis" style="max-width:200px" title="' + esc(s2.root) + '">' + esc(s2.root) + "</td></tr>";
        }).join("");
        var dbChecks = (scan.dbs || []).map(function(d0) {
          return '<label class="radio-line" style="display:inline-flex;margin:2px 18px 2px 0"><input type="checkbox" data-rmdb="' + esc(d0.name) + '"> <span class="mono">' + esc(d0.name) + "</span>" + (d0.ps ? '<span class="help" style="margin:0 0 0 6px">' + esc(d0.ps) + "</span>" : "") + "</label>";
        }).join("");
        qs("#rm-result", body).innerHTML =
          '<div class="settings-section"><div class="settings-section-title">第二步 · 勾选要迁移的内容</div>' +
          '<div class="settings-section-title" style="margin-top:4px">远程站点(' + (scan.sites || []).length + ')<span class="help" style="margin:0">网站文件自动搬运,PHP 版本自动匹配安装;自定义反代规则保真保留</span></div>' +
          '<div class="table-wrap"><table class="table"><thead><tr><th>域名</th><th>类型</th><th>原目录</th></tr></thead><tbody>' + (rows || '<tr><td colspan=3 class=help>未检测到站点</td></tr>') + "</tbody></table></div></div>" +
          '<div class="settings-section"><div class="settings-section-title">计划任务(' + (scan.crons || []).length + ')<span class="help" style="margin:0">自动迁移并改写站点路径;旧服务器定时需手动停用</span></div>' +
          '<div class="settings-section"><div class="settings-section-title">第三步 · 开始迁移</div>' +
          '<div class="help" style="margin:0">迁移在后台运行并显示实时进度条(每搬完一个站点/数据库推进一格),可随时关闭窗口从顶栏「后台任务」查看;' +
          '完成后把域名 DNS 的 A 记录指向本服务器即完成切换,切换前旧站照常服务。</div></div>' +
          '<div class="help">' + (scan.crons || []).map(function(c0) { return esc(c0.name + " " + c0.cycle); }).join("、") + '</div></div>' +
          '<div class="settings-section"><div class="settings-section-title">远程数据库(' + (scan.dbs || []).length + ')<span class="help" style="margin:0">勾选则导出并导入面板 MySQL(账号密码不变,站点连接串 host 需改 172.17.0.1);不勾选则站点继续连旧服务器数据库</span></div>' +
          (dbChecks || '<div class="help">远程无数据库或未启用 MySQL</div>') + "</div>" +
          ((scan.warnings || []).length ? '<div class="alert alert-warning"><span class="alert-ico">' + I("warn") + "</span>" + scan.warnings.map(function(x) { return esc(x); }).join("<br>") + "</div>" : "");
        goBtn.textContent = "开始迁移";
        goBtn.style.display = "";
        goBtn.onclick = async function() {
          var sites = [], dbs = [];
          qsa("[data-rmsite]", body).forEach(function(cb) { if (cb.checked) sites.push(cb.getAttribute("data-rmsite")); });
          qsa("[data-rmdb]", body).forEach(function(cb) { if (cb.checked) dbs.push(cb.getAttribute("data-rmdb")); });
          if (!sites.length && !dbs.length) { toast("请至少勾选一项", "error"); return; }
          if (!(await confirmDlg("开始迁移远程宝塔(" + esc(host) + ")?<br>站点 " + sites.length + " 个、数据库 " + dbs.length + " 个<br><span style='font-size:12px;color:var(--text-3)'>迁移全程不影响旧服务器运行;完成后需把域名 DNS 指向本服务器。</span>"))) return;
          m.close();
          try {
            var d = await api("/migration/bt/remote/apply", { method: "POST", body: {
              host: host, port: Number(qs("#rm-port", body).value) || 22, user: qs("#rm-user", body).value.trim() || "root",
              pass: pass, fingerprint: scan.fingerprint, sites: sites, databases: dbs } });
            taskModal(d.task_id, function(st) {
              if (st === "success") { toast("远程迁移完成,记得切换域名 DNS", "success", 10000); self.render(el); }
            });
          } catch (e) { toast(e.message, "error", 8000); }
        };
      }
    }
    // 宝塔目录名(74)→ 显示版本(7.4)
    function phpDirDisp(d) { return d.length === 2 ? d[0] + "." + d[1] : d; }

    qsa(".hub-tab", m.el).forEach(function(t) {
      t.onclick = function() { switchDir(t.getAttribute("data-dir")); };
    });
    drawIn();
  },

  addDialog(el) {
    var self = this;
    var m = modal({
      title: "创建网站", width: 640,
      body: '<div class="wz-types">' +
        '<div class="wz-type-card active" data-t="static"><b>静态站点</b><div>HTML/CSS/JS 纯静态网站</div></div>' +
        '<div class="wz-type-card" data-t="php"><b>PHP 站点</b><div>WordPress/ThinkPHP 等 PHP 程序<br><span class="help" style="margin:0">需安装 PHP 运行时</span></div></div>' +
        '<div class="wz-type-card" data-t="proxy"><b>反向代理</b><div>Go/Node/Java 等本地服务<br>或任意外部服务</div></div></div>' +
        '<div class="form-item required" style="margin-top:14px"><label>域名</label><input class="input" id="w-domain" placeholder="多个域名用空格分隔,如:demo.com www.demo.com">' +
        '<div class="help">请先将域名解析到本服务器</div></div>' +
        '<div id="w-dyn"></div>' +
        '<div class="form-item"><label>监听端口</label><input class="input" id="w-port" type="number" value="80" style="max-width:180px"></div>' +
        '<div class="wz-collapse"><label class="radio-line"><input type="checkbox" id="w-db-on"> 同时创建数据库(MySQL/MariaDB)</label>' +
        '<div id="w-db-fields" style="display:none;margin-top:8px">' +
        '<div class="form-item"><label>数据库实例</label><select class="select" id="w-db-inst"></select></div>' +
        '<div class="form-item"><label>数据库名</label><input class="input mono" id="w-db-name" placeholder="小写字母/数字/下划线"></div>' +
        '<div class="help">库名与访问密码创建后可在数据库页面管理;需要先安装并运行 MySQL/MariaDB</div></div></div>' +
        '<div class="form-item"><label>备注</label><input class="input" id="w-remark" placeholder="站点用途说明(可选)"></div>',
      footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">创 建</button>',
    });
    var curType = "static";
    var dyn = qs("#w-dyn", m.el);
    // 类型卡片切换
    function renderDyn(t) {
      curType = t;
      qsa(".wz-type-card", m.el).forEach(function(x) { x.classList.toggle("active", x.getAttribute("data-t") === t); });
      if (t === "static") {
        dyn.innerHTML = '<div class="form-item"><label>根目录</label><input class="input mono" id="w-root" placeholder="留空自动生成,可自定义绝对路径"></div>';
      } else if (t === "php") {
        dyn.innerHTML = '<div class="form-item"><label>PHP 版本</label><select class="select" id="w-php-key" style="max-width:320px"></select>' +
          '<div id="w-php-status" class="help" style="margin-top:6px">检测中…</div></div>' +
          '<div class="form-item"><label>根目录</label><input class="input mono" id="w-root" placeholder="留空自动生成,可自定义绝对路径">' +
          '<div class="help">站点目录会自动挂载进 PHP 运行时,上传的 PHP 程序立即可用;需要其他版本可在应用商店安装(多版本共存)</div></div>';
        api("/apps/instances").then(function(insts) {
          var phps = (insts || []).filter(function(x) { return (x.key === "php" || /^php[0-9]+$/.test(x.key)) && x.status === "running"; });
          var sel = qs("#w-php-key", m.el);
          var box = qs("#w-php-status", m.el);
          if (!sel || !box) return;
          if (!phps.length) {
            sel.innerHTML = '<option value="">无可用版本</option>';
            box.innerHTML = '<span class="tag tag-warning">PHP 运行时未安装或未运行</span> <a href="#/apps">去应用商店安装</a>';
            box.dataset.missing = "1";
            return;
          }
          delete box.dataset.missing;
          sel.innerHTML = phps.map(function(x) {
            var v = (x.params && x.params.Version) || x.key;
            return '<option value="' + esc(x.key) + '">PHP ' + esc(v) + '</option>';
          }).join("");
          box.innerHTML = '<span class="tag tag-success"><span class="dot on"></span>运行中,可直接创建;不同项目可选不同版本</span>';
        }).catch(function() {});
      } else {
        dyn.innerHTML = '<div class="form-item required"><label>目标 URL</label><input class="input mono" id="w-proxy" placeholder="如: http://127.0.0.1:3000">' +
          '<div class="help">Go / Node / Java 等应用先在本机或容器中启动,再反代到其端口</div></div>' +
          '<div class="form-item"><label>发送域名 Host</label><input class="input mono" id="w-proxy-host" placeholder="$http_host(默认;可写域名如 api.example.com 或 $host)">' +
          '<div class="help">转发给上游的 Host 请求头;留空使用访客原始域名。上游按域名区分站点时需在此指定</div></div>';
      }
    }
    qsa(".wz-type-card", m.el).forEach(function(c) { c.onclick = function() { renderDyn(c.getAttribute("data-t")); }; });
    renderDyn("static");
    // 数据库可选
    qs("#w-db-on", m.el).onchange = function() {
      qs("#w-db-fields", m.el).style.display = this.checked ? "" : "none";
      if (this.checked) {
        api("/db/instances").then(function(list) {
          var sel = qs("#w-db-inst", m.el);
          if (!sel) return;
          var running = (list || []).filter(function(x) { return x.running; });
          sel.innerHTML = running.length
            ? running.map(function(x) { return '<option value="' + esc(x.key) + '">' + esc(x.name || x.key) + (x.running ? "" : "(未运行)") + "</option>"; }).join("")
            : '<option value="">(无可用实例,请先安装 MySQL/MariaDB)</option>';
        }).catch(function() {});
      }
    };
    qs('[data-act="no"]', m.el).onclick = m.close;
    qs('[data-act="yes"]', m.el).onclick = async function() {
      var domain = qs("#w-domain", m.el).value.trim();
      if (!domain) { toast("域名不能为空", "error"); return; }
      var port = Number(qs("#w-port", m.el).value) || 80;
      if (curType === "php") {
        var box = qs("#w-php-status", m.el);
        if (box && box.dataset.missing) { toast("请先安装并运行 PHP 运行时", "error"); return; }
      }
      var body = {
        domain: domain, type: curType, port: port,
        proxy_to: curType === "proxy" ? qs("#w-proxy", m.el).value.trim() : "",
        proxy_host: curType === "proxy" && qs("#w-proxy-host", m.el) ? qs("#w-proxy-host", m.el).value.trim() : "",
        root_dir: qs("#w-root", m.el) ? qs("#w-root", m.el).value.trim() : "",
        remark: qs("#w-remark", m.el).value.trim(),
      };
      if (curType === "php") {
        var pk = qs("#w-php-key", m.el);
        if (pk && pk.value) body.php_key = pk.value;
      }
      // 可选数据库
      if (qs("#w-db-on", m.el).checked) {
        var inst = qs("#w-db-inst", m.el).value;
        var dbname = qs("#w-db-name", m.el).value.trim();
        if (!inst) { toast("没有可用的数据库实例", "error"); return; }
        if (!dbname) { toast("请填写数据库名", "error"); return; }
        body.db_instance = inst; body.db_name = dbname;
      }
      try {
        var d = await api("/websites", { method: "POST", body: body });
        toast("网站创建成功");
        if (d.db_error) toast("站点已创建,但数据库创建失败: " + d.db_error, "error", 6000);
        else if (body.db_instance) toast("数据库 " + body.db_name + " 已创建");
        m.close();
        self.render(el);
      } catch (e) { toast(e.message, "error", 5000); }
    };
  },
  /* 网站设置(宝塔式页签) */
  /* 站点备份管理:留存/下载/恢复/上传 */
  async siteBackupDialog(s, el) {
    var self = this;
    var m = modal({
      title: "站点备份 · " + s.alias, width: 700,
      body: '<div class="settings-section"><div class="settings-section-title">立即备份</div>' +
        '<div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">' +
        '<button class="btn btn-primary btn-sm" id="sb-create">' + I("archive") + ' 备份到服务端</button>' +
        '<button class="btn btn-plain btn-sm" id="sb-dl-now">' + I("download") + ' 打包并下载到本地</button>' +
        '<button class="btn btn-plain btn-sm" id="sb-upload">' + I("upload") + ' 上传备份包</button>' +
        '<input type="file" id="sb-file" accept=".tar.gz,.tgz" style="display:none"></div>' +
        '<div class="help" style="margin-top:8px">备份保存在面板数据目录(backups/sites),最多保留 10 份;恢复会<b style="color:var(--danger)">清空站点目录</b>后解压覆盖,请谨慎操作</div></div>' +
        '<div class="settings-section"><div class="settings-section-title">备份列表</div><div id="sb-list">' + skeletonTable(4, 4) + "</div></div>",
      footer: '<button class="btn btn-plain" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    const token = encodeURIComponent(localStorage.getItem(TOKEN_KEY));

    async function load() {
      const box = qs("#sb-list", m.el);
      if (!box) return;
      try {
        const list = (await api("/websites/" + s.id + "/backups")) || [];
        if (!list.length) { box.innerHTML = emptyState("archive", "暂无备份", "点击上方「备份到服务端」立即创建"); return; }
        box.innerHTML = '<div class="table-wrap"><table class="table"><thead><tr><th>文件</th><th style="width:100px">大小</th><th style="width:150px">时间</th><th style="width:210px">操作</th></tr></thead><tbody>' +
          list.map((r) => '<tr><td class="mono fm-ellipsis" style="max-width:220px" title="' + esc(r.file) + '">' + esc(r.file) + "</td>" +
            "<td>" + fmtBytes(r.size) + '</td><td class="waf-nowrap">' + esc(r.time) + "</td>" +
            '<td><div class="ops"><button class="btn btn-xs btn-plain" data-sb-dl="' + esc(r.file) + '">下载</button>' +
            '<button class="btn btn-xs btn-plain" data-sb-restore="' + esc(r.file) + '">恢复</button>' +
            '<button class="btn btn-xs btn-danger-plain" data-sb-del="' + esc(r.file) + '">删除</button></div></td></tr>').join("") +
          "</tbody></table></div>";
        box.querySelectorAll("[data-sb-dl]").forEach((b) => (b.onclick = () => window.open("/api/v1/websites/" + s.id + "/backups/" + b.dataset.sbDl + "/download?token=" + token, "_blank")));
        box.querySelectorAll("[data-sb-del]").forEach((b) => (b.onclick = async () => {
          if (!(await confirmDlg("确定删除该备份吗?", { danger: true }))) return;
          try { await api("/websites/" + s.id + "/backups/" + b.dataset.sbDel, { method: "DELETE" }); toast("已删除"); load(); }
          catch (e) { toast(e.message, "error"); }
        }));
        box.querySelectorAll("[data-sb-restore]").forEach((b) => (b.onclick = async () => {
          if (!(await confirmDlg("用备份恢复站点 <b>" + esc(s.alias) + "</b>?<br><span style='font-size:12px;color:var(--danger)'>站点目录将被清空并覆盖为备份内容!</span>", { danger: true }))) return;
          try {
            const d = await api("/websites/" + s.id + "/backups/restore", { method: "POST", body: { file: b.dataset.sbRestore } });
            m.close();
            taskModal(d.task_id, (st) => { if (st === "success") toast("站点已恢复"); });
          } catch (e) { toast(e.message, "error", 6000); }
        }));
      } catch (e) { box.innerHTML = emptyState("warn", "加载失败", e.message); }
    }

    qs("#sb-create", m.el).onclick = async function() {
      this.disabled = true; this.textContent = "备份中...";
      try { await api("/websites/" + s.id + "/backups", { method: "POST", body: {} }); toast("备份完成"); load(); }
      catch (e) { toast(e.message, "error", 6000); this.disabled = false; this.textContent = "备份到服务端"; }
    };
    qs("#sb-dl-now", m.el).onclick = function() {
      window.open("/api/v1/websites/" + s.id + "/backup?token=" + token, "_blank");
    };
    qs("#sb-upload", m.el).onclick = function() { qs("#sb-file", m.el).click(); };
    qs("#sb-file", m.el).onchange = async function() {
      const f = this.files[0];
      if (!f) return;
      const fd = new FormData();
      fd.append("file", f);
      toast("上传中...", "info");
      try {
        const r = await fetch("/api/v1/websites/" + s.id + "/backups/upload", { method: "POST", headers: { "Authorization": "Bearer " + token }, body: fd });
        const d = await r.json();
        if (d.code !== 0) { toast(d.message || "上传失败", "error", 6000); return; }
        toast("已上传"); load();
      } catch (e) { toast(e.message, "error"); }
      this.value = "";
    };
    load();
  },
  settingsDialog(s, el) {
    var self = this;
    var m = modal({
      title: "网站设置 · " + s.alias, width: 700,
      body: '<div class="hub-tabs" style="margin-bottom:14px">' +
        '<div class="hub-tab active" data-t="base">基本</div>' +
        '<div class="hub-tab" data-t="conf">配置文件</div>' +
        '<div class="hub-tab" data-t="auth">密码访问</div>' +
        '<div class="hub-tab" data-t="hotlink">防护与缓存</div>' +
        '<div class="hub-tab" data-t="rewrite">伪静态</div>' +
        '<div class="hub-tab" data-t="subs">子域名</div></div>' +
        '<div id="ws-body"></div>',
      footer: '<button class="btn btn-plain" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    var body = qs("#ws-body", m.el);
    var cur = "base";
    qsa(".hub-tab", m.el).forEach(function(t) {
      t.onclick = function() {
        cur = t.getAttribute("data-t");
        qsa(".hub-tab", m.el).forEach(function(x) { x.classList.remove("active"); });
        t.classList.add("active");
        render();
      };
    });
    var RW_PRESETS = {
      // 全部为 server 级写法(站点已有 location /,预设不能再声明同名 location)
      wordpress: "if (!-e $request_filename) {\n    rewrite ^(.*)$ /index.php?$args last;\n}",
      thinkphp: "if (!-e $request_filename) {\n    rewrite ^(.*)$ /index.php?s=$1 last;\n}",
      laravel: "if (!-e $request_filename) {\n    rewrite ^(.*)$ /index.php?$query_string last;\n}",
      typecho: "if (!-e $request_filename) {\n    rewrite ^(.*)$ /index.php last;\n}",
      empire: "rewrite ^([^\?]*)\\.html$ $1.php last;\nif (!-e $request_filename) {\n    return 404;\n}",
      denyuploads: "location ~* ^/uploads/.*\\.php$ {\n    deny all;\n}",
      redirect301: "# 整站 301 重定向:把 new-domain.com 换成目标域名\nreturn 301 https://new-domain.com$request_uri;"
    };
    function renderRewrite() {
      body.innerHTML = '<div class="settings-section"><div class="settings-section-title">伪静态 / URL 重写</div>' +
        '<div class="form-item"><label>常用预设</label><select class="select" id="ws-rw-preset" style="max-width:240px">' +
        '<option value="">-- 选择预设 --</option>' +
        '<option value="wordpress">WordPress</option><option value="thinkphp">ThinkPHP</option>' +
        '<option value="laravel">Laravel</option><option value="typecho">Typecho</option>' +
        '<option value="empire">帝国 CMS</option><option value="denyuploads">禁止执行 uploads 目录 PHP</option>' +
        '<option value="redirect301">整站 301 重定向(改域名用)</option>' +
        "</select></div>" +
        '<div class="form-item"><label>重写规则(注入 server 块,保存前自动 nginx -t 校验,失败自动回滚)</label>' +
        '<textarea class="textarea mono" id="ws-rw-text" rows="12" spellcheck="false" style="font-size:12px;line-height:1.55">加载中…</textarea></div>' +
        '<div style="display:flex;gap:10px;align-items:center">' +
        '<button class="btn btn-primary btn-sm" id="ws-rw-save">' + I("edit") + " 保存并生效</button>" +
        '<button class="btn btn-plain btn-sm" id="ws-rw-clear">清空(恢复默认)</button></div></div>';
      api("/websites/" + s.id + "/rewrite").then(function(d) {
        var ta = qs("#ws-rw-text", body);
        if (ta) ta.value = d.content || "";
      }).catch(function() {});
      qs("#ws-rw-preset", body).onchange = function() {
        var tpl = RW_PRESETS[this.value];
        if (tpl) qs("#ws-rw-text", body).value = tpl;
      };
      qs("#ws-rw-save", body).onclick = async function() {
        try {
          await api("/websites/" + s.id + "/rewrite", { method: "POST", body: { content: qs("#ws-rw-text", body).value } });
          toast("伪静态规则已保存并生效");
        } catch (e) { toast(e.message, "error", 8000); }
      };
      qs("#ws-rw-clear", body).onclick = async function() {
        qs("#ws-rw-text", body).value = "";
        try {
          await api("/websites/" + s.id + "/rewrite", { method: "POST", body: { content: "" } });
          toast("已恢复默认");
        } catch (e) { toast(e.message, "error", 6000); }
      };
    }
    function render() {
      if (cur === "base") return renderBase();
      if (cur === "conf") return renderConf();
      if (cur === "hotlink") return renderHotlink();
      if (cur === "rewrite") return renderRewrite();
      if (cur === "subs") return renderSubs();
      renderAuth();
    }
    function renderSubs() {
      body.innerHTML = '<div id="ws-subs"><div class="boot-loading" style="height:80px"><div class="spinner"></div></div></div>';
      api("/websites/" + s.id + "/subs").then(function(subs) {
        var list = subs || [];
        var html = '<div class="alert alert-info" style="margin-bottom:12px"><span class="alert-ico">' + I("info") + "</span>" +
          "把其它子域名绑定到本站点的子目录:例如站点 example.com,绑定 <b>m.example.com → m</b> 后,访问 m.example.com 即打开 example.com/m 的内容。<br>" +
          "请先把子域名解析到本服务器;如需独立证书,可单独为其创建站点。</div>" +
          '<div id="subs-list"></div>' +
          '<div class="settings-section"><div class="settings-section-title">添加绑定</div>' +
          '<div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap">' +
          '<input class="input" id="sub-domain" placeholder="子域名,如 m.example.com" style="max-width:280px">' +
          '<span style="color:var(--text-3)">→</span>' +
          '<input class="input mono" id="sub-dir" placeholder="子目录,如 m(留空=站点根目录)" style="max-width:280px">' +
          '<button class="btn btn-sm btn-primary" id="sub-add">' + I("plus") + " 添加</button></div>" +
          '<div class="help">子域名会生成独立的 server 块(共享主站证书与 WAF);目录为站点根目录下的相对路径</div></div>';
        var box = document.getElementById("ws-subs") || document.getElementById("ws-body");
        box.innerHTML = html;
        var renderList = function() {
          var el0 = document.getElementById("subs-list");
          if (!el0) return;
          if (!list.length) { el0.innerHTML = '<div class="settings-section"><div class="help">暂无子域名绑定</div></div>'; return; }
          el0.innerHTML = '<div class="settings-section"><div class="table-wrap"><table class="table"><thead><tr><th>子域名</th><th>绑定目录</th><th style="width:80px">操作</th></tr></thead><tbody>' +
            list.map(function(x, i) {
              return "<tr><td><b class=mono>" + esc(x.sub_domain) + "</b></td><td class=mono>" + esc(x.dir || "(站点根)") + '</td><td><button class="btn btn-xs btn-danger-plain" data-subdel="' + i + '">删除</button></td></tr>';
            }).join("") + "</tbody></table></div></div>";
          el0.querySelectorAll("[data-subdel]").forEach(function(btn) {
            btn.onclick = function() {
              list.splice(parseInt(btn.getAttribute("data-subdel"), 10), 1);
              save(list);
            };
          });
        };
        var save = function(l) {
          return api("/websites/" + s.id + "/subs", { method: "POST", body: { subs: l } }).then(function(ret) {
            list = ret || [];
            toast("已保存并生效");
            renderList();
          }).catch(function(e) { toast(e.message, "error", 6000); });
        };
        renderList();
        var addBtn = document.getElementById("sub-add");
        if (addBtn) addBtn.onclick = function() {
          var dom = (document.getElementById("sub-domain") || {}).value || "";
          var dir = (document.getElementById("sub-dir") || {}).value || "";
          dom = dom.trim().toLowerCase(); dir = dir.trim().replace(/^\/+|\/+$/g, "");
          if (!dom || dom.indexOf(".") < 0) { toast("请输入合法子域名", "error"); return; }
          var dup = list.some(function(x) { return x.sub_domain === dom; });
          if (dup) { toast("该子域名已存在", "error"); return; }
          list.push({ sub_domain: dom, dir: dir });
          save(list);
        };
      }).catch(function(e) {
        var box = document.getElementById("ws-subs");
        if (box) box.innerHTML = emptyState("warn", "加载失败", e.message);
      });
    }
    function renderHotlink() {
      body.innerHTML = '<div class="settings-section"><div class="settings-section-title">资源防盗链</div>' +
        '<div class="form-item"><label style="display:flex;gap:8px;align-items:center"><input type="checkbox" id="ws-hl-on" ' + (s.hotlink_protect ? "checked" : "") + "> 启用防盗链</label>" +
        '<div class="help">图片/音视频/字体等静态资源被其它网站直接引用时返回 403,节省流量;本站页面与搜索引擎正常访问不受影响</div></div>' +
        '<div class="form-item"><label>额外允许的域名(空格分隔)</label>' +
        '<input class="input mono" id="ws-hl-allow" value="' + esc(s.hotlink_allow || "") + '" placeholder="如 partner.com cdn.example.com">' +
        '<div class="help">自身域名与搜索引擎始终放行;此处填写允许引用资源的合作站点域名</div></div></div>' +
        '<div class="settings-section"><div class="settings-section-title">浏览器静态缓存</div>' +
        '<div class="form-item"><label style="display:flex;gap:8px;align-items:center"><input type="checkbox" id="ws-bc-on" ' + (s.browser_cache ? "checked" : "") + "> 启用静态资源 30 天缓存</label>" +
        '<div class="help">css/js/图片/字体等响应头附加 Cache-Control,回访用户不再重复下载,显著提速;站点改版后用户需强刷一次</div></div></div>' +
        '<div class="settings-section"><div class="settings-section-title">流量限制</div>' +
        '<div class="form-item"><label style="display:flex;gap:10px;align-items:center;flex-wrap:wrap"><input type="checkbox" id="ws-rl-on" ' + (s.rate_limit > 0 ? "checked" : "") + '> 启用限速</label>' +
        '<input class="input" type="number" min="1" id="ws-rl-val" value="' + (s.rate_limit > 0 ? s.rate_limit : "") + '" placeholder="如 512" style="max-width:120px"> KB/s' +
        '<div class="help">限制该站点单连接下载速度;0 或关闭 = 不限速。适合控制大文件下载占满带宽的场景</div></div></div>' +
        '<div class="settings-section"><div class="settings-section-title">维护模式</div>' +
        '<div class="form-item"><label style="display:flex;gap:10px;align-items:center"><input type="checkbox" id="ws-maint-on" ' + (s.maintenance ? "checked" : "") + '> 开启维护页</label>' +
        '<div class="help">开启后所有访问返回 503「网站维护中」页面,适合改版/调试时挂出;注意维护期间 Let\'s Encrypt 续期验证也会被拦</div></div></div>' +
        '<div class="settings-section"><div class="settings-section-title">安全响应头</div>' +
        '<div class="form-item"><label style="display:flex;gap:10px;align-items:center"><input type="checkbox" id="ws-sec-on" ' + (s.sec_headers ? "checked" : "") + '> 添加基础安全响应头</label>' +
        '<div class="help">X-Frame-Options(防点击劫持)+ X-Content-Type-Options(防 MIME 嗅探)+ Referrer-Policy(限制 referer 泄露)</div></div></div>' +
        '<button class="btn btn-primary btn-sm" id="ws-hl-save">' + I("edit") + " 保存并生效</button>";
      qs("#ws-hl-save", body).onclick = async function() {
        try {
          await api("/websites/" + s.id + "/hotlink", { method: "POST", body: {
            enabled: qs("#ws-hl-on", body).checked,
            allow: qs("#ws-hl-allow", body).value.trim(),
          }});
          await api("/websites/" + s.id + "/extras", { method: "POST", body: {
            http2: !!s.http2, hsts: !!s.hsts,
            index_files: s.index_files || "",
            browser_cache: qs("#ws-bc-on", body).checked,
            rate_limit: qs("#ws-rl-on", body).checked ? (parseInt(qs("#ws-rl-val", body).value) || 0) : 0,
            maintenance: qs("#ws-maint-on", body).checked,
            sec_headers: qs("#ws-sec-on", body).checked,
          }});
          toast("设置已保存并生效");
          s.hotlink_protect = qs("#ws-hl-on", body).checked;
          s.hotlink_allow = qs("#ws-hl-allow", body).value.trim();
          s.browser_cache = qs("#ws-bc-on", body).checked;
          s.rate_limit = qs("#ws-rl-on", body).checked ? (parseInt(qs("#ws-rl-val", body).value) || 0) : 0;
          s.maintenance = qs("#ws-maint-on", body).checked;
          s.sec_headers = qs("#ws-sec-on", body).checked;
        } catch (e) { toast(e.message, "error", 6000); }
      };
    }
    async function renderBase() {
      let upstreamsText = "";
      if (s.type === "proxy") {
        try { upstreamsText = (await api("/websites/" + s.id + "/upstreams")).content || ""; } catch (e) {}
      }
      body.innerHTML = '<div class="form-item"><label>域名(每行一个)</label>' +
        '<textarea class="textarea mono" id="ws-domains" rows="3">' + esc(s.domain.split(" ").join("\n")) + "</textarea>" +
        '<div class="help">修改后自动重载 Nginx 生效;请确保域名已解析到本服务器</div></div>' +
        '<div class="form-item"><label>转换站点类型</label><select class="select" id="ws-type" style="max-width:320px">' +
          ["static", "php", "proxy"].map(function(t) {
            var names = { static: "静态站点", php: "PHP 站点(容器运行时)", proxy: "反向代理" };
            return '<option value="' + t + '"' + (t === s.type ? " selected" : "") + ">" + names[t] + (t === s.type ? "(当前)" : "") + "</option>";
          }).join("") + '</select>' +
          '<div id="ws-type-params"></div>' +
          '<div class="help">转换即改写站点配置并立即生效,原目录与数据保留;PHP 站点 = 反代到容器 PHP 运行时(可回选版本)</div></div>' +
        (s.type === "proxy"
          ? '<div class="form-item required"><label>代理目标</label><input class="input mono" id="ws-proxy" value="' + esc(s.proxy_to || "") + '" placeholder="http://127.0.0.1:8080"></div>' +
            '<div class="form-item"><label>发送域名 Host</label><input class="input mono" id="ws-proxy-host" value="' + esc(s.proxy_host || "") + '" placeholder="$http_host(默认;可写域名如 api.example.com 或 $host)">' +
            '<div class="help">转发给上游的 Host 请求头;留空使用访客原始域名</div></div>' +
            '<div class="form-item"><label>负载均衡后端(可选)</label>' +
            '<textarea class="textarea mono" id="ws-upstreams" rows="3" placeholder="每行一个:\nhttp://127.0.0.1:8080 5\nhttp://127.0.0.1:8081">' + esc(upstreamsText) + '</textarea>' +
            '<div class="help">每行「地址 [权重]」,多个后端自动加权轮询;配置后替代上方代理目标。格式错误会被 nginx 校验拦截并回滚</div></div>'
          : s.type === "php"
          ? '<div class="form-item"><label>PHP 版本</label><select class="select" id="ws-php-key" style="max-width:320px"></select>' +
            '<div class="help">切换后自动重载 Nginx 生效;选项为当前运行中的 PHP 版本(多版本可在应用商店安装)</div></div>' +
            '<div class="form-item"><label>发送域名 Host</label><input class="input mono" id="ws-proxy-host" value="' + esc(s.proxy_host || "") + '" placeholder="$http_host(默认;可写域名如 api.example.com 或 $host)">' +
            '<div class="help">转发给 PHP 运行时的 Host 请求头;留空使用访客原始域名</div></div>'
          : "") +
        '<div class="form-item"><label>监听端口</label><input class="input" id="ws-port" type="number" value="' + (s.port || 80) + '" style="max-width:180px"></div>' +
        (s.type === "static"
          ? '<div class="form-item"><label>默认文档</label><input class="input mono" id="ws-index" value="' + esc(s.index_files || "index.html index.htm") + '" style="max-width:420px"><div class="help">按顺序匹配,空格分隔,如 index.html index.htm default.html</div></div>'
          : "") +
        '<div class="form-item"><label>备注</label><input class="input" id="ws-remark" value="' + esc(s.remark || "") + '"></div>' +
        '<button class="btn btn-primary btn-sm" id="ws-base-save">' + I("edit") + " 保存并生效</button>";
      if (s.type === "php") {
        (async function() {
          var sel = document.getElementById("ws-php-key");
          if (!sel) return;
          try {
            var insts = await api("/apps/instances");
            var phps = (insts || []).filter(function(x) { return (x.key === "php" || /^php[0-9]+$/.test(x.key)) && x.status === "running"; });
            var cur = s.php_key || "php";
            if (!phps.some(function(x) { return x.key === cur; })) phps.unshift({ key: cur, params: { Version: cur === "php" ? "默认" : cur.replace("php", "") }, status: "running" });
            sel.innerHTML = phps.map(function(x) {
              var v = (x.params && x.params.Version) || x.key;
              return '<option value="' + esc(x.key) + '"' + (x.key === cur ? " selected" : "") + '>PHP ' + esc(v) + '</option>';
            }).join("");
          } catch (e) { sel.innerHTML = '<option value="' + esc(s.php_key || "php") + '">PHP(当前)</option>'; }
        })();
      }
      // 转换类型:选择非当前类型时,渲染对应的目标参数输入
      var typeSel = document.getElementById("ws-type");
      if (typeSel) {
        var renderTypeParams = function() {
          var box = document.getElementById("ws-type-params");
          var nt = typeSel.value;
          if (nt === s.type) { box.innerHTML = ""; return; }
          if (nt === "proxy") {
            box.innerHTML = '<div class="form-item required" style="margin-top:8px"><label>反代目标</label><input class="input mono" id="ws-type-target" placeholder="http://127.0.0.1:8080"></div>' +
              '<div class="form-item"><label>发送域名 Host</label><input class="input mono" id="ws-type-host" placeholder="$http_host(默认)"></div>';
          } else if (nt === "php") {
            box.innerHTML = '<div class="form-item" style="margin-top:8px"><label>PHP 版本</label><select class="select" id="ws-type-php" style="max-width:320px"><option value="php">默认</option></select></div>' +
              '<div class="form-item"><label>发送域名 Host</label><input class="input mono" id="ws-type-host" placeholder="$http_host(默认)"></div>';
            (async function() {
              var sel2 = document.getElementById("ws-type-php");
              try {
                var insts = await api("/apps/instances");
                var phps = (insts || []).filter(function(x) { return (x.key === "php" || /^php[0-9]+$/.test(x.key)) && x.status === "running"; });
                if (phps.length) sel2.innerHTML = phps.map(function(x) {
                  var v = (x.params && x.params.Version) || x.key;
                  return '<option value="' + esc(x.key) + '">PHP ' + esc(v) + '</option>';
                }).join("");
              } catch (e) {}
            })();
          } else {
            box.innerHTML = '<div class="help" style="margin-top:8px">转静态后将按站点根目录提供文件</div>';
          }
        };
        typeSel.onchange = renderTypeParams;
        renderTypeParams();
      }
      qs("#ws-base-save", body).onclick = async function() {
        var domains = qs("#ws-domains", body).value.split("\n").map(function(x) { return x.trim(); }).filter(Boolean).join(" ");
        if (!domains) { toast("域名不能为空", "error"); return; }
        // 类型转换:先转换(带目标参数与 Host),完成后整页刷新
        var nt = typeSel ? typeSel.value : s.type;
        if (nt !== s.type) {
          try {
            await api("/websites/" + s.id + "/type", { method: "POST", body: {
              type: nt,
              target: document.getElementById("ws-type-target") ? document.getElementById("ws-type-target").value.trim() : "",
              php_key: document.getElementById("ws-type-php") ? document.getElementById("ws-type-php").value : "",
              proxy_host: document.getElementById("ws-type-host") ? document.getElementById("ws-type-host").value.trim() : "",
            }});
            toast("已转换为 " + nt + " 类型");
            m.close();
            self.render(el);
          } catch (e) { toast(e.message, "error", 6000); }
          return;
        }
        try {
          await api("/websites/" + s.id + "/domains", { method: "POST", body: { domain: domains } });
          if (s.type !== "php") {
            await api("/websites/" + s.id, { method: "PUT", body: {
              proxy_to: s.type === "proxy" ? qs("#ws-proxy", body).value.trim() : (s.proxy_to || ""),
              proxy_host: s.type === "proxy" && qs("#ws-proxy-host", body) ? qs("#ws-proxy-host", body).value.trim() : (s.proxy_host || ""),
              port: Number(qs("#ws-port", body).value) || 80,
              remark: qs("#ws-remark", body).value.trim(),
            }});
          } else {
            // PHP 站点:提交所选 PHP 版本(端口/备注/Host 随同保存)
            var pk = document.getElementById("ws-php-key");
            await api("/websites/" + s.id, { method: "PUT", body: {
              proxy_to: s.proxy_to || "",
              proxy_host: qs("#ws-proxy-host", body) ? qs("#ws-proxy-host", body).value.trim() : (s.proxy_host || ""),
              port: Number(qs("#ws-port", body).value) || 80,
              remark: qs("#ws-remark", body) ? qs("#ws-remark", body).value.trim() : (s.remark || ""),
              php_key: pk ? pk.value : (s.php_key || "php"),
            }});
          }
          if (s.type === "proxy") {
            await api("/websites/" + s.id + "/upstreams", { method: "POST", body: { content: qs("#ws-upstreams", body) ? qs("#ws-upstreams", body).value : "" } });
          }
          if (s.type === "static") {
            await api("/websites/" + s.id + "/extras", { method: "POST", body: {
              http2: !!s.http2, hsts: !!s.hsts,
              index_files: qs("#ws-index", body).value.trim(),
            }});
          }
          toast("网站设置已保存并生效");
          m.close();
          self.render(el);
        } catch (e) { toast(e.message, "error", 5000); }
      };
    }
    function renderConf() {
      body.innerHTML = '<div class="help" style="margin-bottom:8px">站点 Nginx 配置,保存前自动校验语法,失败会还原并提示</div>' +
        '<textarea class="textarea mono" id="ws-conf" rows="20" spellcheck="false" style="font-size:12px;line-height:1.55">加载中…</textarea>' +
        '<div style="margin-top:10px"><button class="btn btn-primary btn-sm" id="ws-conf-save">' + I("edit") + " 保存并重载</button></div>";
      api("/websites/" + s.id + "/conf").then(function(d) {
        var ta = qs("#ws-conf", body);
        if (ta) {
          ta.value = d.content || "";
          var cm = cmUpgrade(ta, "nginx", { "Ctrl-S": function(c) { qs("#ws-conf-save", body).click(); } });
          if (cm) {
            m.setVeto(function() {
              if (cm.isClean()) return false;
              confirmDlg("配置文件有<b>未保存的修改</b>,确定放弃并关闭吗?", { danger: true }).then(function(ok) {
                if (ok) { cm.markClean(); m.doClose(); }
              });
              return true;
            });
          }
        }
      }).catch(function(e) {
        var ta = qs("#ws-conf", body);
        if (ta) ta.value = "加载失败: " + e.message;
      });
      qs("#ws-conf-save", body).onclick = async function() {
        try {
          await api("/websites/" + s.id + "/conf", { method: "POST", body: { content: qs("#ws-conf", body).value } });
          var cm2 = qs("#ws-conf", body)._cm;
          if (cm2) cm2.markClean();
          toast("配置已保存并重载 Nginx");
        } catch (e) { toast(e.message, "error", 6000); }
      };
    }
    function renderAuth() {
      body.innerHTML = '<div class="form-item"><label class="radio-line"><input type="checkbox" id="ws-auth-on" ' + (s.auth_enabled ? "checked" : "") + "> 启用密码访问(浏览器标准弹窗认证)</label></div>" +
        '<div id="ws-auth-fields" style="' + (s.auth_enabled ? "" : "display:none") + '">' +
        '<div class="form-item"><label>用户名</label><input class="input" id="ws-auth-user" value="' + esc(s.auth_user || "") + '"></div>' +
        '<div class="form-item"><label>密码</label><input class="input" id="ws-auth-pass" type="password" placeholder="' + (s.auth_enabled ? "留空沿用已有密码" : "至少 4 位") + '"></div>' +
        '<div class="help">启用后访问站点需输入账号密码;关闭密码登录不影响面板</div></div>' +
        '<button class="btn btn-primary btn-sm" id="ws-auth-save">保 存</button>';
      qs("#ws-auth-on", body).onchange = function() { qs("#ws-auth-fields", body).style.display = this.checked ? "" : "none"; };
      qs("#ws-auth-save", body).onclick = async function() {
        try {
          await api("/websites/" + s.id + "/auth", { method: "POST", body: {
            enabled: qs("#ws-auth-on", body).checked,
            user: qs("#ws-auth-user", body).value.trim(),
            password: qs("#ws-auth-pass", body).value,
          }});
          toast("密码访问已更新");
        } catch (e) { toast(e.message, "error"); }
      };
    }
    render();
  },
  logsDialog() {
    var self = this;
    const m = modal({
      title: "站点日志(全部站点)", width: 860,
      body: `
        <div style="display:flex;align-items:center;gap:10px;margin-bottom:12px;flex-wrap:wrap">
          <div class="tabs" style="margin-bottom:0;flex:1">
            <div class="tab active" data-t="access">访问日志</div>
            <div class="tab" data-t="error">错误日志</div>
          </div>
          <button class="btn btn-sm btn-plain" data-act="autoclean">${I("clock")} 自动清理</button>
          <button class="btn btn-sm btn-danger-plain" data-act="clear">${I("trash")} 清空当前日志</button>
        </div>
        <div class="log-view" id="site-log-view" style="max-height:56vh">加载中...</div>`,
      footer: '<button class="btn btn-sm btn-plain" data-act="refresh">刷新</button><button class="btn btn-primary" data-act="close">关 闭</button>',
    });
    var logType = "access";
    var load = function () {
      fetch("/api/v1/websites/nginx-logs?type=" + logType + "&tail=300", { headers: { Authorization: "Bearer " + localStorage.getItem(TOKEN_KEY) } })
        .then(function (r) { return r.json(); })
        .then(function (d) {
          qs("#site-log-view", m.el).textContent = d.code === 0 ? (d.data.content || "(空)") : d.message;
        });
    };
    load();
    qsa(".tab", m.el).forEach(function (t) {
      t.onclick = function () {
        qsa(".tab", m.el).forEach(function (x) { x.classList.remove("active"); });
        t.classList.add("active");
        logType = t.dataset.t;
        load();
      };
    });
    qs('[data-act="refresh"]', m.el).onclick = load;
    qs('[data-act="close"]', m.el).onclick = m.close;
    qs('[data-act="clear"]', m.el).onclick = async function () {
      var name = logType === "access" ? "访问日志" : "错误日志";
      if (!(await confirmDlg("清空全部站点的<b>" + name + "</b>?<br><span style='font-size:12px;color:var(--text-3)'>日志文件将被清空(保留文件本身),站点继续写入口常;此操作不可恢复。</span>", { danger: true }))) return;
      var btn = this;
      btn.disabled = true;
      try {
        await api("/websites/nginx-logs/clear", { method: "POST", body: { type: logType } });
        toast("日志已清空");
        load();
      } catch (e) { toast(e.message, "error", 6000); }
      btn.disabled = false;
    };
    qs('[data-act="autoclean"]', m.el).onclick = function () {
      var cfg = modal({
        title: "站点日志自动清理", width: 460,
        body: '<div id="lgac-body">' + skeletonTable(2, 3) + "</div>",
        footer: '<button class="btn btn-plain" data-act="close">取 消</button><button class="btn btn-primary" data-act="save">保 存</button>',
      });
      qs('[data-act="close"]', cfg.el).onclick = cfg.close;
      (async function() {
        var d = {};
        try { d = await api("/websites/nginx-logs/autoclean"); } catch (e) { qs("#lgac-body", cfg.el).innerHTML = emptyState("warn", "加载失败", e.message); return; }
        qs("#lgac-body", cfg.el).innerHTML =
          '<div class="form-item"><label style="display:flex;align-items:center;gap:8px"><span class="switch' + (d.enabled ? " on" : "") + '" id="lgac-on"></span><span>启用每日自动清理</span></label>' +
          '<div class="help">每天自动检查一次:修改时间超过保留天数的站点日志文件将被清空(文件保留,站点继续写入口常)</div></div>' +
          '<div class="form-item"><label>日志保留天数</label><input class="input" id="lgac-days" type="number" value="' + (d.keep_days || 7) + '" style="max-width:140px"> <span class="help" style="margin:0">天(1-365)</span></div>' +
          (d.last_run ? '<div class="help">上次执行:' + esc(d.last_run) + "</div>" : "");
        var sw = qs("#lgac-on", cfg.el);
        sw.onclick = function() { sw.classList.toggle("on"); };
      })();
      qs('[data-act="save"]', cfg.el).onclick = async function() {
        var enabled = qs("#lgac-on", cfg.el).classList.contains("on");
        var days = parseInt(qs("#lgac-days", cfg.el).value, 10) || 7;
        var btn = this;
        btn.disabled = true;
        try {
          var d = await api("/websites/nginx-logs/autoclean", { method: "POST", body: { enabled: enabled, keep_days: days } });
          toast(enabled ? "自动清理已启用,本次已清理 " + (d.cleaned || 0) + " 个过期日志" : "自动清理已关闭");
          cfg.close();
        } catch (e) { toast(e.message, "error", 6000); btn.disabled = false; }
      };
    };
  },
  async statsDialog() {
    var H = { Authorization: "Bearer " + localStorage.getItem(TOKEN_KEY) };
    var m = modal({
      title: "网站访问统计", width: 760,
      body: '<div id="ws-stats" style="min-height:200px"><div class="boot-loading" style="height:100px"><div class="spinner"></div><p>解析中...</p></div></div>',
      footer: '<button class="btn btn-sm btn-plain" data-act="refresh">刷新</button><button class="btn btn-primary" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    var load = async function() {
      var box = qs("#ws-stats", m.el);
      try {
        var d = await fetch("/api/v1/websites/web-stats?lines=10000", { headers: H }).then(function(x){ return x.json() });
        if (d.code !== 0) { box.innerHTML = emptyState("warn", "加载失败", d.message || ""); return; }
        var s = d.data;
        box.innerHTML =
          '<div class="grid-4" style="margin-bottom:16px">' +
            '<div style="text-align:center"><div style="font-size:26px;font-weight:700;color:var(--primary)">' + s.total_pv + '</div><div style="font-size:12px;color:var(--text-3)">总请求(PV)</div></div>' +
            '<div style="text-align:center"><div style="font-size:26px;font-weight:700;color:var(--success)">' + s.total_uv + '</div><div style="font-size:12px;color:var(--text-3)">独立 IP(UV)</div></div>' +
            '<div style="text-align:center"><div style="font-size:26px;font-weight:700;color:var(--warning)">' + fmtBytes(s.total_bytes) + '</div><div style="font-size:12px;color:var(--text-3)">总流量</div></div>' +
            '<div style="text-align:center"><div style="font-size:26px;font-weight:700;color:var(--danger)">' + (s.status_dist["4xx"]||0) + " / " + (s.status_dist["5xx"]||0) + '</div><div style="font-size:12px;color:var(--text-3)">4xx / 5xx</div></div>' +
          "</div>" +
          '<div class="grid-2" style="margin-top:12px">' +
            '<div><div class="card-title" style="font-size:13px;margin-bottom:8px">Top 10 IP</div>' +
            (s.top_ips||[]).map(function(r,i){ return '<div style="display:flex;justify-content:space-between;padding:4px 0;font-size:13px;' + (i%2?'background:#f8f9fa33;':'') + '"><span class="mono">' + esc(r.key) + '</span><b>' + r.count + '</b></div>'; }).join("") +
            '</div><div><div class="card-title" style="font-size:13px;margin-bottom:8px">Top 10 URI</div>' +
            (s.top_uris||[]).map(function(r,i){ return '<div style="display:flex;justify-content:space-between;padding:4px 0;font-size:13px;overflow:hidden"><span class="mono" style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:240px">' + esc(r.key) + '</span><b>' + r.count + '</b></div>'; }).join("") +
            "</div></div>";
      } catch (e) { box.innerHTML = emptyState("warn", "加载失败", e.message); }
    };
    load();
    qs('[data-act="refresh"]', m.el).onclick = load;
  },
  /* 网站全局设置:防恶意解析 / 全站 Gzip / Nginx 管理入口 */
  async globalDialog(el) {
    let ap = false, gz = false;
    try {
      const results = await Promise.all([api("/websites/anti-parsing"), api("/websites/gzip")]);
      ap = !!results[0].enabled; gz = !!results[1].enabled;
    } catch (e) {}
    const m = modal({
      title: "网站全局设置", width: 620,
      body: `
        <div class="settings-section"><div class="settings-section-title">防恶意解析</div>
          <div class="form-item"><label style="display:flex;gap:10px;align-items:center"><span class="switch" id="gp-anti"></span><span>拦截未绑定的域名与裸 IP 访问</span></label>
          <div class="help">开启后,别人把垃圾域名解析到本机 IP 或直接用 IP 访问时,连接将被直接断开(不返回任何内容),防止蹭流量/蹭证书;面板入口不受影响</div></div></div>
        <div class="settings-section"><div class="settings-section-title">Gzip 压缩</div>
          <div class="form-item"><label style="display:flex;gap:10px;align-items:center"><span class="switch" id="gp-gzip"></span><span>全站文本资源压缩传输</span></label>
          <div class="help">HTML/CSS/JS/JSON/SVG/字体等启用 gzip,传输体积平均减少 60% 以上,回访加载更快;图片视频等已压缩格式不重复处理</div></div></div>
        <div class="settings-section"><div class="settings-section-title">Nginx 管理</div>
          <div class="form-item" style="margin-bottom:0"><a class="btn btn-sm btn-plain" href="#/apps" id="gp-nginx-link">${I("tool")} Nginx 主配置 / 负载状态 / 错误日志(在 应用商店→已安装 中管理)</a></div></div>`,
      footer: '<button class="btn btn-plain" data-act="close">关 闭</button>',
    });
    qs("#gp-anti", m.el).classList.toggle("on", ap);
    qs("#gp-gzip", m.el).classList.toggle("on", gz);
    qs("#gp-anti", m.el).onclick = async function() {
      const on = !this.classList.contains("on");
      try {
        await api("/websites/anti-parsing", { method: "POST", body: { enabled: on } });
        this.classList.toggle("on", on);
        toast(on ? "防恶意解析已开启" : "防恶意解析已关闭");
      } catch (e) { toast(e.message, "error"); }
    };
    qs("#gp-gzip", m.el).onclick = async function() {
      const on = !this.classList.contains("on");
      try {
        await api("/websites/gzip", { method: "POST", body: { enabled: on } });
        this.classList.toggle("on", on);
        toast(on ? "Gzip 压缩已开启" : "Gzip 压缩已关闭");
      } catch (e) { toast(e.message, "error"); }
    };
    const nl = qs("#gp-nginx-link", m.el);
    if (nl) nl.onclick = function(e) { e.preventDefault(); m.close(); location.hash = "#/apps"; };
    qs('[data-act="close"]', m.el).onclick = m.close;
  },
  /* HTTPS 证书绑定弹窗:免费证书 / 自有证书 / 自签 三区分离,新手按需选择 */
  async sslDialog(site, el) {
    let certs = [];
    let acmeCfg = { email: "", staging: false, registered: false };
    try {
      const results = await Promise.all([api("/ssl/certs"), api("/ssl/acme/config").catch(() => null)]);
      certs = results[0] || [];
      if (results[1]) acmeCfg = results[1];
    } catch (e) {}
    const mainDomain = site.domain.split(" ")[0];
    const m = modal({
      title: "HTTPS 设置 · " + mainDomain, width: 600,
      body: `
        <div class="form-item"><label>绑定状态</label>
          <div>${site.ssl_cert_id ? '<span class="tag tag-success">已启用 HTTPS</span>' : '<span class="tag tag-info">未启用</span>'}</div></div>
        <div class="form-item"><label>当前使用的证书</label>
          <select class="select" id="ssl-cert"><option value="">不启用 HTTPS</option>
            ${certs.map((c) => '<option value="' + esc(c.id) + '"' + (c.id === site.ssl_cert_id ? " selected" : "") + ">" + esc(c.name) + (c.expire ? "(到期 " + esc(c.expire) + (c.acme ? " · LE" : "") + (c.profile === "shortlived" ? " · 短期" : "") + ")" : "") + "</option>").join("")}
          </select>
          <div class="help" id="ssl-cert-count">已有 ${certs.length} 张证书;LE 证书剩余不足 30 天自动续期(IP 短期证书不足 3 天自动续期)</div></div>

        <div class="waf-set-sec" style="padding:14px 16px 12px"><div class="waf-sec-head">${I("bolt")}免费证书(ACME)<span class="tag tag-success" style="margin-left:2px">推荐</span></div>
          <div class="settings-hint" style="margin:0 0 10px">自动申请、自动续期;Let's Encrypt 适合绝大多数网站,ZeroSSL / Google / BuyPass 适合有偏好的用户(需在其官网获取 EAB 凭据)</div>
          ${/^\d{1,3}(\.\d{1,3}){3}$/.test(mainDomain) ? '<div class="help" style="margin:-4px 0 10px;color:var(--warn,#f59e0b)">当前站点绑定的是 IP 地址:将自动申请 Let\'s Encrypt IP 证书(shortlived 档案,有效期约 6 天,到期前 3 天自动续期;需该 IP 的 80 端口对外开放)</div>' : ""}
          <div class="form-item"><label>证书服务商(CA)</label>
            <select class="select" id="acme-provider" style="max-width:320px"></select>
            <div class="help" id="acme-provider-hint"></div></div>
          <div id="acme-eab-row" style="display:none">
            <div class="form-item"><label>EAB KeyID</label><input class="input" id="acme-eab-kid" value="${esc(acmeCfg.eab_key_id || "")}" placeholder="在 CA 官网获取" style="max-width:320px"></div>
            <div class="form-item"><label>EAB HMAC</label><input class="input" id="acme-eab-hmac" value="${esc(acmeCfg.eab_hmac || "")}" placeholder="在 CA 官网获取" style="max-width:320px"></div>
          </div>
          <div class="form-item"><label>申请邮箱</label>
            <input class="input" id="acme-email" value="${esc(acmeCfg.email || "")}" placeholder="用于到期提醒,如 me@example.com"></div>
          <div class="form-item"><label>验证方式</label>
            <select class="select" id="acme-method" style="max-width:320px">
              <option value="http">HTTP 文件验证(推荐,需域名解析到本服务器且 80 端口可达)</option>
              <option value="dns">DNS 手动验证(支持通配符/无法开放 80 端口的场景,签发时需添加 TXT 记录)</option>
            </select></div>
          <div id="acme-alias-row" style="display:none">
            <div class="form-item"><label>DNS 别名域名(可选)</label>
              <input class="input" id="acme-alias" placeholder="如 acme.your-domain.com(可选)" style="max-width:320px">
              <div class="help">别名模式:在主域 DNS 添加 CNAME「_acme-challenge.主域名 → _acme-challenge.别名域名」,TXT 记录即可统一维护在别名域名下,主域 DNS 无需每次改动</div></div>
          </div>
          <label class="radio-line"><input type="checkbox" id="acme-staging" ${acmeCfg.staging ? "checked" : ""}> 使用测试环境(staging,不限频但浏览器不信任,验证流程用)</label>
          <div style="margin-top:10px"><button class="btn btn-sm btn-primary" id="ssl-acme-issue">${I("bolt")} 申请证书并启用 HTTPS</button></div></div>

        <div class="waf-set-sec" style="padding:14px 16px 12px;margin-top:12px"><div class="waf-sec-head">${I("upload")}自有证书(商业证书)</div>
          <div class="settings-hint" style="margin:0 0 10px">已购买的商业证书(如阿里云/腾讯云/StormBytes 签发):下载 PEM 格式后,选择文件或直接粘贴内容</div>
          <div style="margin-bottom:8px"><button class="btn btn-sm btn-plain" id="ssl-pick-file">${I("upload")} 选择证书文件</button>
            <span class="help" style="margin:0 0 0 8px">支持 .pem / .crt / .key / .txt,自动区分证书与私钥</span></div>
          <textarea class="textarea" id="ssl-cert-pem" rows="3" placeholder="证书内容(BEGIN CERTIFICATE ...)"></textarea>
          <textarea class="textarea" id="ssl-key-pem" rows="3" placeholder="私钥内容(BEGIN PRIVATE KEY ...)" style="margin-top:6px"></textarea>
          <div style="margin-top:10px"><button class="btn btn-sm btn-primary" id="ssl-upload">${I("bolt")} 保存并启用 HTTPS</button></div></div>

        <div class="waf-set-sec" style="padding:14px 16px 12px;margin-top:12px"><div class="waf-sec-head">${I("key")}自签证书<span class="settings-hint" style="margin:0 0 0 4px">仅本地/内网测试用,浏览器会提示不安全</span></div>
          <button class="btn btn-sm btn-plain" id="ssl-selfsign">${I("key")} 生成自签证书(10年)并启用</button></div>

        <div style="margin-top:14px">
          <div class="form-item"><label class="radio-line"><input type="checkbox" id="ssl-redirect" ${site.ssl_redirect ? "checked" : ""}> HTTP 强制跳转 HTTPS(仅监听 80 端口时生效)</label></div>
          <div class="form-item"><label class="radio-line"><input type="checkbox" id="ssl-http2" ${site.http2 ? "checked" : ""}> HTTP/2(多路复用,页面加载更快)</label></div>
          <div class="form-item"><label class="radio-line"><input type="checkbox" id="ssl-hsts" ${site.hsts ? "checked" : ""}> HSTS(强制浏览器一年内只用 HTTPS 访问本站,开启后短期难以回退)</label></div></div>`,
      footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">保 存</button>',
    });
    if (site.ssl_cert_id) {
      const dl = document.createElement("button");
      dl.className = "btn btn-plain";
      dl.style.marginRight = "auto";
      dl.innerHTML = I("download") + " 下载证书";
      dl.onclick = () => window.open("/api/v1/ssl/certs/" + site.ssl_cert_id + "/download?token=" + encodeURIComponent(localStorage.getItem(TOKEN_KEY)), "_blank");
      const foot = m.el.querySelector(".modal-footer");
      foot.insertBefore(dl, foot.firstChild);
    }
    qs('[data-act="no"]', m.el).onclick = m.close;
    const persist = async (certId, redirect) => {
      try {
        await api("/websites/" + site.id + "/ssl", { method: "POST", body: { cert_id: certId, redirect } });
        try {
          await api("/websites/" + site.id + "/extras", { method: "POST", body: {
            http2: qs("#ssl-http2", m.el).checked,
            hsts: qs("#ssl-hsts", m.el).checked,
            index_files: site.index_files || "",
          }});
        } catch (e2) { toast(e2.message, "error", 6000); }
        toast("HTTPS 设置已保存并生效");
        m.close();
        this.render(el);
      } catch (e) { toast(e.message, "error", 5000); }
    };
    // ① CA 下拉与联动
    var providerMeta = {};
    (async function() {
      try {
        var list = await api("/ssl/acme/providers");
        var sel = qs("#acme-provider", m.el);
        sel.innerHTML = list.map(function(p2) {
          providerMeta[p2.name] = p2;
          return '<option value="' + esc(p2.name) + '"' + ((acmeCfg.provider || "letsencrypt") === p2.name ? " selected" : "") + ">" + esc(p2.name) + (p2.staging ? "(测试)" : "") + "</option>";
        }).join("");
        var sync = function() {
          var key = sel.value;
          var meta = providerMeta[key] || {};
          qs("#acme-provider-hint", m.el).textContent = meta.need_eab ? "该服务商需要在官网获取 EAB 凭据后填写到下方" : "无需额外凭据,填邮箱即可申请";
          qs("#acme-eab-row", m.el).style.display = meta.need_eab ? "" : "none";
        };
        sel.onchange = sync;
        sync();
      } catch (e) { /* 下拉失败时保底 letsencrypt */ }
    })();
    var methodSel = qs("#acme-method", m.el);
    methodSel.onchange = function() {
      qs("#acme-alias-row", m.el).style.display = this.value === "dns" ? "" : "none";
    };
    // ② 一键申请
    qs("#ssl-acme-issue", m.el).onclick = async () => {
      const email = qs("#acme-email", m.el).value.trim();
      const provider = qs("#acme-provider", m.el).value;
      const staging = qs("#acme-staging", m.el).checked || (providerMeta[provider] || {}).staging || false;
      const method = methodSel.value;
      const alias = qs("#acme-alias", m.el).value.trim();
      if (!email) { toast("请填写申请邮箱", "error"); return; }
      const body = { email, staging, provider, eab_key_id: qs("#acme-eab-kid", m.el).value.trim(), eab_hmac: qs("#acme-eab-hmac", m.el).value.trim() };
      try { await api("/ssl/acme/config", { method: "POST", body }); }
      catch (e) { toast(e.message, "error", 6000); return; }
      toast(method === "dns" ? "正在创建订单,请在任务日志中查看需要添加的 TXT 记录..." : "正在发起申请...", "info", 6000);
      try {
        const d = await api("/ssl/acme/issue", { method: "POST", body: { site_id: site.id, staging, provider, method, alias } });
        m.close();
        taskModal(d.task_id, (status) => {
          if (status === "success") {
            toast("证书申请成功,已启用 HTTPS");
            setTimeout(() => window.location.reload(), 800);
          }
        });
      } catch (e) { toast(e.message, "error", 6000); }
    };
    // ② 自有证书:本地文件选择(自动识别证书/私钥并填充)
    qs("#ssl-pick-file", m.el).onclick = () => {
      const inp = document.createElement("input");
      inp.type = "file"; inp.multiple = true;
      inp.accept = ".pem,.crt,.cer,.key,.txt";
      inp.onchange = () => {
        let pending = inp.files.length;
        [...inp.files].forEach((f) => {
          const reader = new FileReader();
          reader.onload = () => {
            const txt = String(reader.result || "").trim();
            if (/PRIVATE KEY/.test(txt)) {
              qs("#ssl-key-pem", m.el).value = txt;
              toast(f.name + " 已识别为私钥", "success");
            } else if (/BEGIN CERTIFICATE/.test(txt)) {
              qs("#ssl-cert-pem", m.el).value = txt;
              toast(f.name + " 已识别为证书", "success");
            } else {
              toast(f.name + ": 无法识别(需 PEM 格式的证书或私钥)", "error", 6000);
            }
            if (--pending === 0) inp.remove();
          };
          reader.readAsText(f);
        });
      };
      document.body.appendChild(inp);
      inp.click();
      setTimeout(function() { if (inp.parentNode) inp.remove(); }, 60000);
    };
    // ② 保存自有证书
    qs("#ssl-upload", m.el).onclick = async () => {
      const cert = qs("#ssl-cert-pem", m.el).value.trim(), key = qs("#ssl-key-pem", m.el).value.trim();
      if (!cert || !key) { toast("请填写(或选择)证书与私钥内容", "error"); return; }
      b_dis: {
        const btn = qs("#ssl-upload", m.el);
        btn.disabled = true;
        try {
          const info = await api("/ssl/upload", { method: "POST", body: { name: mainDomain, cert, key } });
          toast("证书上传成功");
          await persist(info.id, qs("#ssl-redirect", m.el).checked);
        } catch (e) {
          toast(e.message, "error");
          btn.disabled = false;
        }
      }
    };
    // ③ 自签
    qs("#ssl-selfsign", m.el).onclick = async () => {
      try {
        const info = await api("/ssl/selfsign", { method: "POST", body: { domain: mainDomain, days: 3650 } });
        toast("自签证书已生成");
        await persist(info.id, qs("#ssl-redirect", m.el).checked);
      } catch (e) { toast(e.message, "error"); }
    };
    qs('[data-act="yes"]', m.el).onclick = () => persist(qs("#ssl-cert", m.el).value, qs("#ssl-redirect", m.el).checked);
  },
  editDialog(s, el) {
    const m = modal({
      title: "编辑站点 · " + s.alias, width: 520,
      body: `
        <div class="form-item"><label>域名</label><input class="input" value="${esc(s.domain)}" disabled>
          <div class="help">域名创建后不可修改;如需变更请新建站点</div></div>
        ${s.type === "proxy" ? '<div class="form-item required"><label>代理目标</label><input class="input" id="w-edit-proxy" value="' + esc(s.proxy_to || "") + '" placeholder="如: http://127.0.0.1:8080"></div>' : ""}
        <div class="form-item"><label>监听端口</label><input class="input" id="w-edit-port" type="number" value="${s.port || 80}"></div>
        <div class="form-item"><label>备注</label><input class="input" id="w-edit-remark" value="${esc(s.remark || "")}" placeholder="站点用途说明"></div>
        <div class="settings-section-title">密码访问(HTTP Basic Auth)</div>
        <div class="form-item"><label class="radio-line"><input type="checkbox" id="w-auth-on" ${s.auth_enabled ? "checked" : ""}> 启用后访问站点需输入账号密码</label></div>
        <div id="w-auth-fields" style="${s.auth_enabled ? "" : "display:none"}">
          <div class="form-item"><label>用户名</label><input class="input" id="w-auth-user" value="${esc(s.auth_user || "")}" placeholder="如 admin"></div>
          <div class="form-item"><label>密码</label><input class="input" id="w-auth-pass" type="password" placeholder="${s.auth_enabled ? "留空则沿用已有密码" : "至少 4 位"}"></div>
        </div>`,
      footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">保 存</button>',
    });
    qs("#w-auth-on", m.el).onchange = function() { qs("#w-auth-fields", m.el).style.display = this.checked ? "" : "none"; };
    qs('[data-act="no"]', m.el).onclick = m.close;
    qs('[data-act="yes"]', m.el).onclick = async () => {
      try {
        await api("/websites/" + s.id, { method: "PUT", body: {
          proxy_to: s.type === "proxy" ? qs("#w-edit-proxy", m.el).value.trim() : (s.proxy_to || ""),
          port: Number(qs("#w-edit-port", m.el).value) || 80,
          remark: qs("#w-edit-remark", m.el).value.trim(),
        }});
        // 密码访问单独保存
        const authOn = qs("#w-auth-on", m.el).checked;
        if (authOn !== !!s.auth_enabled || (authOn && qs("#w-auth-pass", m.el).value)) {
          await api("/websites/" + s.id + "/auth", { method: "POST", body: {
            enabled: authOn,
            user: qs("#w-auth-user", m.el).value.trim(),
            password: qs("#w-auth-pass", m.el).value,
          }});
        }
        toast("站点已更新");
        m.close();
        this.render(el);
      } catch (e) { toast(e.message, "error"); }
    };
  },
};

/* ================= 应用商店 ================= */
PAGES.apps = {
  title: "应用商店",
  tab: "store",
  keyword: "",
  category: "",
  async render(el) {
    el.innerHTML = '<div class="app-grid">' + Array(8).fill(0).map(function(){
      return '<div class="card" style="margin:0;height:150px"><div class="sk" style="height:44px;width:44px;border-radius:12px;margin-bottom:12px"></div><div class="sk" style="height:14px;width:55%;margin-bottom:10px"></div><div class="sk" style="height:12px;width:90%"></div><div class="sk" style="height:12px;width:70%"></div></div>';
    }).join("") + "</div>";
    var self = this;
    // 深链支持:#/apps?tab=installed 直达已安装列表(数据库页空态"前往重建"入口)
    var q = (location.hash || "").split("?")[1] || "";
    if (q.indexOf("tab=installed") >= 0) this.tab = "installed";
    else if (q.indexOf("tab=store") >= 0) this.tab = "store";
    var templates = [], instances = [];
    try {
      var results = await Promise.all([api("/apps/templates"), api("/apps/instances")]);
      templates = results[0] || []; instances = results[1] || [];
    } catch (e) { el.innerHTML = emptyState("warn", "加载失败", e.message); return; }

    // 顶部 Tab + 动作组(左导航右按钮,一条基线)
    var tabsHtml = '<div class="tab ' + (this.tab === "store" ? "active" : "") + '" data-tab="store">应用商店 (' + templates.length + ')</div>';
    tabsHtml += '<div class="tab ' + (this.tab === "installed" ? "active" : "") + '" data-tab="installed">已安装 (' + instances.length + ')</div>';
    var actionsHtml = "";
    if (this.tab === "installed") actionsHtml += '<button class="btn btn-sm btn-plain" id="apps-check-upd">' + I("refresh") + ' 检查更新</button>';
    actionsHtml += '<button class="btn btn-sm btn-primary" id="apps-custom-add">' + I("plus") + ' 自定义应用</button>';
    window.__phpUsedPorts = (instances || []).filter(function(x) { return x.key === "php" || /^php[0-9]+$/.test(x.key); }).map(function(x) { return (x.params && x.params.Port) || ""; }).filter(Boolean);
    var html = '<div class="page-head"><div class="tabs">' + tabsHtml + '</div><div class="tabs-actions">' + actionsHtml + "</div></div><div id=\"apps-body\"></div>";
    el.innerHTML = html;

    var body = document.getElementById("apps-body");
    var self = this;
    qs("#apps-custom-add", el).onclick = function() { addCustomDialog(); };

    // 分类/搜索状态
    self.storeCat = self.storeCat || "全部";
    self.storeKw = self.storeKw || "";

    function drawStore() {
      var cats = ["全部"];
      templates.forEach(function(t) { if (cats.indexOf(t.category) < 0) cats.push(t.category); });
      var kw = self.storeKw.trim().toLowerCase();
      var shown = templates.filter(function(t) {
        if (self.storeCat !== "全部" && t.category !== self.storeCat) return false;
        if (kw && (t.name + " " + t.desc + " " + t.key).toLowerCase().indexOf(kw) < 0) return false;
        return true;
      });
      var html = '<div class="store-toolbar">' +
        '<div class="chips">' + cats.map(function(c) {
          return '<span class="tag ' + (self.storeCat === c ? "tag-primary" : "tag-info") + '" data-cat="' + esc(c) + '">' + esc(c) + "</span>";
        }).join("") + "</div>" +
        '<input class="input" id="store-kw" placeholder="搜索应用名称 / 描述..." value="' + esc(self.storeKw) + '"></div>';
      html += '<div class="app-grid">';
      if (!shown.length) html += '<div class="help" style="padding:30px;text-align:center;width:100%">没有匹配的应用</div>';
      shown.forEach(function(t, i) {
        var inst = instances.find(function(x) { return x.key === t.key; });
        html += '<div class="app-card" style="animation-delay:' + Math.min(i * 0.04, 0.4) + 's">';
        html += '<div class="app-head">' + appIcon(t, 44);
        html += '<div><h4 style="font-size:14px;font-weight:600">' + esc(t.name) + '</h4><span class="tag tag-primary">' + esc(t.category) + '</span></div></div>';
        html += '<p style="font-size:12px;color:var(--text-3);flex:1;min-height:32px;line-height:1.5;margin:6px 0">' + esc(t.desc) + '</p>';
        html += '<div style="display:flex;align-items:center;justify-content:space-between;margin-top:auto">';
        html += '<span class="tag tag-info">' + esc(t.version) + '</span>';
        // PHP 多版本:已装时仍可安装其他版本(卡片保留安装按钮)
        var instMissing = !!inst && inst.status === "missing";
        var phpMulti = t.key === "php" && inst && !instMissing;
        if (t.key.indexOf("custom_") === 0) {
          html += '<span style="display:flex;gap:6px;align-items:center">' +
            (inst ? (instMissing ? '<span class="tag tag-danger" title="容器已不存在,点「重建」按原配置恢复">容器已删除</span>' : '<span class="tag tag-success">已安装</span>') : '<button class="btn btn-sm btn-primary" data-install="' + t.key + '">安装</button>') +
            '<button class="btn btn-xs btn-danger-plain" data-custom-del="' + t.key + '" title="删除该自定义应用">删除</button></span>';
        } else if (instMissing) {
          html += '<span style="display:flex;gap:6px;align-items:center"><span class="tag tag-danger" title="安装记录还在,容器已不存在(可能被手动删除)">容器已删除</span>' +
            '<button class="btn btn-sm btn-primary" data-rebuild="' + t.key + '">重建</button></span>';
        } else if (phpMulti) {
          html += '<span style="display:flex;gap:6px;align-items:center"><span class="tag tag-success">已装 ' + esc((inst.params && inst.params.Version) || "") + '</span>' +
            '<button class="btn btn-sm btn-primary" data-install="' + t.key + '" title="可共存不同版本">装其他版本</button></span>';
        } else if (inst) { html += '<span class="tag tag-success">已安装</span>'; }
        else { html += '<button class="btn btn-sm btn-primary" data-install="' + t.key + '">安装</button>'; }
        html += '</div></div>';
      });
      html += '</div>';
      body.innerHTML = html;
      body.querySelectorAll("[data-cat]").forEach(function(c) {
        c.onclick = function() {
          self.storeCat = c.getAttribute("data-cat");
          drawStore();
        };
      });
      var kwInput = qs("#store-kw", body);
      if (kwInput) {
        kwInput.oninput = function() {
          self.storeKw = this.value;
          clearTimeout(self._kwTimer);
          self._kwTimer = setTimeout(drawStore, 250);
          // 重绘后焦点回填到搜索框并置于末尾
          setTimeout(function() {
            var inp = qs("#store-kw", body);
            if (inp) { inp.focus(); inp.setSelectionRange(inp.value.length, inp.value.length); }
          }, 0);
        };
      }
      body.querySelectorAll("[data-rebuild]").forEach(function(b) {
        b.onclick = function() {
          var key = b.getAttribute("data-rebuild");
          b.disabled = true; b.textContent = "重建中...";
          api("/apps/instances/" + key + "/start", { method: "POST", body: {} })
            .then(function() { toast("重建完成"); self.render(el); })
            .catch(function(e) { toast(e.message || "重建失败", "error", 6000); b.disabled = false; b.textContent = "重建"; });
        };
      });
      body.querySelectorAll("[data-install]").forEach(function(b) {
        b.onclick = function() {
          var tpl = null;
          templates.forEach(function(t) { if (t.key === b.getAttribute("data-install")) tpl = t; });
          if (tpl) self.installDialog(el, tpl);
        };
      });
      body.querySelectorAll("[data-custom-del]").forEach(function(b) {
        b.onclick = async function() {
          var key = b.getAttribute("data-custom-del");
          var inst = instances.find(function(x) { return x.key === key; });
          if (!(await confirmDlg("删除该自定义应用?" + (inst ? "<br><span style='color:var(--danger);font-size:12px'>它已安装,须先在「已安装」中卸载。</span>" : ""), { danger: true }))) return;
          try { await api("/apps/custom/" + key, { method: "DELETE" }); toast("已删除"); self.render(el); }
          catch (e) { toast(e.message, "error", 6000); }
        };
      });
    }

    function addCustomDialog() {
      var m = modal({
        title: "添加自定义应用", width: 680,
        body: '<div class="form-item required"><label>应用名称</label><input class="input" id="ca-name" placeholder="如 我的博客"></div>' +
          '<div class="form-item"><label>描述</label><input class="input" id="ca-desc" placeholder="可选"></div>' +
          '<div class="form-item required"><label>docker-compose.yml 内容</label>' +
          '<textarea class="textarea mono" id="ca-compose" rows="14" spellcheck="false" style="font-size:12px;line-height:1.5" placeholder="services:\n  app:\n    image: your-image:latest\n    ..."></textarea>' +
          '<div class="help">与内置应用同等管理能力:安装/停止/重启/卸载/配置编辑;保存时自动用 Docker 校验语法</div></div>',
        footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">保存到应用商店</button>',
      });
      qs('[data-act="no"]', m.el).onclick = m.close;
      var cmC = cmUpgrade(qs("#ca-compose", m.el), "yaml");
      if (cmC) {
        m.setVeto(function() {
          if (cmC.isClean()) return false;
          confirmDlg("compose 配置有<b>未保存的修改</b>,确定放弃并关闭吗?", { danger: true }).then(function(ok) {
            if (ok) { cmC.markClean(); m.doClose(); }
          });
          return true;
        });
      }
      qs('[data-act="yes"]', m.el).onclick = async function() {
        try {
          await api("/apps/custom", { method: "POST", body: {
            name: qs("#ca-name", m.el).value.trim(),
            desc: qs("#ca-desc", m.el).value.trim(),
            compose: qs("#ca-compose", m.el).value,
          }});
          if (cmC) cmC.markClean();
          toast("自定义应用已保存");
          m.close();
          self.render(el);
        } catch (e) { toast(e.message, "error", 8000); }
      };
    }

    function drawInstalled() {
      var html = '<div class="card">';
      if (!instances.length) {
        html += emptyState("inbox", "暂无已安装应用", "去商店逛逛吧");
      } else {
        html += '<div class="table-wrap"><table class="table"><thead><tr><th>应用</th><th>标识</th><th>状态</th><th>操作</th></tr></thead><tbody>';
        instances.forEach(function(r) {
          var tpl = templates.find(function(x) { return x.key === r.key; }) || {};
          var statusTag = r.status === "running"
            ? '<span class="tag tag-success"><span class="dot on"></span>运行中</span>'
            : (r.status === "missing"
              ? '<span class="tag tag-danger" title="安装记录还在,但容器已不存在(可能被手动删除)。点「重建」可按原配置恢复,数据目录保留">容器已删除</span>'
              : '<span class="tag tag-info"><span class="dot off"></span>未运行</span>');
          var ver = r.params && r.params.Version ? '<span class="tag tag-info" style="margin-left:6px">' + esc(r.params.Version) + '</span>' : "";
          html += '<tr><td><b>' + esc(r.name) + '</b>' + ver + '<span data-upd-badge="' + r.key + '" style="margin-left:6px"></span></td><td><span class="mono">' + esc(r.key) + '</span></td>';
          html += '<td>' + statusTag + '</td>';
          var canManage = r.key === "nginx" || r.key === "openresty" ||
            r.key === "mysql" || r.key === "mariadb" || r.key === "postgresql" || r.key === "redis" ||
            /^php[0-9]*$/.test(r.key);
          html += '<td><div style="display:flex;gap:4px;flex-wrap:wrap">';
          if (canManage) html += '<button class="btn btn-sm btn-primary" data-app-mgr="' + r.key + '">管理</button>';
          html += '<button class="btn btn-sm btn-plain" data-app-cfg="' + r.key + '" data-app-name="' + esc(r.name) + '">配置</button>';
          var running = r.status === "running";
          var missing = r.status === "missing";
          var actNames = { start: missing ? "重建" : "启动", stop: "停止", restart: "重启" };
          ["start","stop","restart"].forEach(function(a) {
            // 状态感知:运行中禁用启动;容器已删除时禁用停止/重启,「重建」按原配置重新创建容器
            var dis = (a === "start" && running) || (a !== "start" && missing) || (a === "stop" && !running);
            html += '<button class="btn btn-sm btn-plain" data-app-act="' + a + '" data-app-key="' + r.key + '"' + (dis ? " disabled" : "") + ">" + actNames[a] + '</button>';
          });
          html += '<button class="btn btn-sm btn-danger-plain" data-app-act="uninstall" data-app-key="' + r.key + '"' + (missing ? ' title="清除安装记录' + '" data-clear="1"' : '') + '>' + (missing ? "清除" : "卸载") + '</button></div></td></tr>';
        });
        html += '</tbody></table></div>';
      }
      html += '</div>';
      body.innerHTML = html;
      body.querySelectorAll("[data-app-mgr]").forEach(function(b) {
        b.onclick = function() { self.appManagerDialog(b.getAttribute("data-app-mgr")); };
      });
      body.querySelectorAll("[data-app-cfg]").forEach(function(b) {
        b.onclick = function() { self.composeDialog(b.getAttribute("data-app-cfg"), b.getAttribute("data-app-name")); };
      });
      // 检查更新:逐实例对比 Docker Hub 最新标签(PHP 多版本实例共用 php 的标签)。
      // 此前按钮无任何事件绑定(v2.35 的实现早已丢失),点击毫无反应、徽标永远为空
      var updBtn = qs("#apps-check-upd", el);
      if (updBtn) updBtn.onclick = async function() {
        this.disabled = true; this.innerHTML = "检查中...";
        var upd = 0, checked = 0;
        // WAF 状态只取一次:nginx 升级会换掉 Lua 定制镜像,需要提醒
        var wafOn = false;
        try { var wf = await api("/waf"); wafOn = !!((wf.settings || wf).enabled); } catch (e) {}
        // 并行拉取各实例的 Hub 标签(此前串行,4 个实例要串 4 次外网请求)
        var checks = instances.map(function(r) {
          var badge = body.querySelector('[data-upd-badge="' + r.key + '"]');
          if (!badge) return null;
          var base = /^php[0-9]*$/.test(r.key) ? "php" : r.key;
          return api("/apps/hub-tags?key=" + encodeURIComponent(base))
            .then(function(tags) { return { r: r, badge: badge, tags: tags || [] }; })
            .catch(function() { return { r: r, badge: badge, tags: [] }; });
        }).filter(Boolean);
        var results = await Promise.all(checks);
        for (var i = 0; i < results.length; i++) {
          var r = results[i].r, badge = results[i].badge, tags = results[i].tags;
          var cur = (r.params && r.params.Version) || "";
          if (!tags.length || !cur) { badge.innerHTML = ""; continue; }
          var rolling = /^(latest|edge|alpine|stable|slim.*|bookworm|bullseye)$/i.test(cur);
          if (rolling) { badge.innerHTML = ""; continue; }
          // php 标签形态是 8.4-apache,归一后与实例版本比对
          var tagsN = tags.map(function(t) { return String(t).replace(/-apache$/, ""); });
          var idx = tagsN.indexOf(cur);
          if (idx === 0) { badge.innerHTML = ""; continue; }
          checked++;
          // WAF 启用时 nginx 跑的是面板自编译的 Lua 定制镜像(版本与官方标签无对应),
          // "可更新 → 官方版"的提示具有误导性且照做会丢 WAF——直接标注定制镜像
          if (r.key === "nginx" && wafOn) {
            badge.innerHTML = '<span class="tag tag-info" title="WAF 启用中:当前为面板自编译的 Lua 定制镜像,官方标签版本不适用。如需升级请升级后重装 WAF">WAF 定制镜像</span>';
            continue;
          }
          if (idx > 0) {
            upd++;
            badge.innerHTML = '<span class="tag tag-warning">可更新 → ' + esc(tagsN[0]) + "</span>";
          } else {
            badge.innerHTML = '<span class="tag tag-info">当前版本已下架</span>';
          }
        }
        toast(upd ? ("检查完成: " + checked + " 个可检查," + upd + " 个可更新") : "全部应用已是最新", upd ? "info" : "success", 6000);
        this.disabled = false; this.innerHTML = I("refresh") + " 检查更新";
      };
      body.querySelectorAll("[data-app-act]").forEach(function(b) {
        b.onclick = function() {
          var key = b.getAttribute("data-app-key"), act = b.getAttribute("data-app-act");
          if (act === "uninstall") {
            if (b.getAttribute("data-clear")) {
              // 容器已删除:专用「彻底清除」弹窗(清安装记录,可选删数据目录)
              var pm = modal({
                title: "彻底清除 · " + key, width: 470,
                body: '<div class="help" style="margin-bottom:12px">该应用的容器已不存在,清除将移除安装记录,应用商店即可重新安装。</div>' +
                  '<label class="radio-line" style="color:var(--danger)"><input type="checkbox" id="un-del-data"> 同时删除数据目录(含数据库文件/配置,<b>不可恢复</b>)</label>',
                footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-danger" data-act="yes">确认清除</button>'
              });
              qs('[data-act="no"]', pm.el).onclick = pm.close;
              qs('[data-act="yes"]', pm.el).onclick = async function() {
                var delData = qs("#un-del-data", pm.el).checked;
                if (delData && !(await confirmDlg("再次确认:将删除该应用在服务器上的全部数据文件,<b>不可恢复</b>!", { danger: true }))) return;
                pm.close();
                b.disabled = true;
                api("/apps/instances/" + key + "/uninstall", { method: "POST", body: { remove_data: delData }, timeout: 0 })
                  .then(function() { toast(delData ? "已彻底清除(含数据目录)" : "已清除安装记录"); self.render(el); })
                  .catch(function(e) { toast(e.message || "清除失败", "error", 6000); b.disabled = false; });
              };
              return;
            }
            confirmDlg("卸载 <b>" + key + "</b>?容器将停止并移除(站点配置保留)。", { danger: true }).then(function(ok) {
              if (!ok) return;
              b.disabled = true;
              api("/apps/instances/" + key + "/uninstall", { method: "POST", body: { remove_data: false }, timeout: 0 })
                .then(function(d) {
                  if (d && d.task_id) {
                    taskModal(d.task_id, function() { toast("卸载完成"); self.render(el); });
                  } else {
                    toast("卸载完成");
                    setTimeout(function(){ self.render(el); }, 1500);
                  }
                })
                .catch(function(e) { toast(e.message || "卸载失败", "error", 6000); b.disabled = false; });
            });
            return;
          }
          api("/apps/instances/" + key + "/" + act, { method: "POST", body: {} })
            .then(function() { toast("操作成功"); setTimeout(function(){ self.render(el); }, 1500); })
            .catch(function(e) { toast(e.message || "操作失败", "error", 6000); });
        };
      });
    }

    function render() {
      var body = document.getElementById("apps-body");
      if (self.tab === "store") drawStore(); else drawInstalled();
    }

    // Tab 切换(整页重绘,让头部动作组跟随 tab 变化)
    el.querySelectorAll(".tab").forEach(function(t) {
      t.onclick = function() {
        if (self.tab === t.getAttribute("data-tab")) return;
        self.tab = t.getAttribute("data-tab");
        self.render(el);
      };
    });
    render();
  },
  /* 应用运行时管理(PHP/Nginx/OpenResty) */
  appManagerDialog(key) {
    var m = modal({
      title: "应用管理 · " + key.toUpperCase(), width: 780,
      body: '<div id="am-body">' + skeletonTable(3, 3) + "</div>",
      footer: '<button class="btn btn-plain" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    var body = qs("#am-body", m.el);
    // PHP 多版本实例(php/php82/…)共用 PHP 管理视图:扩展与 php.ini 按实例键操作
    var isPHP = /^php[0-9]*$/.test(key);
    var self = this;
    self._amTab = "info";
    self._amRT = null;   // 缓存 runtime info
    self._amCatalog = null;

    function tabsHTML() {
      if (!isPHP) return "";
      return '<div class="hub-tabs" style="margin-bottom:14px">' +
        '<div class="hub-tab" data-t="info">运行信息</div>' +
        '<div class="hub-tab" data-t="ext">扩展管理</div>' +
        '<div class="hub-tab" data-t="ini">配置文件</div></div><div id="am-view"></div>';
    }

    function switchTab(t) {
      self._amTab = t;
      qsa(".hub-tab", body).forEach(function(x) { x.classList.toggle("active", x.getAttribute("data-t") === t); });
      if (t === "info") renderInfo();
      else if (t === "ext") renderExt();
      else renderIni();
    }

    function fetchRT(force) {
      if (self._amRT && !force) return Promise.resolve(self._amRT);
      return api("/apps/instances/" + key + "/runtime").then(function(d) { self._amRT = d; return d; });
    }

    function loadedChips(exts, removable) {
      removable = removable || [];
      // PHP 8.4 的 opcache 在 php -m 固有输出两次,显示去重
      exts = exts.filter(function(e2, i) { return exts.indexOf(e2) === i; });
      return '<div class="settings-section"><div class="settings-section-title">已加载扩展(' + exts.length + ')<span class="help" style="margin:0">带 × 的为面板安装,可卸载;其余为内置模块</span></div>' +
        '<div style="display:flex;flex-wrap:wrap;gap:6px">' + exts.map(function(e2) {
          var low = e2.toLowerCase().replace(/\s+/g, "");
          // 卸载必须传 removable 的原始键(= ini 文件名),不能传显示名(如 "Zend OPcache")
          var matchR = removable.find(function(r) { return low === r || low === "zend" + r; }) || "";
          if (matchR) {
            return '<span class="tag tag-info am-ext-chip">' + esc(e2) +
              '<i class="am-ext-del" data-unload="' + esc(matchR) + '" title="卸载该扩展">&times;</i></span>';
          }
          return '<span class="tag tag-info am-ext-chip">' + esc(e2) + "</span>";
        }).join("") + "</div></div>";
    }

    function wireUnloadChips(scope) {
      qsa("[data-unload]", scope).forEach(function(x) {
        x.onclick = function(ev) {
          ev.stopPropagation();
          var ext = x.getAttribute("data-unload");
          confirmDlg("卸载扩展 <b>" + esc(ext) + "</b>?<br><span style='font-size:12px;color:var(--text-3)'>将移除扩展文件并重启 PHP;依赖它的站点代码会立即报错,请确认没有站点在使用</span>", { danger: true }).then(function(ok) {
            if (!ok) return;
            api("/apps/instances/" + key + "/runtime/php-ext-remove", { method: "POST", body: { ext: ext } }).then(function(d) {
              toast("开始卸载 " + ext, "info");
              taskModal(d.task_id, function() { self._amRT = null; renderExt(); });
            }).catch(function(e) { toast(e.message, "error", 6000); });
          });
        };
      });
    }

    /* ---- Tab 1: 运行信息 ---- */
    function renderInfo() {
      var view = qs("#am-view", body);
      view.innerHTML = '<div class="boot-loading" style="height:60px"><div class="spinner"></div></div>';
      fetchRT(true).then(function(d) {
          var html = '<div class="am-info-row"><div class="am-info-label">当前版本</div><div class="mono am-info-val">' + esc(d.version || "-") + "</div></div>" +
          '<div class="am-ops"><button class="btn btn-sm btn-plain" id="am-restart">' + I("restart") + " 重启应用</button>" +
          '<button class="btn btn-sm btn-plain" id="am-reload">' + I("refresh") + " 重载配置</button></div>";
        html += loadedChips(d.extensions || [], d.removable);
        if (d.settings) {
          html += '<div class="settings-section"><div class="settings-section-title">面板快捷设置(写入独立覆盖文件,不影响 php.ini)</div>' +
            '<div class="settings-grid">' +
            '<div><label>上传大小限制 (upload_max_filesize)</label><input class="input" id="am-s-upload" value="' + esc(d.settings.upload_max_filesize || "") + '" placeholder="2M"></div>' +
            '<div><label>POST 大小限制 (post_max_size)</label><input class="input" id="am-s-post" value="' + esc(d.settings.post_max_size || "") + '" placeholder="8M"></div>' +
            '<div><label>执行超时秒 (max_execution_time)</label><input class="input" id="am-s-exec" value="' + esc(d.settings.max_execution_time || "") + '" placeholder="30"></div>' +
            '<div><label>内存限制 (memory_limit)</label><input class="input" id="am-s-mem" value="' + esc(d.settings.memory_limit || "") + '" placeholder="128M"></div></div>' +
            '<button class="btn btn-primary btn-sm" id="am-ini-save" style="margin-top:10px">保存快捷设置并重启 PHP</button></div>';
        }
        view.innerHTML = html + '<div id="am-task"></div>';
        wireUnloadChips(view);
        qs("#am-restart", view).onclick = function() {
          api("/apps/instances/" + key + "/runtime/restart", { method: "POST", body: {} }).then(function() { toast("已重启"); switchTab(self._amTab); }).catch(function(e) { toast(e.message, "error"); });
        };
        qs("#am-reload", view).onclick = function() {
          api("/apps/instances/" + key + "/runtime/reload", { method: "POST", body: {} }).then(function(d2) { toast(d2 || "已重载"); }).catch(function(e) { toast(e.message, "error", 5000); });
        };
        var ib = qs("#am-ini-save", view);
        if (ib) ib.onclick = function() {
          api("/apps/instances/" + key + "/runtime/php-ini", { method: "POST", body: { settings: {
            upload_max_filesize: qs("#am-s-upload", view).value.trim(),
            post_max_size: qs("#am-s-post", view).value.trim(),
            max_execution_time: qs("#am-s-exec", view).value.trim(),
            memory_limit: qs("#am-s-mem", view).value.trim(),
          }}}).then(function() { toast("设置已保存并重启 PHP"); switchTab("info"); }).catch(function(e) { toast(e.message, "error", 5000); });
        };
      }).catch(function(e) { view.innerHTML = emptyState("warn", "加载失败", e.message); });
    }

    /* ---- Tab 2: 扩展管理 ---- */
    function renderExt() {
      var view = qs("#am-view", body);
      view.innerHTML = '<div class="boot-loading" style="height:60px"><div class="spinner"></div></div>';
      Promise.all([fetchRT(), api("/apps/instances/" + key + "/runtime/php-ext-catalog", { method: "POST", body: {} }).catch(function() { return {}; })])
        .then(function(rs) {
          // 归一化:php -m 的 "Zend OPcache" 对应目录键 "opcache"(去空格 + 可选 zend 前缀)
          var loaded = (rs[0].extensions || []).map(function(x) { return x.toLowerCase().replace(/\s+/g, ""); });
          var cat = rs[1] || {};
          self._amCatalog = cat;
          // 分组:已加载在上,可安装按类目
          var html = loadedChips(rs[0].extensions || [], rs[0].removable);
          html += '<div class="settings-section"><div class="settings-section-title">可安装扩展 <span class="help" style="margin:0">点卡片安装;源码/PECL 编译约 1~5 分钟,自动重启生效</span></div>';
          var cats = [];
          Object.keys(cat).forEach(function(name) {
            var c = cat[name].cat || "其他";
            if (cats.indexOf(c) < 0) cats.push(c);
          });
          cats.forEach(function(c) {
            var hasLoaded = function(n) { var k = n.toLowerCase(); return loaded.indexOf(k) >= 0 || loaded.indexOf("zend" + k) >= 0; };
          var items = Object.keys(cat).filter(function(n) { return (cat[n].cat || "其他") === c && !hasLoaded(n); });
            if (!items.length) return;
            html += '<div class="am-cat">' + esc(c) + "</div>" +
              '<div class="am-ext-grid">' + items.map(function(n) {
                var it = cat[n];
                return '<div class="am-ext-card" data-ext="' + esc(n) + '">' +
                  '<div class="am-ext-head"><b class="mono">' + esc(n) + "</b>" + (it.pecl ? '<span class="tag tag-purple am-ext-src">PECL</span>' : "") + "</div>" +
                  '<div class="am-ext-desc">' + esc(it.desc || "") + "</div>" +
                  '<button class="btn btn-xs btn-primary am-ext-install">' + I("download") + " 安装</button></div>";
              }).join("") + "</div>";
          });
          html += "</div>";
          view.innerHTML = html + '<div id="am-task"></div>';
          wireUnloadChips(view);
          qsa(".am-ext-install", view).forEach(function(btn) {
            btn.onclick = function() {
              var card = btn.closest(".am-ext-card");
              var ext = card.getAttribute("data-ext");
              btn.disabled = true;
              btn.innerHTML = "安装中...";
              api("/apps/instances/" + key + "/runtime/php-ext", { method: "POST", body: { ext: ext } }).then(function(d2) {
                toast("开始安装 " + ext, "info");
                taskModal(d2.task_id, function() { self._amRT = null; renderExt(); });
              }).catch(function(e) {
                toast(e.message, "error", 6000);
                btn.disabled = false;
                btn.innerHTML = I("download") + " 安装";
              });
            };
          });
        }).catch(function(e) { view.innerHTML = emptyState("warn", "加载失败", e.message); });
    }

    /* ---- Tab 3: 配置文件 (php.ini) ---- */
    function renderIni() {
      var view = qs("#am-view", body);
      view.innerHTML = '<div class="boot-loading" style="height:60px"><div class="spinner"></div></div>';
      api("/apps/instances/" + key + "/runtime/php-ini-get", { method: "POST", body: {} }).then(function(d) {
        view.innerHTML = '<div class="settings-section"><div class="settings-section-title">php.ini 编辑器' +
          '<span class="help" style="margin:0">完整配置文件,保存后自动重启 PHP;改错参数可能导致站点异常,请谨慎修改</span></div>' +
          '<textarea class="textarea mono" id="am-ini-editor" rows="20" spellcheck="false" style="font-size:12px;line-height:1.5">' + esc(d.content || "") + "</textarea>" +
          '<div style="margin-top:10px;display:flex;gap:10px;align-items:center">' +
          '<button class="btn btn-primary btn-sm" id="am-ini-file-save">' + I("edit") + " 保存并重启 PHP</button>" +
          '<button class="btn btn-plain btn-sm" id="am-ini-reload">放弃修改</button></div></div>';
        qs("#am-ini-file-save", view).onclick = function() {
          api("/apps/instances/" + key + "/runtime/php-ini-file-save", { method: "POST", body: { content: qs("#am-ini-editor", view).value } })
            .then(function() { toast("php.ini 已保存并重启生效"); fetchRT(true); })
            .catch(function(e) { toast(e.message, "error", 6000); });
        };
        qs("#am-ini-reload", view).onclick = function() { renderIni(); };
      }).catch(function(e) { view.innerHTML = emptyState("warn", "加载失败", e.message); });
    }

    /* ---- 组装 ---- */
    if (!isPHP) {
      var isNgin = key === "nginx" || key === "openresty";
      var isMySqlLike = key === "mysql" || key === "mariadb";
      var isDb = isMySqlLike || key === "postgresql" || key === "redis";
      var view;

      function stopPoll() { if (self._amPollTimer) { clearInterval(self._amPollTimer); self._amPollTimer = null; } }

      if (isNgin) {
        renderNginxTabs();
        return;
      }

      function renderNginxTabs() {
        stopPoll();
        body.innerHTML = '<div class="hub-tabs" style="margin-bottom:14px">' +
          '<div class="hub-tab" data-t="info">运行状态</div>' +
          '<div class="hub-tab" data-t="conf">配置文件</div>' +
          '<div class="hub-tab" data-t="log">错误日志</div></div><div id="am-view">' + skeletonTable(3, 3) + "</div>";
        var nview = qs("#am-view", body);

        function parseStub(raw) {
          var o = {};
          var m1 = raw.match(/Active connections:\s*(\d+)/);
          if (m1) o.active = m1[1];
          var m2 = raw.match(/accepts handled requests\s*\n?\s*(\d+)\s+(\d+)\s+(\d+)/);
          if (m2) { o.accepts = m2[1]; o.handled = m2[2]; o.requests = m2[3]; }
          var m3 = raw.match(/Reading:\s*(\d+)\s+Writing:\s*(\d+)\s+Waiting:\s*(\d+)/);
          if (m3) { o.reading = m3[1]; o.writing = m3[2]; o.waiting = m3[3]; }
          return o;
        }
        function renderNginxInfo() {
          stopPoll();
          nview.innerHTML = '<div class="boot-loading" style="height:60px"><div class="spinner"></div></div>';
          fetchRT(true).then(function(d) {
            var html = '<div class="am-info-row"><div class="am-info-label">当前版本</div><div class="mono am-info-val">' + esc(d.version || "-") + "</div></div>" +
              '<div class="am-ops"><button class="btn btn-sm btn-plain" id="am-restart">' + I("restart") + " 重启应用</button>" +
              '<button class="btn btn-sm btn-plain" id="am-reload">' + I("refresh") + " 重载配置</button>" +
              '<a class="btn btn-sm btn-primary" href="#/websites">' + I("globe") + " 网站管理</a></div>";
            html += '<div class="settings-section"><div class="settings-section-title">负载状态<span class="help" style="margin:0">每 4 秒自动刷新(stub_status,仅容器内网可达)</span></div>' +
              '<div class="am-status-grid" id="am-status"><div class="help">读取中...</div></div></div>';
            html += '<div class="settings-section"><div class="settings-section-title">编译模块(nginx -V)</div><pre class="am-raw" id="am-modules">读取中...</pre></div>';
            nview.innerHTML = html;
            qs("#am-restart", nview).onclick = function() {
              api("/apps/instances/" + key + "/runtime/restart", { method: "POST", body: {} }).then(function() { toast("已重启"); renderNginxInfo(); }).catch(function(e) { toast(e.message, "error"); });
            };
            qs("#am-reload", nview).onclick = function() {
              api("/apps/instances/" + key + "/runtime/reload", { method: "POST", body: {} }).then(function(d2) { toast(d2 || "已重载"); }).catch(function(e) { toast(e.message, "error", 5000); });
            };
            function pollStatus() {
              api("/apps/instances/" + key + "/runtime/status", { method: "POST", body: {} }).then(function(d3) {
                var box = qs("#am-status", nview);
                if (!box || !document.body.contains(box)) { stopPoll(); return; }
                if (!d3 || !d3.raw) { box.innerHTML = '<div class="help">' + esc((d3 && d3.error) || "暂无数据") + "</div>"; return; }
                var st = parseStub(d3.raw);
                function cell(k, v, hint) {
                  return '<div class="am-st-cell"><b>' + (v === undefined ? "-" : v) + "</b><span>" + k + (hint ? '<i class="help">' + hint + "</i>" : "") + "</span></div>";
                }
                box.innerHTML = cell("活动连接", st.active) + cell("总连接数", st.accepts, "已接受") + cell("总请求数", st.requests) +
                  cell("读取中", st.reading) + cell("写入中", st.writing) + cell("长连接等待", st.waiting);
              }).catch(function() {});
            }
            pollStatus();
            stopPoll();
            self._amPollTimer = setInterval(pollStatus, 4000);
            api("/apps/instances/" + key + "/runtime/modules", { method: "POST", body: {} }).then(function(d4) {
              var el2 = qs("#am-modules", nview);
              if (el2) el2.textContent = (d4.raw || "").trim() || "无信息";
            }).catch(function() {});
          }).catch(function(e) { nview.innerHTML = emptyState("warn", "加载失败", e.message); });
        }
        function renderNginxConf() {
          stopPoll();
          nview.innerHTML = '<div class="boot-loading" style="height:60px"><div class="spinner"></div></div>';
          api("/apps/instances/" + key + "/runtime/conf-get", { method: "POST", body: {} }).then(function(d) {
            nview.innerHTML = '<div class="settings-section"><div class="settings-section-title">nginx.conf 主配置编辑器<span class="help" style="margin:0">保存时自动 nginx -t 校验,失败会回滚不会应用</span></div>' +
              '<textarea class="textarea mono" id="am-conf-editor" rows="22" spellcheck="false" style="font-size:12px;line-height:1.5">' + esc(d.content || "") + "</textarea>" +
              '<div style="margin-top:10px;display:flex;gap:10px;align-items:center">' +
              '<button class="btn btn-primary btn-sm" id="am-conf-save">' + I("edit") + " 保存并重载</button>" +
              '<button class="btn btn-plain btn-sm" id="am-conf-reload">放弃修改</button></div>' +
              '<div class="settings-hint">站点级配置请在【网站】页每个站点中编辑;改错配置会被自动回滚,可放心尝试</div></div>';
            qs("#am-conf-save", nview).onclick = function() {
              api("/apps/instances/" + key + "/runtime/conf-save", { method: "POST", body: { content: qs("#am-conf-editor", nview).value } })
                .then(function(msg) { toast(msg || "已保存并重载"); })
                .catch(function(e) { toast(e.message, "error", 8000); });
            };
            qs("#am-conf-reload", nview).onclick = function() { renderNginxConf(); };
          }).catch(function(e) { nview.innerHTML = emptyState("warn", "加载失败", e.message); });
        }
        function renderNginxLog() {
          stopPoll();
          nview.innerHTML = '<div class="boot-loading" style="height:60px"><div class="spinner"></div></div>';
          api("/apps/instances/" + key + "/runtime/errorlog", { method: "POST", body: {} }).then(function(d) {
            nview.innerHTML = '<div class="settings-section"><div class="settings-section-title">错误日志(最近 120 行)<span class="help" style="margin:0">/var/log/nginx/error.log</span></div>' +
              '<pre class="am-raw" id="am-errlog">' + esc(d.content || "(空)") + "</pre>" +
              '<div style="margin-top:10px;display:flex;gap:8px"><button class="btn btn-plain btn-sm" id="am-log-refresh">' + I("refresh") + ' 刷新</button><button class="btn btn-danger-plain btn-sm" id="am-log-clear">' + I("trash") + " 清空日志</button></div></div>";
            qs("#am-log-refresh", nview).onclick = function() { renderNginxLog(); };
            qs("#am-log-clear", nview).onclick = async function() {
              if (!(await confirmDlg("确定清空网关错误日志吗?清空后不可恢复(不影响站点访问日志)。"))) return;
              try {
                await api("/apps/instances/" + key + "/runtime/errorlog-clear", { method: "POST", body: {} });
                toast("错误日志已清空");
                renderNginxLog();
              } catch (e) { toast(e.message, "error"); }
            };
            var pre = qs("#am-errlog", nview);
            if (pre) pre.scrollTop = pre.scrollHeight;
          }).catch(function(e) { nview.innerHTML = emptyState("warn", "加载失败", e.message); });
        }
        var nginxTabs = { info: renderNginxInfo, conf: renderNginxConf, log: renderNginxLog };
        qsa(".hub-tab", body).forEach(function(t) {
          t.onclick = function() {
            qsa(".hub-tab", body).forEach(function(x) { x.classList.toggle("active", x === t); });
            (nginxTabs[t.getAttribute("data-t")] || renderNginxInfo)();
          };
        });
        renderNginxInfo();
      }
      // ---- 通用容器管理(数据库/Redis/WordPress 等所有应用)----
      stopPoll();
      body.innerHTML = '<div id="am-view">' + skeletonTable(4, 4) + "</div>";
      view = qs("#am-view", body);
      renderGeneric();

      function renderGeneric() {
        view.innerHTML = '<div class="boot-loading" style="height:60px"><div class="spinner"></div></div>';
        api("/apps/instances/" + key + "/container-info").then(function(d) {
          var running = d.status === "running";
          var html = '<div class="am-info-row"><div class="am-info-label">镜像</div><div class="mono am-info-val">' + esc(d.image) + "</div>" +
            '<span class="tag ' + (running ? "tag-success" : "tag-info") + '" style="margin-left:auto">' + (running ? "运行中" : d.status) + "</span></div>";
          html += '<div class="am-status-grid">' +
            '<div class="am-st-cell"><b>' + esc(d.cpu || "-") + "</b><span>CPU 占用</span></div>" +
            '<div class="am-st-cell"><b style="font-size:14px">' + esc(d.mem || "-") + '</b><span>内存 / 上限 ' + esc(d.mem_limit || "-") + "</span></div>" +
            '<div class="am-st-cell"><b>' + esc((d.started_at || "").slice(0, 10)) + '</b><span>启动日期 ' + esc((d.started_at || "").slice(11, 19)) + "</span></div>" +
            '<div class="am-st-cell"><b>' + (d.restarts || 0) + "</b><span>重启次数</span></div></div>";
          html += '<div class="am-ops" style="margin-top:14px"><button class="btn btn-sm btn-plain" id="am-restart">' + I("restart") + " 重启应用</button>" +
            '<button class="btn btn-sm btn-plain" id="am-log-btn">' + I("logs") + " 容器日志</button>" +
            '<button class="btn btn-sm btn-plain" id="am-term">' + I("terminal") + " 容器终端</button>";
          if (Object.keys(d.ports || {}).length) html += '<span class="help" style="margin:0">端口映射: ' + esc(Object.keys(d.ports).map(function(hp) { return hp + " → " + d.ports[hp].join("/"); }).join("  ")) + "</span>";
          html += "</div>";
          // 数据库专属功能
          if (isMySqlLike) {
            html += '<div class="settings-section" style="margin-top:14px"><div class="settings-section-title">修改 root 密码<span class="help" style="margin:0">需先用当前记录的密码校验,改完同步面板记录</span></div>' +
              '<div class="settings-grid"><div><label>新密码</label><input class="input" type="password" id="am-db-p1"></div>' +
              '<div><label>确认新密码</label><input class="input" type="password" id="am-db-p2"></div></div>' +
              '<button class="btn btn-primary btn-sm" id="am-db-pwd" style="margin-top:10px">确认修改</button></div>';
          }
          if (isDb && key !== "redis") {
            html += '<div class="am-ops" style="margin-top:6px"><a class="btn btn-sm btn-primary" href="#/databases">' + I("database") + " 前往数据库管理(建库 / 备份 / 导入 / SQL)</a></div>";
          }
          // 挂载卷
          if ((d.mounts || []).length) {
            html += '<div class="settings-section" style="margin-top:14px"><div class="settings-section-title">数据目录(挂载卷)</div>' +
              d.mounts.map(function(mt, i) {
                return '<div class="waf-dist-row"><span class="mono am-mount-host" style="flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="' + esc(mt.host) + '">' + esc(mt.host) + '</span><span class="help" style="margin:0 10px">→ ' + esc(mt.container) + '</span><button class="btn btn-xs btn-plain" data-mount-open="' + i + '">文件</button></div>';
              }).join("") + "</div>";
          }
          // 容器日志
          html += '<div class="settings-section" style="margin-top:14px"><div class="settings-section-title">容器日志(最近 150 行)</div>' +
            '<pre class="am-raw" id="am-clog" style="max-height:260px">读取中...</pre>' +
            '<div style="margin-top:10px"><button class="btn btn-plain btn-sm" id="am-clog-refresh">' + I("refresh") + " 刷新日志</button></div></div>";
          view.innerHTML = html;
          qs("#am-restart", view).onclick = function() {
            api("/apps/instances/" + key + "/restart", { method: "POST", body: {} }).then(function() { toast("已重启"); renderGeneric(); }).catch(function(e) { toast(e.message, "error"); });
          };
          qs("#am-log-btn", view).onclick = function() { loadLog(); };
          qs("#am-term", view).onclick = function() {
            window.open(location.pathname + location.search + "#/terminal?container=" + encodeURIComponent(container), "_blank");
          };
          var logBtn2 = qs("#am-clog-refresh", view);
          if (logBtn2) logBtn2.onclick = function() { loadLog(); };
          function loadLog() {
            var pre = qs("#am-clog", view);
            if (pre) pre.textContent = "读取中...";
            api("/apps/instances/" + key + "/container-log?tail=150").then(function(d2) {
              var pre2 = qs("#am-clog", view);
              if (!pre2) return;
              pre2.textContent = d2.content || "(空)";
              pre2.scrollTop = pre2.scrollHeight;
            }).catch(function(e) {
              var pre2 = qs("#am-clog", view);
              if (pre2) pre2.textContent = "读取失败: " + e.message;
            });
          }
          loadLog();
          stopPoll();
          self._amPollTimer = setInterval(async function() {
            const box = qs("#am-view", body);
            if (!box || !document.body.contains(box)) { stopPoll(); return; }
            try {
              const d3 = await api("/apps/instances/" + key + "/container-info");
              const cells = box.querySelectorAll(".am-st-cell b");
              if (cells.length >= 4) {
                cells[0].textContent = d3.cpu || cells[0].textContent;
                cells[1].textContent = d3.mem || cells[1].textContent;
                cells[3].textContent = d3.restarts || 0;
              }
            } catch (e) {}
          }, 5000);
          qsa("[data-mount-open]", view).forEach(function(b) {
            b.onclick = function() {
              var mt = (d.mounts || [])[Number(b.getAttribute("data-mount-open"))];
              if (mt && mt.host) {
                location.hash = "#/files?path=" + encodeURIComponent(mt.host);
              }
            };
          });
          var pb = qs("#am-db-pwd", view);
          if (pb) pb.onclick = async function() {
            var p1 = qs("#am-db-p1", view).value, p2 = qs("#am-db-p2", view).value;
            if (p1.length < 6) { toast("新密码至少 6 位", "error"); return; }
            if (p1 !== p2) { toast("两次输入的密码不一致", "error"); return; }
            try {
              await api("/db/" + key + "/root-password", { method: "POST", body: { new_password: p1 } });
              toast("root 密码修改成功,请牢记新密码");
              qs("#am-db-p1", view).value = ""; qs("#am-db-p2", view).value = "";
            } catch (e) { toast(e.message, "error", 6000); }
          };
        }).catch(function(e) { view.innerHTML = emptyState("warn", "加载失败", e.message); });
      }
      return;
    }
    body.innerHTML = tabsHTML();
    qsa(".hub-tab", body).forEach(function(t) {
      t.onclick = function() { switchTab(t.getAttribute("data-t")); };
    });
    switchTab("info");
  },
  /* 编辑应用 docker-compose 配置(保存校验;重建容器生效) */
  async composeDialog(key, name) {
    var d;
    try { d = await api("/apps/instances/" + key + "/compose"); }
    catch (e) { toast(e.message, "error"); return; }
    var m = modal({
      title: "编辑配置 · " + (name || key), width: 860,
      body: '<div class="help" style="margin-bottom:10px">直接编辑 docker-compose.yml(端口 / 环境变量 / 挂载 / 镜像等)。<b>仅保存</b>只写入文件;<b>保存并重建容器</b>会按新配置重建,可能短暂中断服务。</div>' +
        '<textarea class="textarea mono" id="ac-yaml" rows="20" spellcheck="false" style="font-size:12.5px;line-height:1.55">' + esc(d.content || "") + "</textarea>",
      footer: '<button class="btn btn-plain" data-act="no">取 消</button>' +
        '<button class="btn btn-plain" data-act="save">仅保存</button>' +
        '<button class="btn btn-primary" data-act="apply">保存并重建容器</button>',
    });
    qs('[data-act="no"]', m.el).onclick = m.close;
    var cmY = cmUpgrade(qs("#ac-yaml", m.el), "yaml", {
      "Ctrl-S": function() { var b = qs('[data-act="save"]', m.el); if (b) b.click(); },
      "Cmd-S": function() { var b = qs('[data-act="save"]', m.el); if (b) b.click(); },
    });
    if (cmY) {
      m.setVeto(function() {
        if (cmY.isClean()) return false;
        confirmDlg("compose 配置有<b>未保存的修改</b>,确定放弃并关闭吗?", { danger: true }).then(function(ok) {
          if (ok) { cmY.markClean(); m.doClose(); }
        });
        return true;
      });
    }
    var save = async function() {
      try {
        await api("/apps/instances/" + key + "/compose", { method: "POST", body: { content: qs("#ac-yaml", m.el).value } });
        if (cmY) cmY.markClean();
        return true;
      } catch (e) { toast(e.message, "error", 6000); return false; }
    };
    qs('[data-act="save"]', m.el).onclick = async function() {
      if (await save()) { toast("配置已保存,重建容器后生效"); m.close(); }
    };
    qs('[data-act="apply"]', m.el).onclick = async function() {
      if (!(await save())) return;
      m.close();
      toast("正在按新配置重建容器...", "info");
      try {
        await api("/apps/instances/" + key + "/up", { method: "POST", body: {} });
        toast("容器已按新配置重建完成");
      } catch (e) { toast(e.message, "error", 6000); }
      var pel = document.querySelector('[data-route="apps"]');
      if (pel) PAGES.apps.render(pel);
    };
  },
  installDialog(el, t) {
    // PHP 多版本:端口默认避开已装实例的端口
    var usedPorts = {};
    (window.__phpUsedPorts || []).forEach(function(x) { usedPorts[x] = 1; });
    var defPort = function(p0) {
      var v = p0.default || "";
      if (t.key === "php" && p0.key === "Port" && usedPorts[v]) {
        var n = parseInt(v, 10) || 8088;
        while (usedPorts[String(n)]) n++;
        v = String(n);
      }
      return v;
    };
    var paramsHTML = (t.params || []).map(function(p) {
      var field;
      if (p.type === "select") {
        field = '<select class="select" data-k="' + esc(p.key) + '" style="width:100%">';
        (p.options || []).forEach(function(o) {
          field += '<option value="' + esc(o) + '"' + (o === p.default ? " selected" : "") + '>' + esc(o) + '</option>';
        });
        field += '</select>';
      } else {
        field = '<input class="input" data-k="' + esc(p.key) + '" type="' + (p.type === "password" ? "password" : "text") + '" value="' + esc(defPort(p) || "") + '" placeholder="' + esc(p.default || "请输入") + '">';
      }
      var help = p.help ? '<div class="help">' + esc(p.help) + '</div>' : '';
      return '<div class="form-item ' + (p.required ? "required" : "") + '"><label>' + esc(p.label) + '</label>' + field + help + '</div>';
    }).join("");
    var m = modal({
      title: "安装 " + t.name, width: 540,
      body: '<div class="install-head">' + appIcon(t, 52) + '<div><b>' + esc(t.name) + '</b><div class="help">' + esc(t.desc) + '<br>版本: ' + esc(t.version) + '</div></div></div>' + paramsHTML +
        '<div class="help" style="margin-top:10px;color:var(--text-3)">安装过程将拉取镜像并启动容器,请耐心等待。</div>',
      footer: '<button class="btn btn-plain" data-act="no">取消</button><button class="btn btn-primary" data-act="yes">开始安装</button>',
    });
    // 动态合并 Docker Hub 最新版本标签(静态列表可能落后于官方发布)
    (function() {
      var sel = m.el.querySelector('select[data-k="Version"]');
      if (!sel) return;
      var tplP = (t.params || []).find(function(p) { return p.key === "Version"; });
      var def = tplP ? tplP.default : sel.value;
      api("/apps/hub-tags?key=" + encodeURIComponent(t.key)).then(function(tags) {
        if (!tags || !tags.length || !qs('select[data-k="Version"]', m.el)) return;
        var merged = tags.slice();
        (tplP && tplP.options ? tplP.options : []).forEach(function(o) {
          if (merged.indexOf(o) < 0) merged.push(o);
        });
        sel.innerHTML = merged.map(function(o) {
          return '<option value="' + esc(o) + '"' + (o === def ? " selected" : "") + ">" + esc(o) + "</option>";
        }).join("");
        var help = sel.parentElement.querySelector(".help");
        if (help) help.textContent = "已自动获取 Docker Hub 最新版本(" + tags.length + " 个),当前列表按新→旧排序";
      }).catch(function() {});
    })();
    m.el.querySelector('[data-act="no"]').onclick = m.close;
    m.el.querySelector('[data-act="yes"]').onclick = function() {
      var params = {};
      m.el.querySelectorAll("[data-k]").forEach(function(inp) { params[inp.getAttribute("data-k")] = inp.value.trim(); });
      for (var pi in t.params || []) {
        var pp = t.params[pi];
        if (pp.required && !params[pp.key]) { toast(pp.label + " 不能为空", "error"); return; }
      }
      fetch("/api/v1/apps/install", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" }, body: JSON.stringify({ key: t.key, params: params }) })
        .then(function(r) { return r.json() })
        .then(function(d) {
          m.close();
          if (d.code !== 0) { toast(d.message || "安装失败", "error", 5000); return; }
          taskModal(d.data.id, function() { self.render(el); });
        });
    };
  },
};


/* ================= 数据库 ================= */
PAGES.databases = {
  title: "数据库",
  tab: {},
  SYSTEM_DBS: ["information_schema", "mysql", "performance_schema", "sys", "postgres"],
  async render(el) {
    el.innerHTML = skeletonCards(2);
    let instances = [];
    try { instances = (await api("/db/instances")) || []; }
    catch (e) { el.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    var running = instances.filter((i) => i.running);
    var notRunning = instances.filter((i) => !i.running);
    var typeNames = { mysql: "MySQL", mariadb: "MariaDB", postgresql: "PostgreSQL" };

    var self = this;
    if (!running.length) {
      // 有安装记录但容器被删:给出重建指引,而不是笼统引导"去安装"(商店会显示已安装,无路可走)
      var missingDb = "";
      try {
        var appInsts0 = (await api("/apps/instances")) || [];
        var dbTypes = ["mysql", "mariadb", "postgresql"];
        var m = appInsts0.filter(function(i) { return dbTypes.indexOf(i.key) >= 0 && i.status === "missing"; })[0];
        if (m) missingDb = ({ mysql: "MySQL", mariadb: "MariaDB", postgresql: "PostgreSQL" })[m.key] || m.key;
      } catch (e) {}
      if (missingDb) {
        el.innerHTML = '<div class="card" style="max-width:680px;margin:40px auto;text-align:center;padding:48px 32px">' +
          '<div style="margin-bottom:16px">' + iconBox("warn", 64) + "</div>" +
          '<h3 style="font-size:18px;margin-bottom:8px">' + esc(missingDb) + ' 安装记录还在,但容器已不存在</h3>' +
          '<p style="color:var(--text-3);font-size:13.5px;margin-bottom:6px">容器可能被手动删除或异常丢失;数据目录仍保留在服务器上</p>' +
          '<p style="color:var(--text-3);font-size:13.5px;margin-bottom:20px">到 <b>应用商店 → 已安装</b> 列表点「重建」即可按原配置恢复,数据不丢</p>' +
          '<a class="btn btn-primary" href="#/apps?tab=installed">前往重建</a></div>';
        return;
      }
      el.innerHTML = '<div class="card" style="max-width:680px;margin:40px auto;text-align:center;padding:48px 32px">' +
        '<div style="margin-bottom:16px">' + iconBox("database", 64) + "</div>" +
        '<h3 style="font-size:18px;margin-bottom:8px">未检测到运行中的数据库</h3>' +
        '<p style="color:var(--text-3);font-size:13.5px;margin-bottom:20px">可在应用商店一键安装 MySQL / MariaDB / PostgreSQL</p>' +
        '<a class="btn btn-primary" href="#/apps">去应用商店安装</a></div>';
      return;
    }

    // 保留当前选中的实例(此前每次重置为第一个,多实例时无法切换)
    if (!this.tab.key || !running.some(function(i) { return i.key === self.tab.key; })) {
      this.tab = { key: running[0].key, database: "", table: "" };
    }
    // phpMyAdmin 快捷入口(应用商店已安装且运行时显示真实访问地址)
    var pma = null;
    try {
      const appInsts = (await api("/apps/instances").catch(function() { return []; })) || [];
      const pmaInst = appInsts.filter(function(i) { return i.key === "phpmyadmin" && i.running; })[0];
      if (pmaInst) pma = { url: "http://" + location.hostname + ":" + (pmaInst.params && pmaInst.params.Port || "8082") + "/", label: "phpMyAdmin" };
    } catch (e) {}

    var head = '<div class="waf-hero db-hero">' +
      '<div class="waf-hero-left">' + iconBox("database", 46) +
      '<div class="stat-meta"><h3>' + esc(typeNames[this.tab.key] || this.tab.key) + " " + stateTag(true, "运行中", "已停止") + "</h3>" +
      '<div class="stat-line db-stat-line">业务库 <b id="db-stat-dbs">-</b> 个 · 数据表 <b id="db-stat-tables">-</b> 张 · 备份 <b id="db-stat-baks">-</b> 份</div></div></div>' +
      '<div class="waf-hero-right">' +
      '<button class="btn btn-sm btn-plain" id="db-rootpwd">' + I("key") + " root 密码</button>" +
      (pma ? '<a class="btn btn-sm btn-plain" href="' + esc(pma.url) + '" target="_blank">' + I("external") + " " + esc(pma.label) + "</a>" : "") +
      '<button class="btn btn-sm btn-plain" id="db-refresh" title="刷新">' + I("refresh") + "</button></div></div>";
    // 实例切换 chips(多个数据库服务同时运行时)
    var instBar = "";
    if (running.length > 1) {
      instBar = '<div class="db-chipbar">' + running.map(function(i) {
        var on = self.tab.key === i.key;
        return '<span class="db-chip' + (on ? " active" : "") + '" data-inst="' + esc(i.key) + '"><span class="dot"></span>' + esc(typeNames[i.key] || i.key) + "</span>";
      }).join("") + "</div>";
    } else if (notRunning.length) {
      instBar = '<div class="db-chipbar"><span class="settings-hint" style="margin:0">还可安装:' + notRunning.map(function(i) { return esc(typeNames[i.key] || i.key); }).join(" / ") + '(应用商店一键安装)</span></div>';
    }

    el.innerHTML = head + instBar +
      '<div class="card page-fade" style="margin-top:12px">' +
      '<div class="card-title"><span>数据库<span id="db-sys-slot" style="margin-left:8px"></span></span><span class="title-actions"><button class="btn btn-sm btn-primary" id="db-new">' + I("plus") + " 添加数据库</button></span></div>" +
      '<div id="db-table-wrap">' + skeletonTable(4, 3) + "</div></div>" +
      '<div id="db-mgr" style="margin-top:14px"></div>';

    qs("#db-refresh", el).onclick = function() { self.render(el); };
    qs("#db-rootpwd", el).onclick = function() {
      var instName = typeNames[self.tab.key] || self.tab.key;
      var pm = modal({
        title: "修改 root 密码 · " + instName, width: 460,
        body: '<div class="form-item required"><label>新密码</label><input class="input" type="password" id="db-pwd1" placeholder="至少 6 位"><div class="help">修改后面板与你的应用程序都需使用新密码连接数据库</div></div>' +
          '<div class="form-item required"><label>确认新密码</label><input class="input" type="password" id="db-pwd2"></div>',
        footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">确认修改</button>',
      });
      qs('[data-act="no"]', pm.el).onclick = pm.close;
      qs('[data-act="yes"]', pm.el).onclick = async function() {
        var p1 = qs("#db-pwd1", pm.el).value, p2 = qs("#db-pwd2", pm.el).value;
        if (p1.length < 6) { toast("新密码至少 6 位", "error"); return; }
        if (p1 !== p2) { toast("两次输入的密码不一致", "error"); return; }
        try {
          await api("/db/" + self.tab.key + "/root-password", { method: "POST", body: { new_password: p1 } });
          toast("root 密码修改成功,请牢记新密码");
          pm.close();
        } catch (e) { toast(e.message, "error", 6000); }
      };
    };
    qs("#db-new", el).onclick = async function() {
      var r = await promptDlg("新建数据库", [{ key: "name", label: "库名(小写字母/数字/下划线)", required: true }]);
      if (!r) return;
      try {
        await api("/db/" + self.tab.key + "/create-db", { method: "POST", body: { name: r.name.trim() } });
        toast("数据库已创建");
        self.render(el);
      } catch (e) { toast(e.message, "error"); }
    };
    // 实例切换
    qsa("[data-inst]", el).forEach(function(c) {
      c.onclick = function() {
        self.tab = { key: c.getAttribute("data-inst"), database: "", table: "" };
        self.render(el);
      };
    });

    await this.loadDbList(el);
  },
  async loadDbList(el) {
    var self = this;
    var key = this.tab.key;
    var wrap = qs("#db-table-wrap", el);
    if (!wrap) return;
    var dbs = [];
    try { dbs = (await api("/db/" + key + "/databases")) || []; }
    catch (e) { wrap.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    if (!qs("#db-table-wrap", el)) return;
    var user = dbs.filter(function(d) { return self.SYSTEM_DBS.indexOf(d) < 0; });
    var sys = dbs.filter(function(d) { return self.SYSTEM_DBS.indexOf(d) >= 0; });
    var showSys = this._showSys;
    var st = qs("#db-stat-dbs", el);
    if (st) st.textContent = user.length;
    // 默认选中:只在业务库里选(需在渲染卡片前确定,active 高亮才正确)。
    // 无业务库时必须置空让管理面板走空态——不能回落 dbs[0](按字母序是
    // information_schema),否则删光业务库后表列表会显示 61 张系统表
    if (!this.tab.database || dbs.indexOf(this.tab.database) < 0) {
      this.tab.database = user[0] || "";
    }
    if (!this.tab.database) {
      var stT0 = document.getElementById("db-stat-tables");
      if (stT0) stT0.textContent = "0";
    }
    // 数据库卡片(库名 + 操作),替代空旷表格
    function card(d, isSys) {
      var active = self.tab.database === d;
      return '<div class="db-card' + (active ? " active" : "") + '" data-dbname="' + esc(d) + '" title="点击管理该库">' +
        '<span class="db-c-ico">' + I("database") + "</span>" +
        '<span class="db-c-name mono"><b>' + esc(d) + "</b>" + (isSys ? ' <span class="tag tag-info">系统</span>' : "") + "</span>" +
        '<span class="db-c-ops">' +
        (isSys ? "" : '<button class="btn btn-xs btn-plain" data-dbbak="' + esc(d) + '">备份</button>' +
          '<button class="btn btn-xs btn-danger-plain" data-drop="' + esc(d) + '">删除</button>') +
        "</span></div>";
    }
    var showSysBtn = sys.length
      ? '<button class="btn btn-xs btn-plain" id="db-showsys" style="margin-left:8px">' + (showSys ? "隐藏系统库" : "显示系统库(" + sys.length + ")") + "</button>"
      : "";
    if (user.length || showSys) {
      wrap.innerHTML = '<div class="db-grid">' +
        user.map(function(d) { return card(d, false); }).join("") +
        (showSys ? sys.map(function(d) { return card(d, true); }).join("") : "") +
        "</div>";
    } else {
      wrap.innerHTML = '<div class="db-empty">' + I("database") + '<p>还没有业务数据库</p><p class="help">点击右上角「添加数据库」创建' + (sys.length ? ';系统库 ' + sys.length + ' 个已隐藏' : "") + '</p></div>';
    }

    var ssb = qs("#db-showsys", wrap);
    var slot = qs("#db-sys-slot", el);
    if (ssb && slot) {
      slot.innerHTML = ssb.outerHTML;
      ssb.remove();
      qs("#db-showsys", el).onclick = function() { self._showSys = !self._showSys; self.loadDbList(el); };
    }
    // 点击卡片 = 选中管理
    qsa("[data-dbname]", wrap).forEach(function(a) {
      a.onclick = function(e) {
        if (e.target.closest("button")) return; // 卡内按钮各自处理
        self.tab.database = a.getAttribute("data-dbname");
        self.render(el);
      };
    });
    // 备份 → 选中库并滚到详情
    qsa("[data-dbbak]", wrap).forEach(function(b) {
      b.onclick = function(e) {
        e.stopPropagation();
        self.tab.database = b.getAttribute("data-dbbak");
        self.render(el);
      };
    });
    // 删除
    qsa("[data-drop]", wrap).forEach(function(b) {
      b.onclick = async function(e) {
        e.stopPropagation();
        var d = b.getAttribute("data-drop");
        if (!(await confirmDlg("确定删除数据库 <b>" + esc(d) + "</b> 吗?库内全部数据将丢失!"))) return;
        try {
          await api("/db/" + key + "/drop-db", { method: "POST", body: { name: d } });
          toast("已删除");
          if (self.tab.database === d) self.tab.database = "";
          self.loadDbList(el);
        } catch (e) { toast(e.message, "error"); }
      };
    });
    await this.manager(qs("#db-mgr", el));
  },
  async manager(el) {
    var self = this;
    var key = this.tab.key;
    var db = this.tab.database;
    if (!el || !db) {
      el.innerHTML = "";
      return;
    }

    el.innerHTML = '<div class="card page-fade"><div class="card-title"><span>当前管理: <b class="mono">' + esc(db) + '</b><span class="tag tag-info" style="margin-left:8px">' + esc(key) + "</span></span></div>" + skeletonTable(3, 3, 140) + "</div>";

    var tables = [];
    try {
      tables = (await api("/db/" + key + "/tables?db=" + encodeURIComponent(db))) || [];
    } catch (e) { tables = []; }
    if (!qs(".card", el)) return;
    var stT = document.getElementById("db-stat-tables");
    if (stT) stT.textContent = tables.length;

    el.innerHTML = `
      <div class="grid-2 db-mgr-grid">
        <div class="card page-fade db-tbl-panel" style="padding:14px">
          <div class="card-title" style="font-size:13px;margin-bottom:10px">表列表${tables.length ? ' (' + tables.length + ")" : ""}</div>
          ${tables.length ? '<div class="db-tbl-list">' + tables.map(function(t) { return '<div class="db-tbl-item" data-tbl="' + esc(t) + '" title="' + esc(t) + '">' + I("database", "db-tbl-ico") + '<span class="mono">' + esc(t) + "</span></div>"; }).join("") + "</div>" : '<div style="font-size:12px;color:var(--text-3)">无表</div>'}
        </div>
        <div>
          <div class="card page-fade">
            <div class="card-title">SQL 控制台</div>
            <textarea class="textarea mono" id="db-sql" rows="4" placeholder="SELECT * FROM table LIMIT 20;"></textarea>
            <div style="margin-top:10px;display:flex;gap:10px;align-items:center">
              <button class="btn btn-primary" id="db-run">${I("play")} 执行</button>
              <button class="btn btn-plain" id="db-sql-clear">${I("close")} 清空</button>
              <span class="help">以 root 身份执行,支持任意 SQL;点击左侧表名自动填充查询</span>
            </div>
            <div id="db-result" style="margin-top:14px"></div>
          </div>
          <div class="card page-fade" style="margin-top:14px">
            <div class="card-title"><span>备份 / 恢复</span><span class="title-actions"><button class="btn btn-sm btn-primary" id="db-backup">${I("download")} 备份当前库</button><button class="btn btn-sm btn-plain" id="db-import">${I("upload")} 导入 SQL</button></span></div>
            <div id="db-bak-list"></div>
          </div>
        </div>
      </div>`;

    // 点击表名自动 SELECT
    qsa("[data-tbl]", el).forEach(function(n) {
      n.onclick = function() {
        var tbl = n.dataset.tbl;
        var sqlBox = qs("#db-sql", el);
        sqlBox.value = "SELECT * FROM `" + tbl + "` LIMIT 20";
        qs("#db-run", el).click();
      };
    });

    var sqlClear = qs("#db-sql-clear", el);
    if (sqlClear) sqlClear.onclick = function() { qs("#db-sql", el).value = ""; qs("#db-result", el).innerHTML = ""; };

    // SQL 执行(表格化结果)
    qs("#db-run", el).onclick = async function() {
      var sql = qs("#db-sql", el).value.trim();
      var box = qs("#db-result", el);
      if (!sql) { toast("请输入 SQL", "error"); return; }
      box.innerHTML = '<div class="boot-loading" style="height:50px"><div class="spinner"></div></div>';
      try {
        var r = await api("/db/select", { method: "POST", body: { key: key, database: db, sql: sql } });
        var cols = r.columns || [], rows = r.rows || [];
        if (!cols.length) { box.innerHTML = '<div class="help">执行成功(无返回数据)</div>'; return; }
        var html = '<div class="table-wrap"><table class="table"><thead><tr>';
        cols.forEach(function(c) { html += "<th>" + esc(c) + "</th>"; });
        html += "</tr></thead><tbody>";
        rows.forEach(function(row) {
          html += "<tr>";
          row.forEach(function(v) { html += "<td>" + (v === null ? '<span style="color:var(--text-3)">NULL</span>' : esc(String(v))) + "</td>"; });
          html += "</tr>";
        });
        html += "</tbody></table></div>";
        box.innerHTML = html;
      } catch (e) {
        box.innerHTML = '<div class="alert alert-warning">' + esc(e.message) + "</div>";
      }
    };

    // 备份
    var loadBackups = async function() {
      var listEl = qs("#db-bak-list", el);
      try {
        var list = (await api("/db/" + key + "/backups")) || [];
        var stB = document.getElementById("db-stat-baks");
        if (stB) stB.textContent = list.length;
        listEl.innerHTML = tableHTML([
          { title: "文件", render: function(r) { return '<span class="mono">' + esc(r.file) + "</span>"; } },
          { title: "大小", width: "100px", render: function(r) { return fmtBytes(r.size); } },
          { title: "时间", width: "160px", render: function(r) { return esc(r.time); } },
          { title: "操作", width: "160px", render: function(r) {
            return '<div class="ops"><button class="btn btn-sm btn-plain" data-dl="' + esc(r.file) + '">下载</button>' +
              '<button class="btn btn-sm btn-plain" data-restore="' + esc(r.file) + '">恢复</button>' +
              '<button class="btn btn-sm btn-danger-plain" data-del="' + esc(r.file) + '">删除</button></div>'; } },
        ], list, "暂无备份", "archive");
        qsa("[data-dl]", listEl).forEach(function(b) {
          b.onclick = function() {
            window.open("/api/v1/db/" + key + "/backups/download?token=" + encodeURIComponent(localStorage.getItem(TOKEN_KEY)) + "&file=" + encodeURIComponent(b.dataset.dl), "_blank");
          };
        });
        qsa("[data-del]", listEl).forEach(function(b) {
          b.onclick = async function() {
            if (!(await confirmDlg("确定删除该备份吗?"))) return;
            try { await api("/db/" + key + "/backups/" + b.dataset.del, { method: "DELETE" }); toast("已删除"); loadBackups(); }
            catch (e) { toast(e.message, "error"); }
          };
        });
        qsa("[data-restore]", listEl).forEach(function(b) {
          b.onclick = async function() {
            if (!(await confirmDlg("用备份 <b>" + esc(b.dataset.restore) + "</b> 恢复到当前库?<br><span style='font-size:12px;color:var(--text-3)'>将执行 SQL 导入,同名表数据会被覆盖</span>", { danger: true }))) return;
            try {
              var d = await api("/db/import-backup", { method: "POST", body: { key: key, database: db, file: b.dataset.restore } });
              taskModal(d.id, function() { toast("恢复完成"); });
            } catch (e) { toast(e.message, "error", 6000); }
          };
        });
      } catch (e) { listEl.innerHTML = emptyState("warn", "加载失败", e.message); }
    };
    loadBackups();
    // 导入 SQL 文件
    var impBtn = qs("#db-import", el);
    if (impBtn) impBtn.onclick = function() {
      var inp = document.createElement("input");
      inp.type = "file"; inp.accept = ".sql,.sql.gz,.gz"; inp.style.display = "none";
      inp.onchange = async function() {
        var f = inp.files[0];
        if (!f) return;
        if (!(await confirmDlg("导入 <b>" + esc(f.name) + "</b> 到 <b>" + esc(db) + "</b>?<br><span style='font-size:12px;color:var(--text-3)'>将执行 SQL 导入,同名表会被覆盖</span>", { danger: true }))) { inp.remove(); return; }
        var fd = new FormData();
        fd.append("key", key); fd.append("database", db); fd.append("file", f);
        toast("上传中...", "info");
        try {
          var r = await fetch("/api/v1/db/import", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY) }, body: fd });
          var d = await r.json();
          if (d.code !== 0) { toast(d.message || "导入失败", "error", 6000); inp.remove(); return; }
          inp.remove();
          taskModal(d.data.id, function() { toast("导入完成"); });
        } catch (e) { toast(e.message, "error", 6000); inp.remove(); }
      };
      document.body.appendChild(inp);
      inp.click();
      setTimeout(function() { if (inp.parentNode) inp.remove(); }, 60000);
    };
    qs("#db-backup", el).onclick = async function() {
      toast("正在备份(大库可能需要数分钟)...", "info", 5000);
      try {
        await api("/db/backup", { method: "POST", body: { key: key, database: db } });
        toast("备份完成");
        loadBackups();
      } catch (e) { toast(e.message, "error", 5000); }
    };
  },
};

/* ================= 计划任务 ================= */
PAGES.cron = {
  title: "计划任务",
  async render(el) {
    el.innerHTML = '<div class="card"><div class="card-title">计划任务<button class="btn btn-sm btn-primary" id="btn-add-cron">' + I("plus") + ' 创建任务</button></div>' +
      '<div id="cron-table">' + skeletonTable(6, 6) + "</div></div>";
    var self = this;
    var list;
    try { list = (await api("/crons")) || []; }
    catch (e) { qs("#cron-table", el).innerHTML = emptyState("warn", "加载失败", e.message); return; }
    window.__cronList = list;
    var typeTag = { shell: '<span class="tag tag-primary">脚本</span>', backup: '<span class="tag tag-warning">备份</span>', logrotate: '<span class="tag tag-info">日志切割</span>', dbbackup: '<span class="tag tag-success">数据库备份</span>', trashclean: '<span class="tag tag-purple">回收站清理</span>', panelbackup: '<span class="tag tag-warning">面板备份</span>', containerlog: '<span class="tag tag-info">容器日志</span>' };
    var html;
    if (!list.length) {
      html = emptyState("clock", "暂无计划任务", "支持 Shell 脚本、目录备份、网站日志切割三种类型");
    } else {
      html = '<div class="table-wrap"><table class="table"><thead><tr><th>任务名称</th><th style="width:90px">类型</th><th style="width:130px">执行周期</th><th style="width:170px">上次执行</th><th style="width:80px">结果</th><th style="width:130px">下次执行</th><th style="width:60px">启用</th><th style="width:280px">操作</th></tr></thead><tbody>' +
        list.map(function(r) {
          var stat = r.last_status === "success" ? '<span class="tag tag-success">成功</span>' : r.last_status === "failed" ? '<span class="tag tag-danger">失败</span>' : '<span class="help">未执行</span>';
          return '<tr><td><b>' + esc(r.name) + "</b></td>" +
            "<td>" + (typeTag[r.type] || esc(r.type)) + "</td>" +
            '<td class="mono">' + esc(r.spec) + "</td>" +
            '<td class="waf-nowrap">' + esc(r.last_run || "-") + "</td>" +
            "<td>" + stat + "</td>" +
            '<td class="waf-nowrap">' + esc(r.next_run || "-") + "</td>" +
            '<td><span class="switch ' + (r.enabled ? "on" : "") + '" data-id="' + r.id + '"></span></td>' +
            '<td><div class="ops"><button class="btn btn-sm btn-primary" data-cedit="' + r.id + '">编辑</button>' +
            '<button class="btn btn-sm btn-plain" data-run="' + r.id + '">执行</button>' +
            '<button class="btn btn-sm btn-plain" data-log="' + r.id + '">日志</button>' +
            '<button class="btn btn-sm btn-danger-plain" data-del="' + r.id + '">删除</button></div></td></tr>';
        }).join("") + "</tbody></table></div>";
    }
    qs("#cron-table", el).innerHTML = html;
    bindSwitch("#cron-table .switch", function(id, on) {
      return api("/crons/" + id + "/toggle", { method: "POST", body: { enabled: on } }).then(function() { toast(on ? "已启用" : "已停用"); });
    });
    qsa("[data-run]", el).forEach(function(b) {
      b.onclick = function() {
        api("/crons/" + b.dataset.run + "/run", { method: "POST", body: "{}" }).then(function() { toast("已触发执行"); setTimeout(function() { self.render(el); }, 2500); });
      };
    });
    qsa("[data-log]", el).forEach(function(b) {
      b.onclick = function() { self.logDialog(b.dataset.log); };
    });
    qsa("[data-del]", el).forEach(function(b) {
      b.onclick = function() {
        confirmDlg("确定删除该任务吗?执行日志将一并清除。").then(function(ok) {
          if (!ok) return;
          api("/crons/" + b.dataset.del, { method: "DELETE" }).then(function() { toast("已删除"); self.render(el); });
        });
      };
    });
    qs("#btn-add-cron", el).onclick = function() { self.jobDialog(null, el); };
    qsa("[data-cedit]", el).forEach(function(b) {
      b.onclick = function() {
        var job = null;
        try { job = (window.__cronList || []).find(function(x) { return x.id === b.getAttribute("data-cedit"); }); } catch (e) {}
        if (!job) { toast("任务数据未加载", "error"); return; }
        self.jobDialog(job, el);
      };
    });
  },
  /* 创建/编辑计划任务(job 为空 = 创建) */
  jobDialog(job, el) {
    var self = this;
    var isEdit = !!job;
    job = job || {};
    var m = modal({
      title: (isEdit ? "编辑计划任务 · " : "创建计划任务") + (isEdit ? job.name : ""), width: 600,
      body:
        '<div class="form-item required"><label>任务名称</label><input class="input" id="cr-name" value="' + esc(job.name || "") + '" placeholder="如 每日凌晨备份网站"></div>' +
        '<div class="form-item required"><label>任务类型</label><select class="select" id="cr-type"' + (isEdit ? " disabled" : "") + '>' +
        '<option value="shell">Shell 脚本</option><option value="backup">目录备份</option><option value="dbbackup">数据库备份</option><option value="logrotate">网站日志切割</option><option value="trashclean">回收站清理</option><option value="panelbackup">面板备份</option><option value="containerlog">容器日志清理</option></select></div>' +
        (isEdit ? '<div class="help" style="margin:6px 0 12px">类型创建后不可修改,如需更换类型请新建任务</div>' : "") +
        '<div class="form-item required"><label>执行周期</label><select class="select" id="cr-cyc" style="max-width:240px">' +
        '<option value="day">每天</option><option value="week">每周</option><option value="month">每月</option>' +
        '<option value="hour">每小时</option><option value="minute">每 N 分钟</option><option value="custom">自定义(cron)</option></select></div>' +
        '<div id="cr-cyc-fields" style="margin-bottom:12px"></div>' +
        '<div id="cr-shell">' +
        '<div class="form-item required"><label>脚本命令</label><textarea class="textarea mono" id="cr-cmd" rows="4">' + esc(job.command || "") + "</textarea></div></div>" +
        '<div id="cr-backup" style="display:none">' +
        '<div class="form-item required"><label>备份源目录</label><input class="input mono" id="cr-src" value="' + esc(job.backup_src || "") + '"></div>' +
        '<div class="form-item required"><label>备份保存目录</label><input class="input mono" id="cr-dir" value="' + esc(job.backup_dir || "") + '"></div>' +
        '<div class="form-item"><label>保留份数</label><input class="input" id="cr-keep" type="number" value="' + (job.keep || 5) + '" style="max-width:140px"></div>' +
        '<div class="form-item"><label style="display:flex;gap:8px;align-items:center"><input type="checkbox" id="cr-remote" ' + (job.remote ? "checked" : "") + '> 同时上传到 SFTP 远程(需先在 面板设置→备份与清理 中配置)</label></div></div>' +
        '<div id="cr-dbbackup" style="display:none">' +
        '<div class="form-item required"><label>数据库类型</label><select class="select" id="cr-dbkey"><option value="mysql">MySQL</option><option value="mariadb">MariaDB</option><option value="postgresql">PostgreSQL</option></select></div>' +
        '<div class="form-item required"><label>库名</label><input class="input mono" id="cr-dbname" value="' + esc(job.db_name || "") + '"></div>' +
        '<div class="form-item"><label>保留份数</label><input class="input" id="cr-db-keep" type="number" value="' + (job.keep || 7) + '" style="max-width:140px"></div>' +
        '<div class="form-item"><label style="display:flex;gap:8px;align-items:center"><input type="checkbox" id="cr-db-remote" ' + (job.remote ? "checked" : "") + '> 同时上传到 SFTP 远程</label></div></div>' +
        '<div id="cr-logrotate" style="display:none">' +
        '<div class="help" style="line-height:1.7">轮转网关(Nginx/OpenResty)容器的站点访问/错误日志,自动压缩归档到面板日志目录,防止磁盘被日志撑满。无需额外参数。</div>' +
        '<div class="form-item" style="margin-top:10px"><label>归档保留份数</label><input class="input" id="cr-lr-keep" type="number" value="' + (job.keep || 10) + '" style="max-width:140px"></div></div>' +
        '<div id="cr-panelbackup" style="display:none">' +
        '<div class="help" style="line-height:1.7">定时打包面板配置(面板设置/SSL 证书/站点配置/应用 compose),自动滚动保留。建议每 1~7 天一次;可勾选上传到 SFTP 远程异地容灾。</div>' +
        '<div class="form-item" style="margin-top:10px"><label>保留份数</label><input class="input" id="cr-pb-keep" type="number" value="' + (job.keep || 10) + '" style="max-width:140px"></div>' +
        '<div class="form-item"><label style="display:flex;gap:8px;align-items:center"><input type="checkbox" id="cr-pb-remote" ' + (job.remote ? "checked" : "") + '> 同时上传到 SFTP 远程</label></div></div>' +
        '<div id="cr-containerlog" style="display:none">' +
        '<div class="help" style="line-height:1.7">清理所有运行中容器的 json-file 日志文件,并顺带清空网关(Nginx/OpenResty)容器的站点错误日志 /var/log/nginx/error.log,防止日志无限增长占满磁盘。不影响容器运行;站点访问日志请用「网站日志切割」归档。</div>' +
        '<div class="form-item" style="margin-top:10px"><label>执行频率说明</label><div class="help" style="margin:0">建议每周执行一次(0 3 * * 0)</div></div></div>' +
        '<div id="cr-trashclean" style="display:none">' +
        '<div class="help" style="line-height:1.7">自动清理回收站中删除超过指定天数的文件,释放磁盘空间。</div>' +
        '<div class="form-item" style="margin-top:10px"><label>保留天数</label><input class="input" id="cr-tc-keep" type="number" value="' + (job.keep || 30) + '" style="max-width:140px"></div></div>',
      footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">' + (isEdit ? "保存修改" : "创 建") + "</button>",
    });
    qs("#cr-type", m.el).value = job.type || "shell";
    // ---- 执行周期可视化选择 ----
    var WD = [["1", "周一"], ["2", "周二"], ["3", "周三"], ["4", "周四"], ["5", "周五"], ["6", "周六"], ["0", "周日"]];
    function pad2(n) { return (n < 10 ? "0" : "") + n; }
    function parseSpecUI(spec) {
      // 常见表达式反解为可视化类型,解不出则走自定义
      var p = String(spec || "").trim().split(/\s+/);
      if (p.length !== 5) return { t: "custom", spec: spec };
      var m = p[0], h = p[1], dom = p[2], mon = p[3], dow = p[4];
      var mm = m.match(/^\*\/(\d+)$/);
      if (mm && h === "*" && dom === "*" && mon === "*" && dow === "*") return { t: "minute", n: mm[1] };
      if (/^\d+$/.test(m) && h === "*" && dom === "*" && mon === "*" && dow === "*") return { t: "hour", m: m };
      if (/^\d+$/.test(m) && /^\d+$/.test(h) && dom === "*" && mon === "*" && dow === "*") return { t: "day", time: pad2(+h) + ":" + pad2(+m) };
      if (/^\d+$/.test(m) && /^\d+$/.test(h) && dom === "*" && mon === "*" && /^\d+$/.test(dow)) return { t: "week", dow: dow, time: pad2(+h) + ":" + pad2(+m) };
      if (/^\d+$/.test(m) && /^\d+$/.test(h) && /^\d+$/.test(dom) && mon === "*" && dow === "*") return { t: "month", dom: dom, time: pad2(+h) + ":" + pad2(+m) };
      return { t: "custom", spec: spec };
    }
    function scheduleFieldsHTML(t, v) {
      v = v || {};
      var time = '<input class="input" type="time" id="cyc-time" value="' + (v.time || "03:00") + '" style="max-width:140px">';
      if (t === "day") return '<span>执行时间</span>' + time;
      if (t === "week") return '<select class="select" id="cyc-dow" style="max-width:140px">' + WD.map(function(w2) { return '<option value="' + w2[0] + '"' + (String(v.dow) === w2[0] ? " selected" : "") + ">" + w2[1] + "</option>"; }).join("") + '</select><span>执行时间</span>' + time;
      if (t === "month") return '每月 <input class="input" type="number" min="1" max="31" id="cyc-dom" value="' + (v.dom || 1) + '" style="max-width:90px"> 号 执行时间 ' + time;
      if (t === "hour") return '每小时的第 <input class="input" type="number" min="0" max="59" id="cyc-min" value="' + (v.m || 0) + '" style="max-width:90px"> 分钟执行';
      if (t === "minute") return '每 <input class="input" type="number" min="1" max="59" id="cyc-n" value="' + (v.n || 30) + '" style="max-width:90px"> 分钟执行一次';
      if (t === "custom") return '<input class="input mono" id="cyc-custom" value="' + esc(v.spec || "*/30 * * * *") + '" placeholder="分 时 日 月 周" style="max-width:260px"><div class="help">标准 5 位 cron 表达式,如 0 3 * * * = 每天 3 点</div>';
      return "";
    }
    function scheduleSync() {
      var t = qs("#cr-cyc", m.el).value;
      var box = qs("#cr-cyc-fields", m.el);
      box.innerHTML = '<div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap;font-size:13px;color:var(--text-2)">' + scheduleFieldsHTML(t, schedVal) + "</div>";
      if (schedVal && t === schedVal.t) { /* 编辑预填时恢复解析出的细节 */ }
      schedVal = null;
    }
    function scheduleBuild() {
      var t = qs("#cr-cyc", m.el).value;
      var gv = function(id) { var el2 = qs("#" + id, m.el); return el2 ? el2.value : ""; };
      if (t === "custom") return gv("cyc-custom").trim();
      if (t === "minute") { var n = parseInt(gv("cyc-n")) || 30; return "*/" + Math.min(Math.max(n, 1), 59) + " * * * *"; }
      if (t === "hour") return (parseInt(gv("cyc-min")) || 0) + " * * * *";
      var tm = (gv("cyc-time") || "03:00").split(":");
      var mi = parseInt(tm[1]) || 0, hi = parseInt(tm[0]) || 0;
      if (t === "day") return mi + " " + hi + " * * *";
      if (t === "week") return mi + " " + hi + " * * " + (gv("cyc-dow") || "1");
      if (t === "month") { var dd = parseInt(gv("cyc-dom")) || 1; return mi + " " + hi + " " + Math.min(Math.max(dd, 1), 31) + " * *"; }
      return "0 3 * * *";
    }
    var schedVal = parseSpecUI(job.spec || "0 3 * * *");
    qs("#cr-cyc", m.el).value = schedVal.t;
    scheduleSync();
    qs("#cr-cyc", m.el).onchange = scheduleSync;
    // ---- 执行周期结束 ----

    var toggle = function() {
      var t = qs("#cr-type", m.el).value;
      qs("#cr-shell", m.el).style.display = t === "shell" ? "" : "none";
      qs("#cr-backup", m.el).style.display = t === "backup" ? "" : "none";
      qs("#cr-logrotate", m.el).style.display = t === "logrotate" ? "" : "none";
      qs("#cr-dbbackup", m.el).style.display = t === "dbbackup" ? "" : "none";
      qs("#cr-trashclean", m.el).style.display = t === "trashclean" ? "" : "none";
      qs("#cr-panelbackup", m.el).style.display = t === "panelbackup" ? "" : "none";
      qs("#cr-containerlog", m.el).style.display = t === "containerlog" ? "" : "none";
    };
    qs("#cr-type", m.el).onchange = toggle;
    toggle();
    qs('[data-act="no"]', m.el).onclick = m.close;
    qs('[data-act="yes"]', m.el).onclick = async function() {
      var t = qs("#cr-type", m.el).value;
      var body = {
        id: job.id || "",
        name: qs("#cr-name", m.el).value.trim(),
        type: t,
        spec: scheduleBuild(),
        command: qs("#cr-cmd", m.el) ? qs("#cr-cmd", m.el).value : "",
        backup_src: qs("#cr-src", m.el) ? qs("#cr-src", m.el).value.trim() : "",
        backup_dir: qs("#cr-dir", m.el) ? qs("#cr-dir", m.el).value.trim() : "",
        db_key: qs("#cr-dbkey", m.el) ? qs("#cr-dbkey", m.el).value : "",
        db_name: qs("#cr-dbname", m.el) ? qs("#cr-dbname", m.el).value.trim() : "",
        keep: (t === "logrotate" ? parseInt(qs("#cr-lr-keep", m.el).value) || 10 :
               t === "dbbackup" ? parseInt(qs("#cr-db-keep", m.el).value) || 7 :
               t === "trashclean" ? parseInt(qs("#cr-tc-keep", m.el).value) || 30 :
               t === "panelbackup" ? parseInt(qs("#cr-pb-keep", m.el).value) || 10 :
               parseInt(qs("#cr-keep", m.el).value) || 5),
        remote: (qs("#cr-remote", m.el) && qs("#cr-remote", m.el).checked) || (qs("#cr-db-remote", m.el) && qs("#cr-db-remote", m.el).checked) || (qs("#cr-pb-remote", m.el) && qs("#cr-pb-remote", m.el).checked) || false,
        enabled: job.enabled !== undefined ? job.enabled : true,
      };
      if (!body.name) { toast("任务名称不能为空", "error"); return; }
      if (t === "shell" && !body.command.trim()) { toast("脚本命令不能为空", "error"); return; }
      if (t === "backup" && (!body.backup_src || !body.backup_dir)) { toast("请填写备份源与保存目录", "error"); return; }
      if (t === "dbbackup" && !body.db_name.trim()) { toast("请填写库名", "error"); return; }
      try {
        if (isEdit) await api("/crons", { method: "PUT", body: body });
        else await api("/crons", { method: "POST", body: body });
        toast(isEdit ? "任务已更新" : "任务已创建");
        m.close();
        self.render(qs('[data-route="cron"]') || document.createElement("div"));
      } catch (e) { toast(e.message, "error", 6000); }
    };
  },
  async logDialog(id) {
    var self = this;
    var m = modal({
      title: "任务执行日志", width: 720,
      body: '<pre class="am-raw" id="cr-log" style="max-height:420px">加载中...</pre>',
      footer: '<button class="btn btn-plain" data-act="clear">清空日志</button><button class="btn btn-plain" data-act="refresh">刷新</button><button class="btn btn-primary" data-act="close">关 闭</button>',
    });
    var load = async function() {
      try {
        var c = await api("/crons/" + id + "/log");
        var pre = qs("#cr-log", m.el);
        if (pre) { pre.textContent = c || "(暂无执行记录,点「执行」触发一次试试)"; pre.scrollTop = pre.scrollHeight; }
      } catch (e) { toast(e.message, "error"); }
    };
    load();
    qs('[data-act="close"]', m.el).onclick = m.close;
    qs('[data-act="refresh"]', m.el).onclick = load;
    qs('[data-act="clear"]', m.el).onclick = async function() {
      await api("/crons/" + id + "/log/clear", { method: "POST", body: {} });
      toast("日志已清空");
      load();
    };
  },
};

/* ================= 终端 ================= */
PAGES.terminal = {
  title: "终端",
  ws: null,
  _onResize: null,
  async render(el) {
    el.innerHTML = "";
    // 支持 #/terminal?container=kun-xxx 进入指定容器
    var container = "";
    var qm = location.hash.indexOf("?");
    if (qm >= 0) {
      var usp = new URLSearchParams(location.hash.slice(qm + 1));
      container = (usp.get("container") || "").replace(/[^a-z0-9_-]/gi, "");
    }
    this.container = container;
    var pageTitle = container ? "容器终端 · " + container : "Web 终端";
    var page = document.createElement("div");
    page.className = "term-page page-fade";
    page.innerHTML = '<div class="term-header"><div class="left">' + I("terminal") + ' <b>' + esc(pageTitle) + '</b><span class="term-status off" id="t-status">连接中...</span></div><div style="display:flex;gap:10px;align-items:center"><button class="btn btn-sm btn-plain" id="t-reconnect">重连</button><button class="btn btn-sm btn-plain" id="t-clear">清屏</button></div></div><div class="term-body" id="t-body"><pre class="term-out" id="t-out"></pre></div><div class="term-input-hint">' + (container ? "输入 exit 退出容器" : "Ctrl+C 中断 · Ctrl+D 退出 · Tab 补全") + '</div>';
    el.appendChild(page);
    var out = qs("#t-out", page), status = qs("#t-status", page), body = qs("#t-body", page);
    var send = function(obj) { if (this.ws && this.ws.readyState === 1) this.ws.send(JSON.stringify(obj)); }.bind(this);
    var write = function(s2) { out.textContent += s2; body.scrollTop = body.scrollHeight; };
    var decodeB64 = function(s2) { var bin = atob(s2); var bytes = new Uint8Array(bin.length); for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i); return new TextDecoder("utf-8").decode(bytes); };
    var encodeB64 = function(s2) { return btoa(String.fromCharCode.apply(null, new TextEncoder().encode(s2))); };
    var self = this;
    var sendResize = function() { send({ t: "resize", cols: Math.max(40, Math.floor(body.clientWidth / 8.1)), rows: Math.max(10, Math.floor(body.clientHeight / 20.25)) }); };
    var connect = function() {
      var proto = location.protocol === "https:" ? "wss://" : "ws://";
      status.textContent = "连接中..."; status.className = "term-status off";
      var tk = "";
      fetch("/api/v1/terminal/ticket", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" }, body: "{}" })
        .then(function(r) { return r.json() }).then(function(d) {
          tk = d.data.ticket;
          var wsURL = "/api/v1/terminal?ticket=" + encodeURIComponent(tk);
          if (self.container) wsURL += "&container=" + encodeURIComponent(self.container);
          var ws = new WebSocket(proto + location.host + wsURL);
          self.ws = ws;
          ws.onopen = function() { status.textContent = "已连接"; status.className = "term-status on"; sendResize(); page.focus(); };
          ws.onmessage = function(ev) { try { var m2 = JSON.parse(ev.data); if (m2.t === "o") write(decodeB64(m2.d)); } catch(e) {} };
          ws.onclose = function() { status.textContent = "已断开"; status.className = "term-status off"; };
        }).catch(function() { status.textContent = "鉴权失败"; });
    };
    page.tabIndex = -1; page.style.outline = "none";
    page.addEventListener("click", function() { page.focus(); });
    page.addEventListener("keydown", function(ev) {
      if (!self.ws || self.ws.readyState !== 1) return;
      var data = null;
      if (ev.key === "Enter") data = "\r";
      else if (ev.key === "Backspace") data = "\x7f";
      else if (ev.key === "Tab") { ev.preventDefault(); data = "\t"; }
      else if (ev.ctrlKey && ev.key === "c") data = "\x03";
      else if (ev.ctrlKey && ev.key === "d") data = "\x04";
      else if (ev.key === "ArrowUp") data = "\x1b[A";
      else if (ev.key === "ArrowDown") data = "\x1b[B";
      else if (!ev.ctrlKey && !ev.metaKey && !ev.altKey && ev.key.length === 1) data = ev.key;
      if (data !== null) { ev.preventDefault(); send({ t: "i", d: encodeB64(data) }); }
    });
    this._onResize = debounce(sendResize, 200);
    window.addEventListener("resize", this._onResize);
    qs("#t-reconnect", page).onclick = connect;
    qs("#t-clear", page).onclick = function() { out.textContent = ""; };
    connect();
    setTimeout(function() { page.focus(); }, 120);
  },
  cleanup: function() {
    try { if (this.ws) this.ws.close(); } catch (e) {}
    this.ws = null;
    if (this._onResize) { window.removeEventListener("resize", this._onResize); this._onResize = null; }
  },
};

/* ================= WAF 防火墙 ================= */
PAGES.plugins = {
  title: "WAF 防火墙",
  tab: "overview",
  st: null,
  tabsDef: [
    { key: "overview", label: "总览" },
    { key: "global", label: "全局设置" },
    { key: "cc", label: "CC 防御" },
    { key: "lists", label: "黑白名单" },
    { key: "logs", label: "拦截日志" },
  ],
  RULE_LABELS: { sql: "SQL 注入", xss: "XSS 跨站", lfi: "文件包含", cmd: "命令执行", scan: "扫描器" },
  MODE_INFO: {
    block: { name: "拦截模式", desc: "命中规则直接返回 403,同时记录日志" },
    alert: { name: "告警模式", desc: "放行请求但记录,同一 IP 多次触发将按封禁策略临时封禁" },
    monitor: { name: "监控模式", desc: "仅记录不拦截,适合上线初期观察误报" },
  },
  async render(el) {
    clearPageTimers();
    var self = this;
    var info;
    try { info = await api("/waf"); }
    catch (e) { el.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    if (el.dataset.route !== "securityhub" && el.dataset.route !== "plugins") { /* 允许深链 */ }
    if (!info.installed) {
      el.innerHTML = '<div class="card" style="max-width:720px;margin:40px auto;text-align:center;padding:48px 32px">' +
        '<div style="margin-bottom:16px">' + iconBox("shield", 64) + "</div>" +
        '<h3 style="font-size:18px;margin-bottom:8px">WAF 防火墙</h3>' +
        '<p style="color:var(--text-3);font-size:13.5px;margin-bottom:6px">CC 攻击防护、IP/UA 黑白名单、URL 白名单、SQL 注入 / XSS / 文件包含 / 命令执行 / 扫描器拦截、IP 临时封禁、拦截日志</p>' +
        '<p style="color:var(--text-3);font-size:12.5px;margin-bottom:20px">依赖: Nginx 或 OpenResty(应用商店一键安装)<br><span style="font-size:11.5px">对 Nginx 开启时,面板会自动编译替换为带 Lua 引擎的 nginx 镜像(首次约 5-8 分钟),站点配置不受影响</span></p>' +
        '<button class="btn btn-primary" id="waf-install">' + I("download") + ' 安装插件</button> ' +
        '<a class="btn btn-plain" href="#/apps">去安装 Nginx</a></div>';
      qs("#waf-install", el).onclick = function() {
        api("/plugins/waf/install", { method: "POST", body: {} }).then(function(d) {
          if (d && d.id) taskModal(d.id, function() { self.render(el); });
          else toast("安装任务创建失败", "error");
        }).catch(function(e) { toast(e.message, "error", 6000); });
      };
      return;
    }
    this.st = info.settings || {};
    var st = this.st;
    var modeName = (this.MODE_INFO[st.mode] || this.MODE_INFO.block).name;

    var stats = info.stats || {};
    var head = '<div class="waf-hero">' +
      '<div class="waf-hero-left">' + iconBox("shield", 46) +
      '<div class="stat-meta"><h3>WAF 防火墙 ' + stateTag(info.active, "防护中", "已停用") + "</h3>" +
      '<div class="stat-line"><span class="tag tag-primary">' + esc(modeName) + "</span> · Nginx + Lua 引擎 · v" + esc((info.settings && info.settings.version) || "1.0") + "</div></div></div>" +
      '<div class="waf-hero-mid">' +
      '<div class="wh-stat danger"><b>' + (stats.today || 0) + "</b><span>今日拦截</span></div>" +
      '<div class="wh-stat"><b>' + (stats.total || 0) + "</b><span>累计拦截</span></div>" +
      '<div class="wh-stat"><b>' + ((stats.hours || [])[23] || 0) + "</b><span>最近 1 小时</span></div>" +
      "</div>" +
      '<div class="waf-hero-right">' +
      '<div class="wh-toggle"><span class="switch' + (st.enabled ? " on" : "") + '" id="waf-hero-switch"></span><span>' + (st.enabled ? "防护开启" : "防护关闭") + "</span></div>" +
      '<button class="btn btn-sm btn-plain" id="waf-refresh" title="刷新">' + I("refresh") + "</button>" +
      '<button class="btn btn-sm btn-danger-plain" id="waf-uninstall">卸载</button></div></div>';
    var tabs = '<div class="hub-tabs">';
    this.tabsDef.forEach(function(t) {
      tabs += '<div class="hub-tab ' + (self.tab === t.key ? "active" : "") + '" data-tab="' + t.key + '">' + t.label + "</div>";
    });
    tabs += '</div><div id="waf-body">' + skeletonTable(5, 4) + "</div>";
    el.innerHTML = head + '<div class="card" style="margin-top:12px">' + tabs + "</div>";

    qs("#waf-hero-switch", el).onclick = function() {
      st.enabled = !st.enabled;
      this.classList.toggle("on", st.enabled);
      var label = this.nextElementSibling;
      if (label) label.textContent = st.enabled ? "防护开启" : "防护关闭";
      api("/waf/settings", { method: "POST", body: st }).then(function() {
        toast(st.enabled ? "WAF 防护已开启" : "WAF 防护已关闭", st.enabled ? "success" : "warning");
      }).catch(function(e) {
        toast(e.message, "error");
        st.enabled = !st.enabled;
        self.render(el);
      });
    };
    qs("#waf-refresh", el).onclick = function() { self.render(el); };
    qs("#waf-uninstall", el).onclick = function() {
      confirmDlg("确定卸载 WAF 插件吗?所有站点将恢复无防护状态。", { danger: true }).then(function(ok) {
        if (!ok) return;
        api("/plugins/waf/uninstall", { method: "POST", body: "{}" }).then(function() {
          toast("已卸载"); setTimeout(function() { self.render(el); }, 1000);
        });
      });
    };
    qsa(".hub-tab", el).forEach(function(t) {
      t.onclick = function() {
        self.tab = t.getAttribute("data-tab");
        qsa(".hub-tab", el).forEach(function(x) { x.classList.remove("active"); });
        t.classList.add("active");
        self.renderTab(qs("#waf-body", el));
      };
    });
    this.renderTab(qs("#waf-body", el));
  },
  renderTab(body) {
    var map = { overview: "tabOverview", global: "tabGlobal", cc: "tabCC", lists: "tabLists", logs: "tabLogs" };
    if (this[map[this.tab]]) this[map[this.tab]](body);
  },
  /* ---- 总览 ---- */
  async tabOverview(body) {
    var self = this;
    var info;
    try { info = await api("/waf"); }
    catch (e) { body.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    if (!body.isConnected) return;
    var stats = info.stats || {};
    var hours = stats.hours || [];
    var st = info.settings || {};
    var modeName = (this.MODE_INFO[st.mode] || this.MODE_INFO.block).name;

    var maxH = 1;
    hours.forEach(function(v) { if (v > maxH) maxH = v; });
    var bars = "";
    for (var i = 0; i < 24; i++) {
      var h = Math.round((hours[i] || 0) / maxH * 100);
      bars += '<div class="waf-hbar"' + ' title="' + (String(i).length < 2 ? "0" : "") + i + ":00 前  " + (hours[i] || 0) + ' 次"><i style="height:' + Math.max(hours[i] ? 8 : 3, h) + '%"></i></div>';
    }
    var totalRule = 0;
    (stats.by_rule || []).forEach(function(x) { totalRule += x.v; });
    var ruleRows = (stats.by_rule || []).map(function(x) {
      var label = self.RULE_LABELS[x.k] || x.k || "未知";
      var pct = totalRule ? Math.round(x.v * 100 / totalRule) : 0;
      return '<div class="waf-dist-row"><span class="waf-dist-name">' + esc(label) + '</span><div class="waf-dist-bar"><i style="width:' + pct + '%"></i></div><b>' + x.v + "</b></div>";
    }).join("") || '<div class="help">暂无拦截数据</div>';
    var ipRows = (stats.by_ip || []).map(function(x) {
      return '<div class="waf-dist-row"><span class="waf-dist-name mono">' + esc(x.k) + '</span><div class="waf-dist-bar"><i style="width:' + (stats.today ? Math.round(x.v * 100 / Math.max(totalRule, 1)) : 0) + '%"></i></div><b>' + x.v + "</b></div>";
    }).join("") || '<div class="help">暂无数据</div>';

    body.innerHTML =
      '<div class="kpi-strip" style="grid-template-columns:repeat(4,1fr)">' +
      '<div class="card kpi-card" style="cursor:default"><span class="kpi-ico" style="color:#f56c6c">' + I("shield2") + '</span><div class="kpi-num"><b>' + (stats.today || 0) + "</b><span>今日拦截</span></div></div>" +
      '<div class="card kpi-card" style="cursor:default"><span class="kpi-ico" style="color:#94a3b8">' + I("shield") + '</span><div class="kpi-num"><b>' + (stats.total || 0) + "</b><span>累计拦截</span></div></div>" +
      '<div class="card kpi-card" style="cursor:default"><span class="kpi-ico" style="color:#f59e0b">' + I("pulse") + '</span><div class="kpi-num"><b>' + (hours[23] || 0) + "</b><span>最近 1 小时</span></div></div>" +
      '<div class="card kpi-card" style="cursor:default"><span class="kpi-ico" style="color:#0ea5e9">' + I("bolt") + '</span><div class="kpi-num"><b style="font-size:16px">' + esc(modeName.replace("模式", "")) + "</b><span>当前模式</span></div></div>" +
      "</div>" +
      '<div class="card" style="margin-top:14px;padding:16px 18px"><div class="card-title" style="margin-bottom:10px">24 小时拦截趋势</div><div class="waf-hbars">' + bars + "</div>" +
      '<div style="display:flex;justify-content:space-between;font-size:11px;color:var(--text-3)"><span>24 小时前</span><span>现在</span></div></div>' +
      '<div class="grid-2" style="margin-top:14px">' +
      '<div class="card" style="margin:0;padding:16px 18px"><div class="card-title" style="margin-bottom:10px">攻击类型分布</div>' + ruleRows + "</div>" +
      '<div class="card" style="margin:0;padding:16px 18px"><div class="card-title" style="margin-bottom:10px">攻击来源 IP Top6</div>' + ipRows + "</div></div>";
    setPageTimer(function() { if (document.visibilityState === "visible" && body.isConnected && self.tab === "overview") self.tabOverview(body); }, 15000);
  },
  /* ---- 全局设置:左侧锚点导航 + 分组卡片 ---- */
  tabGlobal(body) {
    var self = this;
    var st = this.st;
    var grp = [["sql", "SQL 注入", "union select、sleep() 等注入特征"], ["xss", "XSS 跨站", "script 标签、事件注入等"], ["lfi", "文件包含", "目录穿越、/etc/passwd 等"], ["cmd", "命令执行", "cat /etc、system() 等"], ["scan", "扫描器", "常见扫描器 UA 与探测行为"], ["sensitive", "敏感文件", ".git/.env/.sql 备份泄露"], ["webshell", "Webshell 木马", "eval($_POST)、一句话木马、反序列化"]];
    var modeCards = ["block", "alert", "monitor"].map(function(m) {
      var on = st.mode === m;
      return '<div class="waf-mode-card ' + (on ? "active" : "") + '" data-mode="' + m + '"><b>' + self.MODE_INFO[m].name + "</b><div>" + self.MODE_INFO[m].desc + "</div></div>";
    }).join("");
    var ruleItems = grp.map(function(g) {
      var on = !st.rule_groups || st.rule_groups[g[0]] !== false;
      return '<div class="waf-rule-item"><label><b>' + g[1] + '</b><span class="settings-hint">' + g[2] + '</span></label><span class="switch' + (on ? " on" : "") + '" data-grp="' + g[0] + '"></span></div>';
    }).join("");
    var navDef = [["basic", "shield2", "基础防护"], ["rules", "search", "检测规则"], ["enhance", "bolt", "增强防护"], ["autoban", "stop", "自动封禁"], ["ratelimit", "pulse", "限速防护"], ["method", "tool", "方法管控"], ["spider", "globe", "蜘蛛白名单"]];
    var nav = navDef.map(function(n) { return '<a data-sec="' + n[0] + '">' + I(n[1]) + n[2] + "</a>"; }).join("");
    body.innerHTML =
      '<div class="waf-set-wrap"><div class="waf-set-nav">' + nav + '</div><div class="waf-set-body">' +
      '<div class="waf-set-sec" data-sec="basic"><div class="waf-sec-head">' + I("shield2") + "基础防护</div>" +
      '<div class="settings-row"><label style="min-width:170px">启用防火墙<div class="settings-hint" style="margin:0">关闭后所有站点不设防</div></label><label class="radio-line"><input type="checkbox" id="waf-enable" ' + (st.enabled ? "checked" : "") + "> 启用</label></div>" +
      '<div class="settings-section-title" style="margin:14px 0 10px">拦截模式</div><div class="waf-mode-grid">' + modeCards + "</div></div>" +
      '<div class="waf-set-sec" data-sec="rules"><div class="waf-sec-head">' + I("search") + '检测规则<span class="settings-hint" style="margin:0 0 0 4px">按类型启停内置规则组</span></div>' +
      '<div class="waf-rule-grid">' + ruleItems + "</div>" +
      '<div class="settings-row" style="margin-top:14px"><label style="align-items:flex-start;min-width:170px">自定义检测规则<div class="settings-hint" style="margin:0">对 URI/参数/UA/Referer/POST 体扫描,命中走全局拦截模式<br>每行一条:PCRE 正则 | 标签(可选),最多 100 条<br>示例:<code>\\.(bak|old|sql)$ | 备份文件探测</code></div></label>' +
      '<textarea class="textarea mono" id="waf-custom-rules" rows="5" style="flex:1;max-width:520px;font-size:12px">' +
      esc((st.custom_rules || []).map(function(c) { return c.pattern + " | " + (c.label || ""); }).join("\n")) + "</textarea></div></div>" +
      '<div class="waf-set-sec" data-sec="enhance"><div class="waf-sec-head">' + I("bolt") + "增强防护</div>" +
      '<div class="settings-row" style="margin-bottom:12px"><label style="align-items:flex-start;min-width:170px">Apache 防护(PHP 容器)<div class="settings-hint" style="margin:0">在 PHP 运行时容器内启用 ModSecurity:保护直连 Apache 的流量(IP/UA 黑白名单、CC、敏感文件、自定义规则)。面板站点流量默认先经 Nginx WAF(全量规则),此项为纵深防护,可选。</div></label>' +
      '<div style="display:flex;align-items:center;gap:10px"><span class="switch" id="sw-apache"></span><span class="settings-hint" id="apache-status" style="margin:0">检测中…</span></div></div>' +
      '<div class="waf-rule-grid">' +
      '<div class="waf-rule-item"><label><b>拦截空 UA 请求</b><span class="settings-hint">正规浏览器必有 User-Agent,空 UA 多为脚本工具</span></label><span class="switch' + (st.empty_ua_block ? " on" : "") + '" id="sw-emptyua"></span></div>' +
      '<div class="waf-rule-item"><label><b>拦截 HTTP/1.0 请求</b><span class="settings-hint">现代浏览器均用 HTTP/1.1+,1.0 多为攻击工具</span></label><span class="switch' + (st.http10_block ? " on" : "") + '" id="sw-http10"></span></div>' +
      "</div>" +
      '<div class="settings-row" style="margin-top:14px"><label style="align-items:flex-start;min-width:170px">蜜罐诱捕<div class="settings-hint" style="margin:0">访问伪装路径(假后台/备份文件)的 IP 立即封禁 24 小时并返回伪装 404<br>每行一条路径,须以 / 开头,最多 50 条,如 /admin.zip</div></label>' +
      '<div style="flex:1;max-width:520px"><label style="display:flex;align-items:center;gap:8px;margin-bottom:6px"><span class="switch' + (st.honeypot_enable ? " on" : "") + '" id="sw-honeypot"></span><span class="settings-hint" style="margin:0">启用蜜罐</span></label>' +
      '<textarea class="textarea mono" id="honeypot-paths" rows="3" style="width:100%;font-size:12px" placeholder="/admin.zip\n/.env_bak\n/backup.sql">' + esc((st.honeypot_paths || []).join("\n")) + "</textarea></div></div></div>" +
      '<div class="waf-set-sec" data-sec="autoban"><div class="waf-sec-head">' + I("stop") + '自动封禁<span class="settings-hint" style="margin:0 0 0 4px">24h 内违规达阈值的 IP 自动进持久黑名单,每日巡检,上限 500</span></div>' +
      '<div class="waf-params-row"><label style="display:flex;align-items:center;gap:8px"><span class="switch' + (st.auto_ban_enable ? " on" : "") + '" id="sw-autoban"></span><span>启用</span></label>' +
      '<span class="waf-param">阈值 <input class="input" id="autoban-threshold" type="number" value="' + (st.auto_ban_threshold || 50) + '"> 次 / 24h</span>' +
      '<button class="btn btn-sm btn-plain" id="autoban-clear">清空清单</button></div>' +
      '<div class="settings-hint" id="autoban-list" style="margin-top:10px">加载中…</div></div>' +
      '<div class="waf-set-sec" data-sec="ratelimit"><div class="waf-sec-head">' + I("pulse") + "限速防护</div>" +
      '<div class="settings-row"><label style="align-items:flex-start;min-width:170px">敏感路径限速<div class="settings-hint" style="margin:0">对指定 URL 前缀独立限频,防暴力破解登录/API<br>每行一条:路径 | 限制次数 | 窗口秒</div></label>' +
      '<textarea class="textarea mono" id="url-rules" rows="4" style="flex:1;max-width:520px;font-size:12px">' +
      esc((st.url_rate_rules || []).map(function(r) { return r.path + " | " + r.limit + " | " + (r.window || 60); }).join("\n")) + "</textarea></div>" +
      '<div class="waf-params-row" style="margin-top:14px"><label style="display:flex;align-items:center;gap:8px"><span class="switch' + (st.dirscan_enable ? " on" : "") + '" id="sw-dirscan"></span><span><b style="font-size:13px">目录扫描防御</b></span></label>' +
      '<span class="waf-param">窗口期访问 <input class="input" id="dirscan-threshold" type="number" value="' + (st.dirscan_threshold || 30) + '"> 个不同 URI</span>' +
      '<span class="waf-param">统计窗口 <input class="input" id="dirscan-window" type="number" value="' + (st.dirscan_window || 60) + '"> 秒</span></div>' +
      '<div class="settings-hint" style="margin-top:8px">超阈值自动封禁,防目录暴力枚举与扫描器</div></div>' +
      '<div class="waf-set-sec" data-sec="method"><div class="waf-sec-head">' + I("tool") + "方法管控</div>" +
      '<div class="settings-row"><label style="align-items:flex-start;min-width:170px">方法白名单<div class="settings-hint" style="margin:0">仅放行列表内方法,其余 405(比黑名单更安全);每行一个,大写</div></label>' +
      '<div style="flex:1;max-width:520px"><label style="display:flex;align-items:center;gap:8px;margin-bottom:6px"><span class="switch' + (st.method_whitelist_enable ? " on" : "") + '" id="sw-mwl"></span><span class="settings-hint" style="margin:0">启用白名单</span></label>' +
      '<textarea class="textarea mono" id="mwl-list" rows="3" style="width:100%;font-size:12px">' + esc((st.method_whitelist || ["GET","POST","HEAD","OPTIONS"]).join("\n")) + "</textarea></div></div>" +
      '<div class="settings-row" style="margin-top:14px"><label style="align-items:flex-start;min-width:170px">危险方法拦截<div class="settings-hint" style="margin:0">命中返回 405,防 TRACK/TRACE 探测;需要 PUT/DELETE 的 REST 接口自行增删</div></label>' +
      '<div style="flex:1;max-width:520px"><label style="display:flex;align-items:center;gap:8px;margin-bottom:6px"><span class="switch' + (st.method_block_enable ? " on" : "") + '" id="sw-method"></span><span class="settings-hint" style="margin:0">启用拦截</span></label>' +
      '<textarea class="textarea mono" id="method-list" rows="3" style="width:100%;font-size:12px">' + esc((st.method_block_list || []).join("\n")) + "</textarea></div></div></div>" +
      '<div class="waf-set-sec" data-sec="spider"><div class="waf-sec-head">' + I("globe") + '搜索引擎蜘蛛白名单<span class="settings-hint" style="margin:0 0 0 4px">名单内爬虫跳过全部检测(含 CC 验证/封禁),保护 SEO 收录</span></div>' +
      '<div class="settings-row"><label style="min-width:170px">启用蜘蛛白名单</label><label style="display:flex;align-items:center;gap:8px"><span class="switch' + (st.spider_white_enable ? " on" : "") + '" id="sw-spider"></span></label></div>' +
      '<div class="settings-row"><label style="align-items:flex-start;min-width:170px">UA 特征(正则)<div class="settings-hint" style="margin:0">每行一条,匹配 User-Agent 即放行</div></label>' +
      '<textarea class="textarea mono" id="spider-list" rows="4" style="flex:1;max-width:520px;font-size:12px">' + esc((st.spider_white || []).join("\n")) + "</textarea></div>" +
      '<div class="settings-row"><label style="min-width:170px">严格反向 DNS 验证<div class="settings-hint" style="margin:0">PTR 反查 + 正向确认匹配客户端 IP,防伪造 Googlebot 绕过;开启后蜘蛛首次访问略慢</div></label>' +
      '<label style="display:flex;align-items:center;gap:8px"><span class="switch' + (st.spider_verify_dns ? " on" : "") + '" id="sw-spiderdns"></span></label></div></div>' +
      '<div class="waf-save-bar"><span class="settings-hint" style="margin:0">修改后需点击保存,自动重载 Nginx 生效</span><div class="spacer"></div>' +
      '<button class="btn btn-primary btn-sm" id="waf-save-global">' + I("edit") + " 保存设置</button></div>" +
      "</div></div>";
    // 左侧导航:点击滚动定位 + 滚动高亮(元素移除后自清理)
    qsa(".waf-set-nav a", body).forEach(function(a) {
      a.onclick = function() {
        var t = qs('.waf-set-sec[data-sec="' + a.getAttribute("data-sec") + '"]', body);
        if (t) t.scrollIntoView({ behavior: "smooth", block: "start" });
      };
    });
    var secs = qsa(".waf-set-sec", body);
    var spy = function() {
      if (!body.isConnected) { window.removeEventListener("scroll", spy); return; }
      var cur = secs[0];
      secs.forEach(function(s) { if (s.getBoundingClientRect().top <= 130) cur = s; });
      qsa(".waf-set-nav a", body).forEach(function(a) {
        a.classList.toggle("active", !!cur && a.getAttribute("data-sec") === cur.getAttribute("data-sec"));
      });
    };
    window.addEventListener("scroll", spy, { passive: true });
    spy();
    // 通用开关:点击切换 on 类
    function bindSwitch(id, fn) {
      var el = qs(id, body);
      if (!el) return;
      el.onclick = function() { el.classList.toggle("on"); if (fn) fn(el.classList.contains("on")); };
    }
    bindSwitch("#sw-emptyua"); bindSwitch("#sw-http10"); bindSwitch("#sw-autoban");
    bindSwitch("#sw-dirscan"); bindSwitch("#sw-mwl"); bindSwitch("#sw-method");
    bindSwitch("#sw-spider"); bindSwitch("#sw-spiderdns");
    // Apache 防护(独立开关,立即生效,不随下方保存)
    (async function() {
      var sw = qs("#sw-apache", body), st = qs("#apache-status", body);
      if (!sw || !st) return;
      try {
        var d = await api("/waf/apache");
        if (!d.available) { sw.style.display = "none"; st.textContent = "未检测到 PHP 运行时容器"; return; }
        sw.classList.toggle("on", !!d.enabled);
        st.textContent = d.enabled ? "ModSecurity 已启用" : "未启用";
        sw.onclick = async function() {
          var on = !sw.classList.contains("on");
          sw.classList.toggle("on", on);
          st.textContent = on ? "启用中…" : "禁用中…";
          try {
            var r2 = await api("/waf/apache", { method: "POST", body: { enabled: on } });
            st.textContent = r2.enabled ? "ModSecurity 已启用" : "未启用";
            toast(on ? "Apache 防护已启用" : "Apache 防护已关闭", on ? "success" : "info");
          } catch (e2) {
            sw.classList.toggle("on", !on);
            st.textContent = on ? "未启用" : "ModSecurity 已启用";
            toast(e2.message, "error", 6000);
          }
        };
      } catch (e) { st.textContent = "状态获取失败"; }
    })();
    qsa("[data-mode]", body).forEach(function(c) {
      c.onclick = function() {
        qsa("[data-mode]", body).forEach(function(x) { x.classList.remove("active"); });
        c.classList.add("active");
        self.st.mode = c.getAttribute("data-mode");
      };
    });
    qsa("[data-grp]", body).forEach(function(sw) {
      sw.onclick = function() {
        sw.classList.toggle("on");
        self.st.rule_groups = self.st.rule_groups || {};
        self.st.rule_groups[sw.getAttribute("data-grp")] = sw.classList.contains("on");
      };
    });
    qs("#waf-enable", body).onchange = function() { self.st.enabled = this.checked; };
    // 自动封禁清单:渲染时即加载/绑定(此前误放在保存回调里,要点一次保存才生效)
    (async function() {
      try {
        var d = await api("/waf/auto-ban");
        var el2 = qs("#autoban-list", body);
        if (el2) el2.textContent = (d.list && d.list.length) ? d.list.slice(0, 20).join(", ") + (d.list.length > 20 ? " …共 " + d.list.length + " 个" : "") : "暂无封禁 IP";
      } catch (e) { var el3 = qs("#autoban-list", body); if (el3) el3.textContent = "加载失败"; }
    })();
    var abClear = qs("#autoban-clear", body);
    if (abClear) abClear.onclick = async function() {
      if (!(await confirmDlg("确定清空自动封禁清单吗?", { danger: true }))) return;
      try { await api("/waf/auto-ban/clear", { method: "POST", body: {} }); toast("已清空"); var el4 = qs("#autoban-list", body); if (el4) el4.textContent = "暂无封禁 IP"; }
      catch (e) { toast(e.message, "error"); }
    };
    qs("#waf-save-global", body).onclick = function() {
      self.st.rule_groups = self.st.rule_groups || {};
      qsa("[data-grp]", body).forEach(function(sw) {
        self.st.rule_groups[sw.getAttribute("data-grp")] = sw.classList.contains("on");
      });
      self.st.honeypot_enable = qs("#sw-honeypot", body).classList.contains("on");
      self.st.honeypot_paths = qs("#honeypot-paths", body).value.split("\n").map(function(x) { return x.trim(); }).filter(Boolean);
      self.st.spider_white_enable = qs("#sw-spider", body).classList.contains("on");
      self.st.spider_verify_dns = qs("#sw-spiderdns", body).classList.contains("on");
      self.st.spider_white = qs("#spider-list", body).value.split("\n").map(function(x) { return x.trim(); }).filter(Boolean);
      self.st.custom_rules = (qs("#waf-custom-rules", body).value || "").split("\n").map(function(line) {
        var seg = line.split("|");
        var pat = (seg[0] || "").trim();
        if (!pat) return null;
        return { pattern: pat, label: (seg[1] || "").trim() };
      }).filter(Boolean);
      self.st.empty_ua_block = qs("#sw-emptyua", body).classList.contains("on");
      self.st.http10_block = qs("#sw-http10", body).classList.contains("on");
      self.st.auto_ban_enable = qs("#sw-autoban", body).classList.contains("on");
      self.st.auto_ban_threshold = Number(qs("#autoban-threshold", body).value) || 50;
      self.st.url_rate_rules = (qs("#url-rules", body).value || "").split("\n").map(function(line) {
        var parts = line.split("|").map(function(x) { return x.trim(); });
        if (!parts[0]) return null;
        return { path: parts[0], limit: Number(parts[1]) || 10, window: Number(parts[2]) || 60 };
      }).filter(Boolean);
      self.st.dirscan_enable = qs("#sw-dirscan", body).classList.contains("on");
      self.st.dirscan_threshold = Number(qs("#dirscan-threshold", body).value) || 30;
      self.st.dirscan_window = Number(qs("#dirscan-window", body).value) || 60;
      self.st.method_whitelist_enable = qs("#sw-mwl", body).classList.contains("on");
      self.st.method_whitelist = (qs("#mwl-list", body).value || "").split("\n").map(function(x) { return x.trim().toUpperCase(); }).filter(Boolean);
      self.st.method_block_enable = qs("#sw-method", body).classList.contains("on");
      self.st.method_block_list = qs("#method-list", body).value.split("\n").map(function(x) { return x.trim(); }).filter(Boolean);
      self.save(body);
    };
  },
  /* ---- CC 防御 ---- */
  tabCC(body) {
    var self = this;
    var st = this.st;
    var ccModes = [
      { k: "rate", name: "限流拦截", desc: "不做人机验证;同一 IP 超过阈值直接返回 403,最简单直接" },
      { k: "jump", name: "5 秒跳转", desc: "首次访问展示 5 秒等待页,通过后免验放行;真人友好,机器无法通过" },
      { k: "js", name: "JS 无感验证", desc: "首次访问浏览器自动完成验证,真人无感知,脚本工具无法通过" },
      { k: "cf", name: "Cloudflare 风格", desc: "首次访问全屏 Verifying 动画页后无感放行,观感最接近 CDN 厂商,对脚本工具极不友好" },
      { k: "click", name: "点击验证", desc: "首次访问需点击确认按钮后继续访问" },
      { k: "captcha", name: "算术验证码", desc: "首次访问需输入算式答案,防护强度最高" },
      { k: "slider", name: "滑块验证", desc: "首次访问按住滑块拖到最右侧完成验证,真人体验好,脚本难以模拟拖动轨迹" },
    ];
    var modeCards = ccModes.map(function(mo) {
      var on = (st.cc_mode || "rate") === mo.k;
      return '<div class="waf-mode-card ' + (on ? "active" : "") + '" data-ccmode="' + mo.k + '"><b>' + mo.name + (mo.k === "rate" ? "(默认)" : "") + "</b><div>" + mo.desc + "</div></div>";
    }).join("");
    body.innerHTML =
      '<div class="waf-set-body">' +
      '<div class="waf-set-sec"><div class="waf-sec-head">' + I("pulse") + 'CC 攻击防御<span class="settings-hint" style="margin:0 0 0 4px">限流模式超阈值即拒绝;人机验证模式选完即全站启用,首次访问先验证</span></div>' +
      '<div class="waf-params-row">' +
      '<label style="display:flex;align-items:center;gap:8px"><span class="switch' + (st.cc_enable ? " on" : "") + '" id="sw-cc"></span><span>启用</span></label>' +
      '<span class="waf-param">统计窗口 <input class="input" id="cc-interval" type="number" value="' + (st.cc_interval || 60) + '"> 秒</span>' +
      '<span class="waf-param">请求阈值 <input class="input" id="cc-limit" type="number" value="' + (st.cc_limit || 120) + '"> 次<span class="settings-hint" style="margin:0">(限流模式=触发上限;验证模式=已验证访客的频率上限,建议 60-600)</span></span>' +
      "</div></div>" +
      '<div class="waf-set-sec"><div class="waf-sec-head">' + I("shield2") + '触发后的验证方式<span class="settings-hint" style="margin:0 0 0 4px">选择人机验证模式后立即生效:首次访问先验证,通过后免验;真人可完成、脚本工具被拦</span></div>' +
      '<div class="waf-mode-grid cc-grid">' + modeCards + "</div>" +
      '<div class="waf-params-row" style="margin-top:14px"><span class="waf-param"><b>验证有效期</b> <input class="input" id="cc-ttl" type="number" value="' + Math.round((st.cc_clear_ttl || 3600) / 60) + '"> 分钟<span class="settings-hint" style="margin:0">(通过验证后该 IP 在此期限内免验证)</span></span></div></div>' +
      '<div class="waf-set-sec"><div class="waf-sec-head">' + I("stop") + 'IP 临时封禁<span class="settings-hint" style="margin:0 0 0 4px">触发 CC 或多次命中规则后临时封禁该 IP</span></div>' +
      '<div class="waf-params-row">' +
      '<label style="display:flex;align-items:center;gap:8px"><span class="switch' + (st.ip_ban_enable ? " on" : "") + '" id="sw-ban"></span><span>启用</span></label>' +
      '<span class="waf-param">触发 <input class="input" id="ban-thres" type="number" value="' + (st.ip_ban_threshold || 10) + '"> 次</span>' +
      '<span class="waf-param">封禁 <input class="input" id="ban-dur" type="number" value="' + Math.round((st.ip_ban_duration || 600) / 60) + '"> 分钟</span>' +
      "</div></div>" +
      '<div class="waf-save-bar"><span class="settings-hint" style="margin:0">修改后需点击保存,自动重载 Nginx 生效</span><div class="spacer"></div>' +
      '<button class="btn btn-primary btn-sm" id="waf-save-cc">' + I("edit") + " 保存设置</button></div></div>";
    qsa("#sw-cc, #sw-ban", body).forEach(function(sw) {
      sw.onclick = function() { sw.classList.toggle("on"); };
    });
    qsa("[data-ccmode]", body).forEach(function(c) {
      c.onclick = function() {
        qsa("[data-ccmode]", body).forEach(function(x) { x.classList.remove("active"); });
        c.classList.add("active");
        self.st.cc_mode = c.getAttribute("data-ccmode");
      };
    });
    qs("#waf-save-cc", body).onclick = function() {
      self.st.cc_enable = qs("#sw-cc", body).classList.contains("on");
      self.st.cc_interval = parseInt(qs("#cc-interval", body).value) || 60;
      self.st.cc_limit = parseInt(qs("#cc-limit", body).value) || 120;
      self.st.cc_mode = self.st.cc_mode || "rate";
      self.st.cc_clear_ttl = (parseInt(qs("#cc-ttl", body).value) || 60) * 60;
      self.st.ip_ban_enable = qs("#sw-ban", body).classList.contains("on");
      self.st.ip_ban_threshold = parseInt(qs("#ban-thres", body).value) || 10;
      self.st.ip_ban_duration = (parseInt(qs("#ban-dur", body).value) || 10) * 60;
      self.save(body);
    };
  },
  /* ---- 黑白名单 ---- */
  tabLists(body) {
    var self = this;
    var st = this.st;
    function card(id, icon, tone, title, hint, ph, val) {
      var n = (val || []).length;
      return '<div class="waf-list-card ' + tone + '"><div class="waf-lc-head">' + I(icon) + "<b>" + title + '</b><span class="waf-lc-badge">' + n + " 条</span></div>" +
        '<div class="settings-hint">' + hint + "</div>" +
        '<textarea class="textarea mono" id="' + id + '" rows="7" placeholder="' + ph + '">' + esc((val || []).join("\n")) + "</textarea></div>";
    }
    body.innerHTML = '<div class="waf-set-body"><div class="waf-lists-grid" style="gap:14px">' +
      card("waf-ipw", "shield", "safe", "IP 白名单", "这些 IP 完全跳过检测;每行一条,支持单 IP、正则、CIDR 网段", "每行一条,支持正则,如 10\\.0\\.0\\.1", st.ip_white) +
      card("waf-iph", "stop", "danger", "IP 黑名单", "命中直接 403;支持单 IP、正则、CIDR 网段(如 103.0.0.0/8)", "每行一条:支持单 IP、正则、CIDR 网段(如 103.0.0.0/8)", st.ip_black) +
      card("waf-urlw", "globe", "safe", "URL 白名单", "这些 URI 跳过检测(如支付回调地址);每行一条正则", "每行一条正则,如 ^/api/pay/notify", st.url_white) +
      card("waf-uab", "eye", "danger", "UA 黑名单", "命中 User-Agent 直接 403;每行一条正则", "每行一条正则,如 (curl|python-requests|sqlmap)", st.ua_black) +
      "</div>" +
      '<div class="waf-save-bar"><span class="settings-hint" style="margin:0">每行一条,支持正则;保存前自动校验合法性,保存后自动重载 Nginx 生效</span><div class="spacer"></div>' +
      '<button class="btn btn-primary btn-sm" id="waf-save-lists">' + I("edit") + " 保存名单</button></div></div>";
    qs("#waf-save-lists", body).onclick = function() {
      var split = function(id) { return qs(id, body).value.split("\n").map(function(x) { return x.trim(); }).filter(Boolean); };
      self.st.ip_white = split("#waf-ipw");
      self.st.ip_black = split("#waf-iph");
      self.st.url_white = split("#waf-urlw");
      self.st.ua_black = split("#waf-uab");
      self.save(body);
    };
  },
  /* ---- 拦截日志 ---- */
  TAG_CLASS: { sql: "tag-sql", xss: "tag-xss", lfi: "tag-lfi", cmd: "tag-cmd", scan: "tag-scan", sensitive: "tag-sensitive", webshell: "tag-webshell", custom: "tag-custom", cc: "tag-cc", dirscan: "tag-dirscan", empty_ua: "tag-empty_ua", http10: "tag-http10", method: "tag-method", method_wl: "tag-method", upload_block: "tag-upload", url_rate: "tag-upload", ip_ban: "tag-dirscan", smuggle: "tag-cmd", honeypot: "tag-webshell", encoding: "tag-lfi", slider: "tag-custom" },
  tabLogs(body) {
    var self = this;
    var all = [];
    var m = '<div class="card-title" style="padding:0;margin-bottom:10px">拦截日志<span class="settings-hint" id="waf-log-count" style="margin:0 0 0 6px"></span></div>' +
      '<div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:12px">' +
      '<select class="select" id="waf-log-type" style="max-width:150px"><option value="">全部类型</option></select>' +
      '<input class="input" id="waf-log-ip" placeholder="按 IP 搜索" style="max-width:170px">' +
      '<div class="spacer" style="flex:1"></div>' +
      '<button class="btn btn-sm btn-plain" id="waf-log-refresh">' + I("refresh") + " 刷新</button>" +
      '<button class="btn btn-sm btn-danger-plain" id="waf-log-clear">' + I("trash") + " 清空日志</button></div>" +
      '<div id="waf-log-table">' + skeletonTable(8, 5) + "</div>";
    body.innerHTML = m;
    var load = async function() {
      var box = qs("#waf-log-table", body);
      if (!box) return;
      var list;
      try { list = (await api("/waf/logs?n=100")) || []; }
      catch (e) { box.innerHTML = emptyState("warn", "加载失败", e.message); return; }
      if (!box.isConnected) return;
      all = list;
      // 类型下拉去重填充(保留已选)
      var sel = qs("#waf-log-type", body);
      if (sel && sel.options.length <= 1) {
        var seen = {};
        list.forEach(function(e) { var lb = e.label || e.rule; if (lb && !seen[lb]) { seen[lb] = 1; sel.innerHTML += '<option value="' + esc(lb) + '">' + esc(lb) + "</option>"; } });
      }
      draw();
    };
    var draw = function() {
      var box = qs("#waf-log-table", body);
      if (!box) return;
      var tf = (qs("#waf-log-type", body) || {}).value || "";
      var ipq = ((qs("#waf-log-ip", body) || {}).value || "").trim();
      var list = all.filter(function(e) {
        if (tf && (e.label || e.rule) !== tf) return false;
        if (ipq && (e.ip || "").indexOf(ipq) < 0) return false;
        return true;
      });
      var cnt = qs("#waf-log-count", body);
      if (cnt) cnt.textContent = all.length ? "共 " + all.length + " 条" + (list.length !== all.length ? ",筛选出 " + list.length + " 条" : "") : "";
      if (!all.length) { box.innerHTML = emptyState("shield", "暂无拦截记录", "攻击发生时会实时记录在这里"); return; }
      if (!list.length) { box.innerHTML = emptyState("search", "无匹配记录", "调整类型或 IP 关键字试试"); return; }
      box.innerHTML = '<div class="table-wrap"><table class="table waf-log-table"><thead><tr><th style="width:170px">时间</th><th style="width:150px">IP</th><th style="width:150px">类型</th><th style="width:40%">URI</th><th>详情</th><th style="width:90px">操作</th></tr></thead><tbody>' +
        list.map(function(e) {
          var tc = self.TAG_CLASS[e.rule] || "tag-danger";
          return '<tr><td class="waf-nowrap">' + esc(e.time) + '</td><td class="mono waf-nowrap">' + esc(e.ip) + '</td><td><span class="tag ' + tc + '">' + esc(e.label || e.rule) + "</span></td>" +
            '<td class="mono fm-ellipsis" style="max-width:260px" title="' + esc(e.method + " " + e.uri) + '">' + esc(e.method + " " + e.uri) + '</td>' +
            '<td class="fm-ellipsis" style="max-width:200px" title="' + esc(e.detail || "") + '">' + esc(e.detail || "-") + '</td>' +
            '<td style="width:90px"><button class="btn btn-xs btn-danger-plain" data-waf-ban="' + esc(e.ip) + '">封禁</button></td></tr>';
        }).join("") + "</tbody></table></div>";
      qsa("[data-waf-ban]", box).forEach(function(b) {
        b.onclick = async function() {
          var ip = b.getAttribute("data-waf-ban");
          if (!(await confirmDlg("将 IP <b class=mono>" + esc(ip) + "</b> 加入 WAF IP 黑名单?<br><span style='font-size:12px;color:var(--text-3)'>加入后该 IP 的所有请求直接 403;若误封自己的出口 IP 将无法访问面板(可在黑名单中移除)。</span>", { danger: true }))) return;
          try {
            var st2 = (await api("/waf")).settings;
            var bl = st2.ip_black || [];
            if (bl.indexOf(ip) >= 0) { toast("该 IP 已在黑名单中"); return; }
            bl.push(ip);
            st2.ip_black = bl;
            await api("/waf/settings", { method: "POST", body: st2 });
            toast("IP " + ip + " 已加入黑名单");
          } catch (e) { toast(e.message, "error"); }
        };
      });
    };
    load();
    var typeSel = qs("#waf-log-type", body), ipInput = qs("#waf-log-ip", body);
    if (typeSel) typeSel.onchange = draw;
    if (ipInput) ipInput.oninput = draw;
    qs("#waf-log-refresh", body).onclick = load;
    qs("#waf-log-clear", body).onclick = async function() {
      if (!(await confirmDlg("确定清空全部拦截日志吗?", { danger: true }))) return;
      try { await api("/waf/logs/clear", { method: "POST", body: {} }); toast("日志已清空"); load(); }
      catch (e) { toast(e.message, "error"); }
    };
  },
  async save(body) {
    try {
      await api("/waf/settings", { method: "POST", body: this.st });
      toast("WAF 设置已保存并生效");
    } catch (e) { toast(e.message, "error", 5000); }
  },
};


/* ================= 系统服务 ================= */

/* ================= 系统服务 ================= */
PAGES.services = {
  title: "系统服务",
  keyword: "",
  async render(el) {
    el.innerHTML = '<div class="card"><div class="alert alert-warning page-fade"><span class="alert-ico">' + I("warn") + '</span>服务操作会直接影响系统运行,请谨慎操作。</div><div id="svc-body">' + skeletonTable(6, 4) + "</div></div>";
    var body = qs("#svc-body", el);
    var self = this;
    var data = {};
    try { data = await api("/services"); }
    catch (e) { body.innerHTML = emptyState("warn", "加载失败", e.message); return; }

    if (!data.available) {
      body.innerHTML = emptyState("info", "当前系统不支持 systemd 服务管理");
      return;
    }
    var list = data.services || [];

    var draw = function() {
      var rows = list;
      var kw = (self.keyword || "").toLowerCase();
      if (kw) {
        rows = list.filter(function(s) { return s.name.toLowerCase().indexOf(kw) >= 0 || (s.desc || "").toLowerCase().indexOf(kw) >= 0; });
      }
      var html = '<div class="svc-toolbar"><div class="svc-search">' + I("search") +
        '<input id="svc-search" placeholder="搜索服务..." value="' + esc(self.keyword || "") + '"></div>' +
        '<span class="tag tag-info">共 ' + rows.length + ' 个</span></div>';
      if (!rows.length) {
        html += '<div class="help" style="padding:20px;text-align:center">未找到匹配的服务</div>';
      } else {
        html += '<div class="table-wrap"><table class="table svc-table"><thead><tr>' +
          '<th>服务名</th><th>状态</th><th>自启</th><th>描述</th><th>操作</th></tr></thead><tbody>';
        rows.forEach(function(r) {
          var statusTag2 = r.active === "active"
            ? '<span class="tag tag-success"><span class="dot on"></span>运行中</span>'
            : r.active === "failed"
            ? '<span class="tag tag-danger">失败</span>'
            : '<span class="tag tag-info">已停止</span>';
          var autoTag = r.enabled === "enabled"
            ? '<span class="tag tag-success">自启</span>'
            : '<span class="tag tag-info">手动</span>';
          html += '<tr><td><b>' + esc(r.name) + '</b></td><td>' + statusTag2 + '</td><td>' + autoTag + '</td><td class="svc-desc">' + esc(r.desc || "-") + '</td><td><div class="svc-ops">' +
            '<button class="btn btn-xs btn-plain" data-sact="start" data-s="' + esc(r.name) + '">启动</button>' +
            '<button class="btn btn-xs btn-plain" data-sact="stop" data-s="' + esc(r.name) + '">停止</button>' +
            '<button class="btn btn-xs btn-plain" data-sact="restart" data-s="' + esc(r.name) + '">重启</button>' +
            (r.enabled === "enabled"
              ? '<button class="btn btn-xs btn-plain" data-sact="disable" data-s="' + esc(r.name) + '">禁自启</button>'
              : '<button class="btn btn-xs btn-plain" data-sact="enable" data-s="' + esc(r.name) + '">设自启</button>') +
            '</div></td></tr>';
        });
        html += '</tbody></table></div>';
      }
      body.innerHTML = html;
      var searchInput = document.getElementById("svc-search");
      if (searchInput) {
        searchInput.oninput = function(e) { self.keyword = e.target.value; draw(); };
        if (self.keyword) searchInput.value = self.keyword;
      }
      body.querySelectorAll("[data-sact]").forEach(function(b) {
        b.onclick = function() {
          var name = b.getAttribute("data-s"), act = b.getAttribute("data-sact");
          var cn = {start:"启动",stop:"停止",restart:"重启",enable:"设为开机自启",disable:"禁用开机自启"}[act];
          confirmDlg("确定" + cn + "服务 <b>" + esc(name) + "</b> 吗?", {danger: act === "stop"}).then(function(ok) {
            if (!ok) return;
            fetch("/api/v1/services/" + encodeURIComponent(name) + "/" + act, {method:"POST", headers:{"Authorization":"Bearer "+localStorage.getItem(TOKEN_KEY),"Content-Type":"application/json"}, body:"{}"})
              .then(function(r){return r.json()})
              .then(function(d) {
                if (d.code === 0) { toast(cn + "成功"); setTimeout(function(){ self.render(el); }, 500); }
                else toast(d.message || "操作失败", "error");
              });
          });
        };
      });
    };
    draw();
  },
};



/* ================= 日志 ================= */
PAGES.logs = {
  title: "日志",
  tab: "login",
  async render(el) {
    var self = this;
    el.innerHTML = '<div class="card"><div class="card-title"><span>日志</span><span class="title-actions"><button class="btn btn-sm btn-plain" id="log-export">' + I("download") + ' 导出 CSV</button><button class="btn btn-sm btn-danger-plain" id="log-clear">' + I("trash") + ' 清空当前日志</button></span></div>' +
      '<div class="tabs"><div class="tab ' + (this.tab === "login" ? "active" : "") + '" data-tab="login">登录日志</div><div class="tab ' + (this.tab === "operation" ? "active" : "") + '" data-tab="operation">操作日志</div></div><div id="log-table">' + skeletonTable(5, 4) + "</div></div>";
    qsa(".tab", el).forEach((t) => (t.onclick = () => { this.tab = t.dataset.tab; this.render(el); }));
    qs("#log-export", el).onclick = function() {
      fetch("/api/v1/logs/" + self.tab + "/export", { headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY) } })
        .then(function(r) { return r.blob().then(function(b) { return { ok: r.ok, blob: b }; }); })
        .then(function(d) {
          if (!d.ok) { toast("导出失败", "error"); return; }
          var a = document.createElement("a");
          a.href = URL.createObjectURL(d.blob);
          a.download = self.tab + "_log.csv";
          a.click();
          setTimeout(function() { URL.revokeObjectURL(a.href); }, 5000);
        });
    };
    qs("#log-clear", el).onclick = async function() {
      var kind = self.tab === "operation" ? "operation" : "login";
      var name = kind === "login" ? "登录日志" : "操作日志";
      if (!(await confirmDlg("确定清空全部" + name + "吗?清空后不可恢复。"))) return;
      try {
        await api("/logs/" + kind + "/clear", { method: "POST", body: {} });
        toast("日志已清空");
        var tbl = qs("#log-table", el);
        if (tbl) tbl.innerHTML = emptyState("logs", "暂无日志");
      } catch (e) { toast("清空失败: " + e.message, "error"); }
    };
    let list = [];
    try { list = (await api("/logs/" + this.tab)) || []; }
    catch (e) { qs("#log-table", el).innerHTML = emptyState("warn", "加载失败", e.message); return; }
    qs("#log-table", el).innerHTML = tableHTML([
      { title: "时间", width: "170px", render: (r) => esc(r.time) },
      { title: "IP", width: "150px", render: (r) => '<span class="mono">' + esc(r.ip) + "</span>" },
      { title: this.tab === "login" ? "动作" : "操作", render: (r) => esc(r.action) },
      { title: "详情", render: (r) => esc(r.detail || "-") },
      { title: "结果", width: "90px", render: (r) => r.ok ? '<span class="tag tag-success">成功</span>' : '<span class="tag tag-danger">失败</span>' },
    ], list, "暂无日志", "logs");
  },
};

/* ================= 设置 ================= */

/* ================= SSH 安全(独立页,由安全中心承载) ================= */
PAGES.ssh = {
  title: "SSH 安全",
  render(el) {
    var self = this;
    el.innerHTML = '<div class="settings-section"><div class="sk" style="height:120px"></div></div>';
    api("/ssh").then(function(st) {
      var rootOpt = function(v, label) {
        return '<option value="' + v + '"' + (st.permit_root === v ? " selected" : "") + ">" + label + "</option>";
      };
      var html = '<div class="settings-section">' +
        '<div class="settings-section-title">SSH 服务状态</div>' +
        '<div class="settings-row"><label>服务</label><span>' + esc(st.service) + '</span><span class="tag tag-success"><span class="dot on"></span>运行中</span></div>' +
        '<div class="settings-row"><label>当前端口</label><b>' + st.port + '</b><span class="settings-hint">公钥数量: ' + (st.keys || []).length + ' 条</span></div>' +
        '</div>' +
        '<div class="settings-section"><div class="settings-section-title">修改 SSH 端口</div>' +
        '<div class="settings-row"><label>新端口</label><input class="input" id="ssh-port" type="number" value="' + st.port + '" style="max-width:160px">' +
        '<button class="btn btn-primary btn-sm" id="ssh-port-btn">修改端口</button></div>' +
        '<div class="settings-hint">自动放行新端口(如防火墙已启用);修改后请用新端口重连。修改前请记牢当前端口。</div></div>' +
        '<div class="settings-section"><div class="settings-section-title">登录加固</div>' +
        '<div class="settings-row"><label>Root 登录</label><select class="select" id="ssh-root" style="max-width:260px">' +
        rootOpt("yes", "允许 Root 直接登录(不安全)") +
        rootOpt("prohibit-password", "仅允许密钥登录 Root(推荐)") +
        rootOpt("no", "禁止 Root 登录") +
        '</select></div>' +
        '<div class="settings-row"><label>密码登录</label><select class="select" id="ssh-pwdauth" style="max-width:260px">' +
        '<option value="yes"' + (st.password_auth !== "no" ? " selected" : "") + ">允许密码登录</option>" +
        '<option value="no"' + (st.password_auth === "no" ? " selected" : "") + ">仅允许密钥登录(最安全)</option>" +
        '</select></div>' +
        '<div class="settings-row"><label></label><button class="btn btn-primary btn-sm" id="ssh-harden-btn">应用加固</button></div>' +
        '<div class="settings-hint">关闭密码登录前请先确认下方已添加公钥并能用密钥登录,否则会把自己锁在服务器外。</div></div>' +
        '<div class="settings-section"><div class="settings-section-title">公钥管理(/root/.ssh/authorized_keys)</div>' +
        '<div class="settings-row"><label>添加公钥</label><textarea class="textarea" id="ssh-key-input" rows="2" placeholder="ssh-ed25519 AAAA... user@host" style="flex:1"></textarea></div>' +
        '<div class="settings-row"><label></label><button class="btn btn-primary btn-sm" id="ssh-key-add">添加公钥</button></div>' +
        '<div id="ssh-keys">' + self.keyTable(st.keys || []) + "</div></div>";
      el.innerHTML = html;

      qs("#ssh-port-btn", el).onclick = async function() {
        var port = Number(qs("#ssh-port", el).value);
        if (!(await confirmDlg("确定把 SSH 端口改为 <b>" + port + "</b> 吗?<br><span style='font-size:12px;color:var(--text-3)'>防火墙将自动放行新端口;请确保新端口未被占用,改完用新端口重连。</span>", { danger: true }))) return;
        try {
          await api("/ssh/port", { method: "POST", body: { port: port } });
          toast("SSH 端口已改为 " + port + ",现有连接不受影响,新连接请用新端口");
          self.render(el);
        } catch (e) { toast(e.message, "error", 5000); }
      };
      qs("#ssh-harden-btn", el).onclick = async function() {
        var rootV = qs("#ssh-root", el).value, pwdV = qs("#ssh-pwdauth", el).value;
        if (!(await confirmDlg("确定应用登录加固设置吗?<br>Root: <b>" + esc(rootV) + "</b> · 密码登录: <b>" + (pwdV === "no" ? "关闭" : "开启") + "</b>", { danger: pwdV === "no" }))) return;
        try {
          await api("/ssh/hardening", { method: "POST", body: { permit_root: rootV, password_auth: pwdV } });
          toast("加固设置已生效");
          self.render(el);
        } catch (e) { toast(e.message, "error", 5000); }
      };
      qs("#ssh-key-add", el).onclick = async function() {
        var key = qs("#ssh-key-input", el).value.trim();
        if (!key) { toast("请粘贴公钥内容", "error"); return; }
        try {
          await api("/ssh/keys", { method: "POST", body: { key: key } });
          toast("公钥已添加");
          qs("#ssh-key-input", el).value = "";
          self.render(el);
        } catch (e) { toast(e.message, "error"); }
      };
      qsa("[data-key-del]", el).forEach(function(b) {
        b.onclick = async function() {
          if (!(await confirmDlg("确定删除第 <b>" + b.getAttribute("data-key-del") + "</b> 条公钥吗?", { danger: true }))) return;
          try {
            await api("/ssh/keys/" + b.getAttribute("data-key-del"), { method: "DELETE" });
            toast("公钥已删除");
            self.render(el);
          } catch (e) { toast(e.message, "error"); }
        };
      });
    }).catch(function(e) {
      el.innerHTML = emptyState("warn", "无法读取 SSH 配置", e.message);
    });
  },
  keyTable(keys) {
    if (!keys.length) return '<div class="help" style="padding:10px 0">还没有公钥。关闭密码登录前必须先添加。</div>';
    return '<div class="table-wrap"><table class="table"><thead><tr><th style="width:50px">#</th><th>公钥</th><th style="width:80px">操作</th></tr></thead><tbody>' +
      keys.map(function(k, i) {
        var parts = k.split(" ");
        var brief = esc(parts[0] || "") + " " + (parts[1] ? esc(parts[1].slice(0, 24)) + "..." : "") + (parts[2] ? " (" + esc(parts[2]) + ")" : "");
        return '<tr><td>' + (i + 1) + '</td><td class="mono fm-ellipsis" title="' + esc(k) + '">' + brief + '</td>' +
          '<td><button class="btn btn-xs btn-danger-plain" data-key-del="' + (i + 1) + '">删除</button></td></tr>';
      }).join("") + "</tbody></table></div>";
  },
};

PAGES.settings = {
  title: "面板设置",
  tab: "general",
  async render(el) {
    el.innerHTML = '<div class="card"><div class="card-title">面板设置</div>' + skeletonTable(4, 3, 200) + '</div>';
    var self = this;
    var s = {};
    var alertCfg = { enabled: false, webhook: "", cpu_thres: 90, mem_thres: 90, duration: 120 };
    try {
      var results = await Promise.all([api("/settings"), api("/alert").catch(function(){ return {enabled:false, webhook:"", cpu_thres:90, mem_thres:90, duration:120} })]);
      s = results[0];
      alertCfg = Object.assign(alertCfg, results[1]);
    } catch (e) { el.innerHTML = emptyState("warn", "加载失败", e.message); return; }

    var tabs = [
      { key: "general", label: "常规设置", icon: "gear" },
      { key: "security", label: "安全", icon: "shield" },
      { key: "alert", label: "告警通知", icon: "bolt" },
      { key: "backup", label: "备份与清理", icon: "archive" },
      { key: "about", label: "关于", icon: "info" }
    ];

    var navHTML = '<div class="set-layout">' +
      '<div class="card set-nav">' +
      tabs.map(function(t) {
        return '<div class="set-nav-item' + (self.tab === t.key ? " active" : "") + '" data-tab="' + t.key + '">' + I(t.icon) + "<span>" + t.label + "</span></div>";
      }).join("") +
      "</div>" +
      '<div class="set-content" id="settings-body">' + skeletonTable(4, 3, 200) + "</div></div>";
    el.innerHTML = navHTML;

    var body = qs("#settings-body", el);

    qsa(".set-nav-item", el).forEach(function(n) {
      n.onclick = function() {
        qsa(".set-nav-item", el).forEach(function(x) { x.classList.toggle("active", x === n); });
        self.tab = n.getAttribute("data-tab");
        var fns = { general: renderGeneral, security: renderSecurity, alert: renderAlert, backup: renderBackup, about: renderAbout };
        (fns[self.tab] || renderGeneral)();
      };
    });

    function renderGeneral() {
      var isDark = document.documentElement.getAttribute("data-theme") === "dark";
      body.innerHTML =
        '<div class="settings-section">' +
        '<div class="settings-section-title">基础设置</div>' +
        '<div class="settings-row"><label>面板名称</label><input class="input" id="s-name" value="' + esc(s.panel_name) + '"></div>' +
        '<div class="settings-row"><label>监听端口</label><input class="input" id="s-port" type="number" value="' + s.port + '"><span class="settings-hint">修改后需重启面板生效</span></div>' +
        '<div class="settings-row"><label>登录用户名</label><input class="input" id="s-user" value="' + esc(s.username) + '"></div>' +
        '<div class="settings-row"><label>安全入口</label><div style="display:flex;gap:8px;flex:1;max-width:520px">' +
        '<input class="input" id="s-entrance" style="flex:1" value="' + (s.entrance && s.entrance !== "-" ? esc(s.entrance) : "") + '" placeholder="' + (s.entrance === "-" ? "已关闭" : "") + '">' +
        '<button class="btn btn-plain btn-sm" id="s-entrance-rand">随机</button></div>' +
        '<div class="settings-hint">当前后台: http://IP:' + s.port + '/' + (s.entrance && s.entrance !== "-" ? esc(s.entrance) : "(未设置)") + ' · 清空=关闭</div></div>' +
        '<div class="settings-row"><label>网站默认根目录</label><input class="input" id="s-webroot" style="max-width:520px" value="' + esc(s.web_root || "") + '" placeholder="默认: /opt/kunpanel/data/websites/roots">' +
        '<div class="settings-hint">新建站点的根目录所在位置(留空 = 面板默认);已有站点不受影响,站点创建时网关容器会自动挂载该目录</div></div>' +
        '</div>' +
        '<div class="settings-section"><div class="settings-section-title">外观</div>' +
        '<div class="settings-row"><label>主题</label><div class="set-theme-cards">' +
        '<div class="set-theme-card' + (!isDark ? " on" : "") + '" data-set-theme="light">' +
        '<div class="th-prev"><i class="tp-bar"></i><i class="tp-side"></i><i class="tp-block"></i></div><span>浅色</span></div>' +
        '<div class="set-theme-card' + (isDark ? " on" : "") + '" data-set-theme="dark">' +
        '<div class="th-prev dark"><i class="tp-bar"></i><i class="tp-side"></i><i class="tp-block"></i></div><span>深色</span></div>' +
        "</div></div></div>" +
        '<div class="settings-section"><div class="settings-section-title">应用更改</div>' +
        '<div style="display:flex;gap:12px;align-items:center;flex-wrap:wrap">' +
        '<button class="btn btn-primary" id="s-save">保存设置</button>' +
        '<span class="settings-hint" style="margin:0">监听端口修改后需重启面板生效,其余即时生效</span></div></div>';

      body.querySelector("#s-entrance-rand").onclick = function() {
        var cs = "abcdefghjkmnpqrstuvwxyz23456789";
        var arr = new Uint8Array(10); crypto.getRandomValues(arr);
        var str = ""; for (var i = 0; i < 10; i++) str += cs[arr[i] % cs.length];
        body.querySelector("#s-entrance").value = str;
      };
      body.querySelectorAll("[data-set-theme]").forEach(function(b) {
        b.onclick = function() {
          document.documentElement.setAttribute("data-theme", b.getAttribute("data-set-theme"));
          localStorage.setItem("kp_theme", b.getAttribute("data-set-theme"));
          body.querySelectorAll(".set-theme-card").forEach(function(x) { x.classList.remove("on"); });
          b.classList.add("on");
        };
      });
      body.querySelector("#s-save").onclick = async function() {
        try {
          var r = await api("/settings", { method: "POST", body: {
            panel_name: body.querySelector("#s-name").value.trim(),
            port: parseInt(body.querySelector("#s-port").value) || 0,
            username: body.querySelector("#s-user").value.trim(),
            entrance: body.querySelector("#s-entrance").value.trim(),
            web_root: body.querySelector("#s-webroot").value.trim()
          }});
          toast(r.message || "保存成功");
        } catch (e) { toast(e.message, "error"); }
      };
    }

    function renderSecurity() {
      var totpOn = s.totp_enabled;
      var sslState = { enabled: !!s.ssl, hasCert: !!s.ssl_has_cert };
      if (!s.ssl_has_cert) {
        sslState.enabled = false;
      }
      window.__psSSL = sslState;
      body.innerHTML =
        '<div class="settings-section"><div class="settings-section-title">安全自检</div>' +
        '<div style="display:flex;gap:12px;align-items:center;flex-wrap:wrap">' +
        '<button class="btn btn-primary btn-sm" id="sec-checkup">' + I("pulse") + ' 一键安全自检</button>' +
        '<span class="help">检查磁盘 / 内存 / 负载 / 证书 / 应用 / 安全配置 / 计划任务,生成体检报告</span></div></div>' +
        '<div class="settings-section">' +
        '<div class="settings-section-title">两步验证 (2FA)</div>' +
        '<div class="settings-row"><label>状态</label><span class="tag ' + (totpOn ? 'tag-success">已启用' : 'tag-info">未启用') + '</span></div>' +
        '<div id="totp-setup-box" style="display:' + (totpOn ? 'none' : 'block') + '">' +
        '<button class="btn btn-primary btn-sm" id="totp-gen">生成密钥</button>' +
        '<div id="totp-secret-box" style="display:none;margin-top:12px">' +
        '<div class="settings-row"><label>密钥(base32)</label><input class="input mono" id="totp-secret" readonly style="font-family:monospace"></div>' +
        '<div class="settings-row"><label>动态码确认</label><input class="input" id="totp-code" placeholder="6 位动态码" maxlength="6" style="width:160px"></div>' +
        '<button class="btn btn-primary btn-sm" id="totp-enable">确认启用</button></div></div>' +
        '<div id="totp-on-box" style="display:' + (totpOn ? 'block' : 'none') + '">' +
        '<button class="btn btn-danger-plain btn-sm" id="totp-disable">关闭两步验证</button>' +
        '<div class="settings-hint">关闭需要验证登录密码</div></div>' +
        '<div class="settings-hint" style="margin-top:10px">启用后登录需要 密码 + 验证器动态码(Google Authenticator 等兼容)</div>' +
        '</div>' +
        '<div class="settings-section"><div class="settings-section-title">面板 HTTPS(面板 SSL)</div>' +
        '<div class="form-item"><label style="display:flex;gap:10px;align-items:center;flex-wrap:wrap">' +
        '<input type="checkbox" id="ps-ssl-on" ' + (!sslState.hasCert || sslState.enabled ? "checked" : "") + '> 启用面板 HTTPS 访问</label></div>' +
        '<div class="form-item"><label>上传证书(PEM)</label>' +
        '<textarea class="textarea mono" id="ps-cert" rows="3" placeholder="证书内容(BEGIN CERTIFICATE ...),留空沿用已存证书"></textarea>' +
        '<textarea class="textarea mono" id="ps-key" rows="3" placeholder="私钥内容(BEGIN PRIVATE KEY ...)" style="margin-top:6px"></textarea></div>' +
        '<div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">' +
        '<button class="btn btn-primary btn-sm" id="ps-ssl-save">保存并重启面板</button>' +
        '<button class="btn btn-plain btn-sm" id="ps-selfsign">一键生成自签证书</button></div>' +
        '<div class="settings-hint">自签证书浏览器会提示不受信任,点击「继续访问」即可;保存后面板自动重启,请改用 https:// 协议访问;证书加载失败时面板会自动回退 HTTP,不会失联</div></div>' +
        '<div class="settings-section">' +
        '<div class="settings-section-title">修改密码</div>' +
        '<div class="settings-row"><label>原密码</label><input class="input" id="s-old-pass" type="password"></div>' +
        '<div class="settings-row"><label>新密码</label><input class="input" id="s-new-pass" type="password"><span class="settings-hint">至少 6 位</span></div>' +
        '<button class="btn btn-primary btn-sm" id="s-pass">修改密码</button></div>';

      body.querySelector("#totp-gen").onclick = function() {
        fetch("/api/v1/auth/totp/setup", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" }, body: "{}" })
          .then(function(r) { return r.json() }).then(function(d) {
            body.querySelector("#totp-secret").value = d.data.secret;
            body.querySelector("#totp-secret-box").style.display = "block";
          });
      };
      body.querySelector("#totp-enable").onclick = function() {
        fetch("/api/v1/auth/totp/enable", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" }, body: JSON.stringify({ code: body.querySelector("#totp-code").value.trim() }) })
          .then(function(r) { return r.json() }).then(function(d) {
            if (d.code === 0) { toast("两步验证已启用"); self.render(el); }
            else toast(d.message || "动态码错误", "error");
          });
      };
      body.querySelector("#totp-disable").onclick = function() {
        confirmDlg("确定关闭两步验证吗?").then(function(ok) {
          if (!ok) return;
          fetch("/api/v1/auth/totp/disable", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" }, body: "{}" })
            .then(function(r) { return r.json() }).then(function(d) {
              if (d.code === 0) { toast("已关闭"); self.render(el); }
              else toast(d.message || "关闭失败", "error");
            });
        });
      };
      body.querySelector("#s-pass").onclick = function() {
        fetch("/api/v1/settings/password", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" },
          body: JSON.stringify({ old_password: body.querySelector("#s-old-pass").value, new_password: body.querySelector("#s-new-pass").value })
        }).then(function(r) { return r.json() }).then(function(d) {
          if (d.code === 0) toast("密码修改成功");
          else toast(d.message || "修改失败", "error");
        });
      };
      // 面板 HTTPS
      var st = window.__psSSL || {};
      qs("#ps-selfsign", body).onclick = async function() {
        this.disabled = true; this.textContent = "生成中...";
        try {
          await api("/panel/ssl/selfsign", { method: "POST", body: {} });
          toast("自签证书已生成");
          this.textContent = "已生成 ✓";
        } catch (e) { toast(e.message, "error", 6000); this.disabled = false; }
      };
      qs("#ps-ssl-save", body).onclick = async function() {
        const on = qs("#ps-ssl-on", body).checked;
        const cert = qs("#ps-cert", body).value.trim();
        const key = qs("#ps-key", body).value.trim();
        if (on && !st.hasCert && !cert) { toast("请先上传证书或生成自签证书", "error"); return; }
        if (!(await confirmDlg("面板将以 HTTPS 模式重启,重启后请使用 <b>https://</b> 访问面板<br><span style='font-size:12px;color:var(--text-3)'>自签证书浏览器会提示不受信任;若证书异常,面板会自动回退 HTTP,不会失联</span>", { danger: false }))) return;
        try {
          await api("/panel/ssl", { method: "POST", body: { enabled: on, cert: cert, key: key } });
          toast("保存成功,面板正在重启...", "info", 5000);
          setTimeout(function() { location.href = location.href.replace(/^http:/, "https:"); location.reload(); }, 2500);
        } catch (e) { toast(e.message, "error", 8000); }
      };
      qs("#sec-checkup", body).onclick = function() { self.checkupDialog(); };
    }

    function renderAlert() {
      body.innerHTML =
        '<div class="settings-section">' +
        '<div class="settings-section-title">监控告警</div>' +
        '<div class="settings-row"><label style="display:flex;gap:6px;align-items:center"><input type="checkbox" id="al-enable" ' + (alertCfg.enabled ? "checked" : "") + '> 启用监控告警</label></div>' +
        '<div class="settings-row"><label>Webhook 地址</label><input class="input" id="al-webhook" value="' + esc(alertCfg.webhook || "") + '" placeholder="https://your-webhook/path"></div>' +
        '<div class="settings-row"><label>推送格式</label><select class="select" id="al-channel" style="max-width:240px">' +
        '<option value="generic"' + (alertCfg.channel !== "dingtalk" && alertCfg.channel !== "feishu" ? " selected" : "") + '>通用 JSON</option>' +
        '<option value="dingtalk"' + (alertCfg.channel === "dingtalk" ? " selected" : "") + '>钉钉机器人</option>' +
        '<option value="feishu"' + (alertCfg.channel === "feishu" ? " selected" : "") + '>飞书机器人</option></select>' +
        '<span class="settings-hint">钉钉/飞书群机器人地址粘贴到上方即可,面板会自动适配推送格式</span></div>' +
        '<div class="settings-grid"><div><label>CPU 阈值(%)</label><input class="input" id="al-cpu" type="number" value="' + alertCfg.cpu_thres + '"></div>' +
        '<div><label>内存阈值(%)</label><input class="input" id="al-mem" type="number" value="' + alertCfg.mem_thres + '"></div></div>' +
        '<div class="settings-row"><label>持续时间(秒)</label><input class="input" id="al-duration" type="number" value="' + alertCfg.duration + '"></div>' +
        '<div style="display:flex;gap:8px"><button class="btn btn-primary btn-sm" id="al-save">保存</button><button class="btn btn-plain btn-sm" id="al-test">发送测试</button></div>' +
        '<div class="settings-hint">CPU / 内存超过阈值并持续一段时间时,向 Webhook 推送 JSON 通知(恢复时也会推送)</div></div>' +
        '<div class="settings-section"><div class="settings-section-title">邮件告警(SMTP)</div>' +
        '<div class="settings-grid"><div><label>SMTP 服务器</label><input class="input" id="al-smtp-host" value="' + esc(alertCfg.smtp_host || "") + '" placeholder="如 smtp.qq.com(留空关闭邮件渠道)"></div>' +
        '<div><label>端口</label><select class="select" id="al-smtp-port">' +
        '<option value="465"' + (alertCfg.smtp_port === 465 ? " selected" : "") + '>465 (SSL)</option>' +
        '<option value="587"' + (alertCfg.smtp_port === 587 ? " selected" : "") + '>587 (STARTTLS)</option>' +
        '<option value="25"' + (alertCfg.smtp_port === 25 ? " selected" : "") + '>25 (明文)</option></select></div></div>' +
        '<div class="settings-grid"><div><label>账号</label><input class="input" id="al-smtp-user" value="' + esc(alertCfg.smtp_user || "") + '"></div>' +
        '<div><label>密码/授权码</label><input class="input" id="al-smtp-pass" type="password" value="' + esc(alertCfg.smtp_pass || "") + '"></div></div>' +
        '<div class="settings-grid"><div><label>发件人</label><input class="input" id="al-smtp-from" value="' + esc(alertCfg.mail_from || "") + '"></div>' +
        '<div><label>收件人(逗号分隔)</label><input class="input" id="al-smtp-to" value="' + esc(alertCfg.mail_to || "") + '"></div></div>' +
        '<div style="display:flex;gap:8px"><button class="btn btn-primary btn-sm" id="al-smtp-save">保存邮件配置</button><button class="btn btn-plain btn-sm" id="al-smtp-test">发送测试邮件</button></div>' +
        '<div class="settings-hint">宕机告警、证书到期、CPU/内存告警都会同时发送邮件。QQ/163 邮箱需使用授权码而非登录密码。</div></div>';

      body.querySelector("#al-save").onclick = function() {
        fetch("/api/v1/alert/settings", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" },
          body: JSON.stringify({
            enabled: body.querySelector("#al-enable").checked,
            webhook: body.querySelector("#al-webhook").value.trim(),
            channel: body.querySelector("#al-channel").value,
            cpu_thres: parseInt(body.querySelector("#al-cpu").value) || 90,
            mem_thres: parseInt(body.querySelector("#al-mem").value) || 90,
            duration: parseInt(body.querySelector("#al-duration").value) || 120,
            smtp_host: body.querySelector("#al-smtp-host").value.trim(),
            smtp_port: parseInt(body.querySelector("#al-smtp-port").value) || 465,
            smtp_user: body.querySelector("#al-smtp-user").value.trim(),
            smtp_pass: body.querySelector("#al-smtp-pass").value,
            mail_from: body.querySelector("#al-smtp-from").value.trim(),
            mail_to: body.querySelector("#al-smtp-to").value.trim()
          })
        }).then(function(r) { return r.json() }).then(function(d) {
          if (d.code === 0) toast("告警设置已保存"); else toast(d.message || "保存失败", "error");
        });
      };
      body.querySelector("#al-smtp-save").onclick = function() {
        body.querySelector("#al-save").click();
      };
      body.querySelector("#al-smtp-test").onclick = function() {
        fetch("/api/v1/alert/test", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" },
          body: JSON.stringify({ channel: "mail" })
        }).then(function(r) { return r.json() }).then(function(d) {
          if (d.code === 0) toast("测试邮件已发送,请查收"); else toast(d.message || "发送失败", "error", 5000);
        });
      };
      body.querySelector("#al-test").onclick = function() {
        fetch("/api/v1/alert/test", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" },
          body: JSON.stringify({ webhook: body.querySelector("#al-webhook").value.trim() })
        }).then(function(r) { return r.json() }).then(function(d) {
          if (d.code === 0) toast("测试通知已发送"); else toast(d.message || "发送失败", "error");
        });
      };
    }

    function renderBackup() {
      var rb = {};
      body.innerHTML =
        '<div class="settings-section">' +
        '<div class="settings-section-title">面板备份 / 恢复</div>' +
        '<div class="settings-hint" style="margin-bottom:12px">备份包含: 面板配置、SSL 证书、网站 Nginx 配置、应用 compose 文件<br>恢复后需重启面板生效</div>' +
        '<div style="display:flex;gap:8px;flex-wrap:wrap">' +
        '<button class="btn btn-primary btn-sm" id="bk-download">' + I("download") + ' 下载备份</button>' +
        '<button class="btn btn-plain btn-sm" id="bk-remote">' + I("upload") + ' 备份并上传到远程</button>' +
        '<button class="btn btn-plain btn-sm" id="bk-restore">上传恢复</button>' +
        '<input type="file" id="bk-file" accept=".tar.gz,.tgz" style="display:none"></div></div>' +
        '<div class="settings-section">' +
        '<div class="settings-section-title">远程备份目标 (SFTP)<span class="help" style="margin:0">配置后面板备份与计划任务可一键上云;兼容 NAS / 独立服务器 / 支持 SSH 的对象存储网关</span></div>' +
        '<div class="settings-grid"><div><label>主机地址</label><input class="input" id="rb-host" value="' + esc(rb.host || "") + '" placeholder="如 192.168.1.10 或 backup.example.com"></div>' +
        '<div><label>端口</label><input class="input" id="rb-port" type="number" value="' + (rb.port || 22) + '" style="max-width:140px"></div></div>' +
        '<div class="settings-grid"><div><label>用户名</label><input class="input" id="rb-user" value="' + esc(rb.user || "") + '"></div>' +
        '<div><label>密码</label><input class="input" id="rb-pass" type="password" value="' + esc(rb.password || "") + '"></div></div>' +
        '<div class="settings-row"><label>远程目录</label><input class="input" id="rb-dir" value="' + esc(rb.dir || "") + '" placeholder="如 /backups/kunpanel(不存在会自动创建)" style="max-width:420px"></div>' +
        '<div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:6px">' +
        '<button class="btn btn-primary btn-sm" id="rb-save">保存远程配置</button>' +
        '<button class="btn btn-plain btn-sm" id="rb-test">测试连接</button></div>' +
        '<div class="settings-hint">计划任务「目录备份」可勾选「同时上传到 SFTP 远程」,自动上云并清理本地副本</div></div>' +
        '<div class="settings-section">' +
        '<div class="settings-section-title">系统清理</div>' +
        '<div style="display:flex;gap:8px;flex-wrap:wrap">' +
        '<button class="btn btn-plain btn-sm" data-clean="apt">APT 缓存</button>' +
        '<button class="btn btn-plain btn-sm" data-clean="journal">Journal 日志</button>' +
        '<button class="btn btn-plain btn-sm" data-clean="docker">Docker 悬空镜像</button>' +
        '<button class="btn btn-plain btn-sm" data-clean="tmp">临时文件</button></div>' +
        '<div id="clean-result" class="settings-hint" style="margin-top:8px"></div></div>';

      api("/remote-backup/settings").then(function(c) {
        qs("#rb-host", body).value = c.host || "";
        qs("#rb-port", body).value = c.port || 22;
        qs("#rb-user", body).value = c.user || "";
        qs("#rb-pass", body).value = c.password || "";
        qs("#rb-dir", body).value = c.dir || "";
      }).catch(function() {});
      var rbCfg = function() {
        return {
          host: qs("#rb-host", body).value.trim(),
          port: parseInt(qs("#rb-port", body).value) || 22,
          user: qs("#rb-user", body).value.trim(),
          password: qs("#rb-pass", body).value,
          dir: qs("#rb-dir", body).value.trim() || ".",
        };
      };
      qs("#rb-save", body).onclick = async function() {
        try { await api("/remote-backup/settings", { method: "POST", body: rbCfg() }); toast("远程备份配置已保存"); }
        catch (e) { toast(e.message, "error"); }
      };
      qs("#rb-test", body).onclick = async function() {
        var b = this;
        b.disabled = true; b.textContent = "测试中...";
        try { var msg = await api("/remote-backup/test", { method: "POST", body: rbCfg() }); toast(msg || "连接成功"); }
        catch (e) { toast(e.message, "error", 6000); }
        b.disabled = false; b.textContent = "测试连接";
      };
      qs("#bk-remote", body).onclick = function() {
        api("/panel/backup/remote", { method: "POST", body: {} }).then(function(d) {
          if (d && d.id) taskModal(d.id, function() { toast("远程备份完成"); });
          else toast("任务创建失败", "error");
        }).catch(function(e) { toast(e.message, "error", 6000); });
      };

      body.querySelector("#bk-download").onclick = function() {
        window.open("/api/v1/panel/backup?token=" + encodeURIComponent(localStorage.getItem(TOKEN_KEY)), "_blank");
      };
      body.querySelector("#bk-restore").onclick = function() { body.querySelector("#bk-file").click(); };
      body.querySelector("#bk-file").onchange = function(ev) {
        var f = ev.target.files[0]; if (!f) return;
        confirmDlg("恢复备份将覆盖当前面板配置,确定继续吗?").then(function(ok) {
          if (!ok) { ev.target.value = ""; return; }
          var fd = new FormData(); fd.append("backup", f);
          fetch("/api/v1/panel/restore", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY) }, body: fd })
            .then(function(r) { return r.json() }).then(function(d) {
              if (d.code === 0) toast(d.data.message || "恢复完成", "info", 5000);
              else toast(d.message || "恢复失败", "error");
            });
          ev.target.value = "";
        });
      };
      body.querySelectorAll("[data-clean]").forEach(function(b) {
        b.onclick = function() {
          b.disabled = true;
          b.textContent = b.textContent + "…";
          fetch("/api/v1/system/cleanup", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" },
            body: JSON.stringify({ [b.getAttribute("data-clean")]: true })
          }).then(function(r) { return r.json() }).then(function(d) {
            if (d.code === 0) { body.querySelector("#clean-result").textContent = (d.data.results || []).join(" · "); toast("清理完成"); }
            else toast(d.message || "清理失败", "error");
          }).catch(function(e) { toast(e.message || "请求失败", "error"); })
            .finally(function() { b.disabled = false; b.textContent = b.getAttribute("data-label"); });
        };
        b.setAttribute("data-label", b.textContent);
      });
    }

    function renderAbout() {
      body.innerHTML =
        '<div class="settings-section">' +
        '<div class="settings-section-title">面板更新</div>' +
        '<div class="settings-hint" style="margin-bottom:10px">对接 GitHub Releases,自动检查新版本;一键更新会下载安装包并自动重启面板(旧版备份为 .bak)</div>' +
        '<div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">' +
        '<span class="help" style="margin:0" id="upd-state">当前版本 ' + esc(s.version) + '</span>' +
        '<button class="btn btn-sm btn-plain" id="s-upd-check">' + I("refresh") + ' 检查更新</button>' +
        '<button class="btn btn-sm btn-primary" id="s-upd-apply" style="display:none">' + I("download") + ' 一键更新</button>' +
        '</div>' +
        '<pre class="am-raw" id="upd-notes" style="display:none;max-height:200px;overflow:auto;white-space:pre-wrap;word-break:break-word;margin-top:10px"></pre></div>' +
        '<div class="settings-section">' +
        '<div class="settings-section-title">关于面板</div>' +
        '<div class="desc-grid">' +
        '<div class="desc-item"><span class="k">面板名称</span><span class="v">' + esc(s.panel_name) + '</span></div>' +
        '<div class="desc-item"><span class="k">当前版本</span><span class="v">' + esc(s.version) + '</span></div>' +
        '<div class="desc-item"><span class="k">运行环境</span><span class="v">' + esc(s.os) + ' / ' + esc(s.arch) + '</span></div>' +
        '<div class="desc-item"><span class="k">数据目录</span><span class="v">' + esc(s.data_dir) + '</span></div>' +
        '<div class="desc-item"><span class="k">安装时间</span><span class="v">' + esc(s.created_at) + '</span></div>' +
        '<div class="desc-item"><span class="k">后台地址</span><span class="v">http://IP:' + s.port + '/' + (s.entrance && s.entrance !== "-" ? esc(s.entrance) : "(未设置)") + '</span></div>' +
        '</div></div>' +
        '<div class="settings-section">' +
        '<div class="settings-section-title">重启面板</div>' +
        '<div class="settings-hint" style="margin-bottom:8px">重启期间面板将短暂不可访问</div>' +
        '<button class="btn btn-danger-plain btn-sm" id="s-restart">' + I("restart") + ' 重启面板</button></div>';

      var updTarget = "";
      qs("#s-upd-check", body).onclick = async function() {
        var btn = this;
        btn.disabled = true;
        btn.innerHTML = '<span class="spinner" style="width:12px;height:12px;border-width:2px;display:inline-block;vertical-align:middle;margin-right:4px"></span> 检查中…';
        qs("#upd-state", body).textContent = "正在连接 GitHub 检查 ...";
        try {
          var d = await api("/panel/update?force=1");
          if (d.has_update) {
            updTarget = d.latest;
            qs("#upd-state", body).innerHTML = '发现新版本 <b class="mono">' + esc(d.latest) + '</b>(当前 ' + esc(d.current) + ',发布于 ' + esc((d.published_at || "").slice(0, 10)) + ')';
            qs("#s-upd-apply", body).style.display = "";
            var notes = qs("#upd-notes", body);
            notes.style.display = "";
            notes.textContent = (d.name ? d.name + "\n\n" : "") + (d.notes || "(无更新说明)");
          } else {
            qs("#upd-state", body).textContent = "当前已是最新版本 " + d.latest + "(检查于 " + (d.checked_at || "") + ")";
            qs("#s-upd-apply", body).style.display = "none";
            qs("#upd-notes", body).style.display = "none";
          }
        } catch (e) {
          qs("#upd-state", body).textContent = "检查失败: " + e.message;
          toast("检查更新失败: " + e.message, "error");
        }
        var sv = qs("#s-upd-check", body);
        if (sv) { sv.disabled = false; sv.innerHTML = I("refresh") + " 检查更新"; }
        btn.disabled = false;
      };
      qs("#s-upd-apply", body).onclick = async function() {
        if (!updTarget) return;
        if (!(await confirmDlg("确定更新到 " + updTarget + " 吗?更新过程中面板会自动重启,期间短暂不可访问。"))) return;
        try {
          var d = await api("/panel/update/apply", { method: "POST", body: { tag: updTarget } });
          taskModal(d.id, function(status) {
            if (status === "success") {
              toast("更新完成,页面即将刷新 ...", "success", 6000);
              setTimeout(function() { location.reload(); }, 4000);
            }
          });
        } catch (e) { toast(e.message, "error"); }
      };

      body.querySelector("#s-restart").onclick = function() {
        confirmDlg("确定重启面板服务吗?").then(function(ok) {
          if (!ok) return;
          fetch("/api/v1/panel/restart", { method: "POST", headers: { "Authorization": "Bearer " + localStorage.getItem(TOKEN_KEY), "Content-Type": "application/json" }, body: "{}" })
            .then(function(r) { return r.json() }).then(function(d) {
              toast(d.message || "重启中", "info", 5000);
              setTimeout(function() { location.reload(); }, 4000);
            });
        });
      };
    }

    var renderFns = { general: renderGeneral, security: renderSecurity, alert: renderAlert, backup: renderBackup, about: renderAbout };
    if (renderFns[this.tab]) renderFns[this.tab](); else renderFns.general();
  },
  async checkupDialog() {
    var m = modal({
      title: "面板安全自检", width: 660,
      body: '<div style="text-align:center;padding:10px 0 16px">' +
        '<div style="position:relative;display:inline-block"><div id="cu-ring"></div>' +
        '<div style="position:absolute;inset:0;display:flex;align-items:center;justify-content:center;font-size:26px;font-weight:700" id="cu-score">--</div></div>' +
        '<div class="help" id="cu-summary">检查中...</div></div>' +
        '<div id="cu-items">' + skeletonTable(5, 3) + "</div>",
      footer: '<button class="btn btn-plain" data-act="rerun">重新检查</button><button class="btn btn-primary" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    qs('[data-act="rerun"]', m.el).onclick = function() { run(); };
    function ring(pct) {
      const size = 108, sw = 9, r = (size - sw) / 2 - 1, c = 2 * Math.PI * r;
      const color = pct >= 90 ? "#4ade80" : pct >= 70 ? "#e6a23c" : "#f56c6c";
      return '<svg class="ring" width="' + size + '" height="' + size + '" viewBox="0 0 ' + size + " " + size + '">' +
        '<circle cx="' + size/2 + '" cy="' + size/2 + '" r="' + r + '" stroke="#e8eef5" stroke-width="' + sw + '" fill="none"/>' +
        '<circle cx="' + size/2 + '" cy="' + size/2 + '" r="' + r + '" stroke="' + color + '" stroke-width="' + sw + '" fill="none" stroke-linecap="round" ' +
        'stroke-dasharray="' + c + '" stroke-dashoffset="' + (c * (100 - pct) / 100).toFixed(1) + '" transform="rotate(-90 ' + size/2 + ' ' + size/2 + ')"/></svg>';
    }
    async function run() {
      const itemsBox = qs("#cu-items", m.el);
      const scoreBox = qs("#cu-score", m.el);
      const ringBox = qs("#cu-ring", m.el);
      const sumBox = qs("#cu-summary", m.el);
      if (itemsBox) itemsBox.innerHTML = skeletonTable(5, 3);
      if (scoreBox) scoreBox.textContent = "--";
      if (sumBox) sumBox.textContent = "检查中...";
      try {
        const d = await api("/panel/checkup");
        const score = d.score || 0;
        if (scoreBox) scoreBox.textContent = score;
        if (ringBox) ringBox.innerHTML = ring(score);
        if (sumBox) sumBox.textContent = score >= 95 ? "状态极佳" : score >= 80 ? "状态良好,有优化空间" : score >= 60 ? "存在问题,建议尽快处理" : "存在严重问题,请立即处理!";
        const ic = { ok: ["✓", "#4ade80"], warn: ["!", "#e6a23c"], fail: ["✗", "#f56c6c"] };
        if (itemsBox) itemsBox.innerHTML = (d.items || []).map(function(it) {
          const i2 = ic[it.status] || ic.system;
          return '<div class="cu-item"><span class="cu-badge" style="color:' + i2[1] + ';border-color:' + i2[1] + '">' + i2[0] + "</span>" +
            '<div class="cu-item-body"><b>' + esc(it.name) + "</b>" + (it.detail ? '<div class="help" style="margin:2px 0 0">' + esc(it.detail) + "</div>" : "") + "</div></div>";
        }).join("");
      } catch (e) {
        if (itemsBox) itemsBox.innerHTML = emptyState("warn", "检查失败", e.message);
      }
    }
    run();
  },
};

/* ================= 文件管理 ================= */
function fmIcon(e) {
  if (e.is_dir) return I("folderOpen", "fm-ico fm-dir-ico");
  var n = (e.name || "").toLowerCase();
  if (/\.(zip|tar|gz|tgz|bz2|xz|7z|rar)$/.test(n)) return I("archive", "fm-ico arc");
  if (/\.(png|jpg|jpeg|gif|webp|svg|ico|bmp)$/.test(n)) return I("eye", "fm-ico img");
  return I("file", "fm-ico fil");
}
function fmTimeShort(t) {
  var full = fmTime(t);
  return full.slice(0, 5) === String(new Date().getFullYear()) + "-" ? full.slice(5) : full;
}
function fmTime(t) {
  if (!t) return "-";
  var d = new Date(t);
  if (isNaN(d.getTime())) return esc(t);
  var p = function(x) { return (x < 10 ? "0" : "") + x; };
  return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " + p(d.getHours()) + ":" + p(d.getMinutes());
}
PAGES.files = {
  title: "文件",
  cwd: "",
  sel: {},
  searchMode: "",
  searchQ: "",
  async render(el) {
    // 支持 #/files?path=xxx(网站页跳转)
    var qm = location.hash.indexOf("?");
    if (qm >= 0) {
      var usp = new URLSearchParams(location.hash.slice(qm + 1));
      var p0 = usp.get("path");
      if (p0) { this.cwd = p0; this.searchMode = ""; this.searchQ = ""; this.sel = {}; }
    }
    el.innerHTML = '<div class="card fm-card"><div id="fm-root">' + skeletonTable(9, 5) + "</div>" +
      '<div class="fm-dropzone" id="fm-dropzone"><div class="fm-dropzone-inner">' + I("upload") +
      '<div>松开鼠标,上传到当前目录</div><div class="help" style="margin-top:4px;font-weight:400">支持多选文件,文件夹请先打包</div></div></div></div>';
    await this.load(el);
  },
  async load(el) {
    var root = qs("#fm-root", el);
    if (!root) return;
    var self = this;
    var data;
    try {
      if (this.searchMode === "name") {
        var rs = await api("/files/search?path=" + encodeURIComponent(this.cwd) + "&q=" + encodeURIComponent(this.searchQ));
        data = { results: rs || [], grep: false };
      } else if (this.searchMode === "code") {
        var rg = await api("/files/grep?path=" + encodeURIComponent(this.cwd) + "&q=" + encodeURIComponent(this.searchQ));
        data = { results: rg || [], grep: true };
      } else {
        data = await api("/files?path=" + encodeURIComponent(this.cwd));
      }
    } catch (e) {
      root.innerHTML = emptyState("warn", "加载失败", e.message) +
        '<div style="text-align:center;padding-bottom:16px"><button class="btn btn-plain btn-sm" id="fm-retry">' + I("refresh") + " 重试</button></div>";
      var rb = qs("#fm-retry", root);
      if (rb) rb.onclick = function() { self.load(el); };
      return;
    }
    if (data && data.path) this.cwd = data.path;

    /* ----- 工具栏 ----- */
    var inSearch = this.searchMode !== "";
    var html = '<div class="fm-toolbar">' +
      '<button class="btn btn-sm btn-plain" id="fm-home" title="回到根目录">' + I("home") + "</button>" +
      '<input class="fm-path-input mono" id="fm-path-input" value="' + esc(self.cwd || "/") + '" spellcheck="false" title="可直接编辑路径后回车跳转">' +
      '<button class="btn btn-sm btn-plain" id="fm-up" title="上级目录">' + I("chevronLeft") + "</button>" +
      '<button class="btn btn-sm btn-plain" id="fm-refresh" title="刷新">' + I("refresh") + "</button>" +
      '<span class="fm-sep"></span>' +
      (inSearch
        ? '<button class="btn btn-sm btn-plain" id="fm-exit-search">' + I("close") + " 退出搜索</button>"
        : '<button class="btn btn-sm btn-plain" id="fm-newfile">' + I("file") + " 新建文件</button>" +
          '<button class="btn btn-sm btn-plain" id="fm-newdir">' + I("folder") + " 新建目录</button>" +
          '<button class="btn btn-sm btn-plain" id="fm-upload" title="也可直接把文件拖入下方列表上传">' + I("upload") + " 上传</button>" +
          '<button class="btn btn-sm btn-plain" id="fm-remote">' + I("download") + " 远程下载</button>" +
          '<button class="btn btn-sm btn-plain" id="fm-trash">' + I("trash") + " 回收站</button>") +
      '<span class="fm-sep"></span>' +
      '<div class="fm-quick">' +
      [['网站目录', '/opt/kunpanel/data/websites/roots'], ['数据库备份', '/opt/kunpanel/data/backups/db'], ['站点日志', '/opt/kunpanel/data/websites/logs'], ['面板备份', '/opt/kunpanel/data/backups/panel']].map(function(q2) {
        return '<span class="fm-quick-item" data-q="' + esc(q2[1]) + '">' + q2[0] + "</span>";
      }).join("") + "</div>" +
      '<span class="fm-sep"></span>' +
      '<div class="fm-search">' + I("search") +
      '<input id="fm-search-input" placeholder="搜索当前目录…" value="' + esc(inSearch ? this.searchQ : "") + '">' +
      '<div class="fm-search-mode"><button data-m="name" class="' + (this.searchMode !== "code" ? "on" : "") + '">文件名</button>' +
      '<button data-m="code" class="' + (this.searchMode === "code" ? "on" : "") + '" title="搜索文件内容">内容</button></div>' +
      "</div></div>";
    if (!inSearch) html += '<input type="file" id="fm-file-input" multiple style="display:none">';

    /* ----- 列表 ----- */
    if (inSearch) {
      var rows = data.results || [];
      if (!rows.length) {
        html += emptyState("search", "未找到匹配结果", this.searchMode === "code" ? "内容搜索基于 grep,最多返回 200 条" : "换个关键词试试");
      } else if (data.grep) {
        html += '<div class="table-wrap"><table class="table"><thead><tr><th style="width:34%">文件</th><th style="width:56px">行号</th><th>内容</th><th style="width:70px">操作</th></tr></thead><tbody>';
        rows.forEach(function(r, i) {
          html += '<tr><td class="mono fm-ellipsis" title="' + esc(r.file) + '">' + esc(r.file) + '</td><td class="mono">' + esc(r.line) +
            '</td><td class="mono fm-ellipsis" title="' + esc(r.text) + '">' + esc(r.text) +
            '</td><td><button class="btn btn-xs btn-plain" data-fm-edit="' + esc(r.file) + '">编辑</button></td></tr>';
        });
        html += "</tbody></table></div>";
      } else {
        html += '<div class="table-wrap"><table class="table"><thead><tr><th>文件</th><th style="width:110px">大小</th><th style="width:150px">修改时间</th><th style="width:150px">操作</th></tr></thead><tbody>';
        rows.forEach(function(r) {
          html += '<tr><td>' + fmIcon(r) + ' <a href="javascript:void(0)" class="fm-name" data-fm-open="' + esc(r.path) + '" data-dir="0">' + esc(r.name) + "</a></td>" +
            "<td>" + fmtBytes(r.size) + "</td><td>" + esc(r.mod_time || "-") + "</td>" +
            '<td><button class="btn btn-xs btn-plain" data-fm-edit="' + esc(r.path) + '">编辑</button>' +
            '<button class="btn btn-xs btn-plain" data-fm-loc="' + esc(r.path) + '">定位</button></td></tr>';
        });
        html += "</tbody></table></div>";
      }
    } else {
      var entries = data.entries || [];
      var selCount = Object.keys(this.sel).length;
      if (selCount) {
        html += '<div class="fm-selbar page-fade"><span>已选 <b>' + selCount + "</b> 项</span>" +
          '<div class="fm-selbar-ops">' +
          '<button class="btn btn-xs btn-plain" data-fmb="compress">' + I("archive") + " 压缩</button>" +
          '<button class="btn btn-xs btn-plain" data-fmb="copy">' + I("copy") + " 复制</button>" +
          '<button class="btn btn-xs btn-plain" data-fmb="move">' + I("shuffle") + " 移动</button>" +
          '<button class="btn btn-xs btn-plain" data-fmb="chmod">' + I("key") + " 权限</button>" +
          '<button class="btn btn-xs btn-danger-plain" data-fmb="delete">' + I("trash") + " 删除</button>" +
          '<button class="btn btn-xs btn-plain" data-fmb="cancel">取消选择</button></div></div>';
      }
      var cols = '<tr><th style="width:36px"><input type="checkbox" id="fm-selall"></th><th>名称</th><th style="width:100px">大小</th><th style="width:110px">权限</th><th style="width:150px">修改时间</th><th style="width:280px">操作</th></tr>';
      html += '<div class="table-wrap"><table class="table fm-table"><thead>' + cols + "</thead><tbody>";
      if (data.parent && data.parent !== this.cwd) {
        html += '<tr class="fm-parent"><td></td><td colspan="5">' + I("chevronLeft", "fm-ico") +
          ' <a href="javascript:void(0)" class="fm-name" data-fm-open="' + esc(data.parent) + '" data-dir="1">.. (上级目录)</a></td></tr>';
      }
      if (!entries.length) {
        html += '<tr><td colspan="6" style="text-align:center;padding:28px;color:var(--text-3)">空目录</td></tr>';
      }
      var self2 = this;
      entries.forEach(function(e) {
        var isSel = !!self2.sel[e.path];
        var isOpen = '<button class="btn btn-xs btn-plain" data-fm-open="' + esc(e.path) + '" data-dir="' + (e.is_dir ? 1 : 0) + '">' + (e.is_dir ? "打开" : "编辑") + "</button>";
        var more = '<button class="btn btn-xs btn-plain" data-fm-more="' + esc(e.path) + '" data-name="' + esc(e.name) + '" data-dir="' + (e.is_dir ? 1 : 0) + '" data-mode="' + esc((e.mode || "").split(".")[0].slice(1)) + '">更多</button>';
        var ops;
        if (e.is_dir) {
          ops = isOpen + '<button class="btn btn-xs btn-plain" data-fm-zipdir="' + esc(e.path) + '">压缩</button>' + more;
        } else {
          var n = e.name.toLowerCase();
          var isArc = /\.(zip|tar|gz|tgz|bz2|xz)$/.test(n);
          ops = isOpen +
            (isArc ? '<button class="btn btn-xs btn-plain" data-fm-extract="' + esc(e.path) + '">解压</button>' : "") +
            '<button class="btn btn-xs btn-plain" data-fm-dl="' + esc(e.path) + '">下载</button>' + more;
        }
        html += '<tr' + (isSel ? ' class="fm-selected"' : "") + "><td><input type=\"checkbox\" class=\"fm-check\" data-path=\"" + esc(e.path) + "\"" + (isSel ? " checked" : "") + "></td>" +
          "<td>" + fmIcon(e) + ' <a href="javascript:void(0)" class="fm-name" data-fm-open="' + esc(e.path) + '" data-dir="' + (e.is_dir ? 1 : 0) + '" data-name="' + esc(e.name) + '">' + esc(e.name) + "</a>" +
          (e.symlink ? ' <span class="fm-symlink" title="' + esc(e.symlink) + '">→ ' + esc(e.symlink) + "</span>" : "") + "</td>" +
          '<td class="fm-size">' + (e.is_dir ? "—" : fmtBytes(e.size)) + "</td>" +
          '<td class="mono fm-perm">' + esc(e.mode || "") + "</td>" +
          '<td class="fm-size">' + fmTimeShort(e.mod_time) + "</td>" +
          "<td><div class=\"fm-ops\">" + ops + "</div></td></tr>";
      });
      html += "</tbody></table></div>";
    }
    root.innerHTML = html;
    this.bind(el, data);
  },
  bind(el, data) {
    var self = this;
    var inSearch = this.searchMode !== "";
    var refresh = function() { self.load(el); };
    var go = function(path, isDir) {
      if (isDir) {
        self.cwd = path;
        self.searchMode = ""; self.searchQ = ""; self.sel = {};
        history.replaceState(null, "", "#/files?path=" + encodeURIComponent(path));
        refresh();
      } else {
        // 图片直接预览,其余走编辑器
        if (/\.(png|jpe?g|gif|webp|svg|ico|bmp)$/i.test(path)) { self.imagePreview(path); return; }
        self.editFile(path, this);
      }
    };
    qsa("[data-fm-open]", el).forEach(function(a) {
      a.onclick = function() { go(a.getAttribute("data-fm-open"), a.getAttribute("data-dir") === "1"); };
    });
    var homeBtn = qs("#fm-home", el);
    if (homeBtn) homeBtn.onclick = function() { go("/", true); };
    var pathInput = qs("#fm-path-input", el);
    if (pathInput) pathInput.onkeydown = function(e) {
      if (e.key === "Enter") {
        var p = this.value.trim() || "/";
        go(p, true);
      }
    };
    qsa(".fm-quick-item", el).forEach(function(q2) {
      q2.onclick = function() {
        go(q2.getAttribute("data-q"), true);
      };
    });
    var up = qs("#fm-up", el);
    if (up) up.onclick = function() {
      var parent = (data && data.parent) || self.cwd.replace(/\/[^/]*\/?$/, "") || "/";
      if (parent === self.cwd) parent = "/";
      go(parent, true);
    };
    var rf = qs("#fm-refresh", el);
    if (rf) rf.onclick = refresh;
    // 新建
    var nf = qs("#fm-newfile", el);
    if (nf) nf.onclick = async function() {
      var r = await promptDlg("新建文件", [{ key: "name", label: "文件名", required: true, placeholder: "如 index.html" }]);
      if (!r) return;
      try { await api("/files/save", { method: "POST", body: { path: self.cwd + "/" + r.name, content: "" } }); toast("文件已创建"); refresh(); }
      catch (e) { toast(e.message, "error"); }
    };
    var nd = qs("#fm-newdir", el);
    if (nd) nd.onclick = async function() {
      var r = await promptDlg("新建目录", [{ key: "name", label: "目录名", required: true }]);
      if (!r) return;
      try { await api("/files/mkdir", { method: "POST", body: { path: self.cwd + "/" + r.name } }); toast("目录已创建"); refresh(); }
      catch (e) { toast(e.message, "error"); }
    };
    // 上传(按钮选文件 + 拖拽,共用 uploadFiles)
    var upBtn = qs("#fm-upload", el), fi = qs("#fm-file-input", el);
    if (upBtn && fi) {
      upBtn.onclick = function() { fi.value = ""; fi.click(); };
      fi.onchange = function() {
        if (!fi.files.length) return;
        self.uploadFiles(el, fi.files);
      };
    }
    var rm = qs("#fm-remote", el);
    if (rm) rm.onclick = function() {
      var mm = modal({
        title: "远程下载", width: 560,
        body: '<div class="form-item required"><label>文件 URL</label><input class="input mono" id="rd-url" placeholder="https://example.com/file.zip"></div>' +
          '<div class="form-item"><label>保存文件名(留空自动取 URL 末段)</label><input class="input" id="rd-name" placeholder="可选"></div>' +
          '<div class="help">下载到当前目录:' + esc(self.cwd) + ',最大 2GB</div>',
        footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">开始下载</button>',
      });
      qs('[data-act="no"]', mm.el).onclick = mm.close;
      qs('[data-act="yes"]', mm.el).onclick = async function() {
        var u = qs("#rd-url", mm.el).value.trim();
        if (!u) { toast("请输入 URL", "error"); return; }
        try {
          var d = await api("/files/remote-download", { method: "POST", body: { url: u, dir: self.cwd, name: qs("#rd-name", mm.el).value.trim() } });
          mm.close();
          taskModal(d.id, function() { self.load(el); });
        } catch (e) { toast(e.message, "error", 6000); }
      };
    };
    var card = qs(".fm-card", el), dz = qs("#fm-dropzone", el);
    if (card && dz && !card.dataset.dropBound) {
      card.dataset.dropBound = "1"; // bind 每次 load 都会执行,防止拖拽监听重复叠加导致重复上传
      var dragDepth = 0;
      card.addEventListener("dragenter", function(e) {
        e.preventDefault();
        if (self.searchMode) return;
        dragDepth++;
        dz.classList.add("show");
      });
      card.addEventListener("dragover", function(e) { e.preventDefault(); });
      card.addEventListener("dragleave", function() {
        dragDepth--;
        if (dragDepth <= 0) { dragDepth = 0; dz.classList.remove("show"); }
      });
      card.addEventListener("drop", function(e) {
        e.preventDefault();
        dragDepth = 0;
        dz.classList.remove("show");
        if (self.searchMode) { toast("请先退出搜索,再上传到目录", "error"); return; }
        var files = e.dataTransfer && e.dataTransfer.files;
        if (!files || !files.length) { toast("暂不支持拖入文件夹,请先打包成压缩包", "error"); return; }
        self.uploadFiles(el, files);
      });
    }
    // 回收站
    var trashBtn = qs("#fm-trash", el);
    if (trashBtn) trashBtn.onclick = function() { self.trashDialog(el); };
    // 搜索
    var searchInput = qs("#fm-search-input", el);
    var doSearch = function(mode) {
      var qv = (searchInput.value || "").trim();
      if (!qv) { toast("请输入搜索关键词", "error"); return; }
      self.searchMode = mode; self.searchQ = qv; self.sel = {};
      refresh();
    };
    qsa(".fm-search-mode button", el).forEach(function(mb) {
      mb.onclick = function() {
        qsa(".fm-search-mode button", el).forEach(function(x) { x.classList.toggle("on", x === mb); });
        self.searchMode = mb.getAttribute("data-m");
        if ((searchInput.value || "").trim()) doSearch(self.searchMode);
      };
    });
    if (searchInput) searchInput.onkeydown = function(e) { if (e.key === "Enter") doSearch(self.searchMode || "name"); };
    var es = qs("#fm-exit-search", el);
    if (es) es.onclick = function() { self.searchMode = ""; self.searchQ = ""; refresh(); };
    // 搜索结果操作
    qsa("[data-fm-edit]", el).forEach(function(b) {
      b.onclick = function() { self.editFile(b.getAttribute("data-fm-edit"), b); };
    });
    qsa("[data-fm-loc]", el).forEach(function(b) {
      b.onclick = function() {
        var p = b.getAttribute("data-fm-loc");
        self.cwd = p.substring(0, p.lastIndexOf("/")) || "/";
        self.searchMode = ""; self.searchQ = "";
        history.replaceState(null, "", "#/files?path=" + encodeURIComponent(self.cwd));
        refresh();
      };
    });
    if (!inSearch) {
      // 全选
      var selAll = qs("#fm-selall", el);
      if (selAll) selAll.onchange = function() {
        self.sel = {};
        if (selAll.checked) {
          (data.entries || []).forEach(function(e) { self.sel[e.path] = true; });
        }
        refresh();
      };
      qsa(".fm-check", el).forEach(function(c) {
        c.onchange = function() {
          if (c.checked) self.sel[c.getAttribute("data-path")] = true;
          else delete self.sel[c.getAttribute("data-path")];
          refresh();
        };
      });
      // 批量
      qsa("[data-fmb]", el).forEach(function(b) {
        b.onclick = function() { self.batchOp(b.getAttribute("data-fmb"), el); };
      });
      // ---- 单文件/目录操作(行内按钮 + 更多菜单共用) ----
      var doRename = async function(p, name) {
        var r = await promptDlg("重命名", [{ key: "name", label: "新名称", required: true, value: name }]);
        if (!r || r.name === name) return;
        try { await api("/files/rename", { method: "POST", body: { old_path: p, new_path: self.cwd + "/" + r.name } }); toast("已重命名"); refresh(); }
        catch (e) { toast(e.message, "error"); }
      };
      var doChmod = async function(p, cur) {
        var r = await promptDlg("修改权限", [{ key: "mode", label: "权限(八进制,如 644 / 755)", required: true, value: cur }]);
        if (!r) return;
        var v = parseInt(r.mode, 8);
        if (isNaN(v) || v <= 0) { toast("权限格式错误", "error"); return; }
        try { await api("/files/chmod", { method: "POST", body: { path: p, mode: v } }); toast("权限已修改"); refresh(); }
        catch (e) { toast(e.message, "error"); }
      };
      var doDel = async function(p, name) {
        if (!(await confirmDlg("删除 <b>" + esc(name) + "</b>?<br><span style='font-size:12px;color:var(--text-3)'>将移入回收站,可随时恢复</span>", { danger: true }))) return;
        try {
          var r = await api("/files/delete", { method: "POST", body: { path: p } });
          toast(r.mode === "permanent" ? "已删除" : "已移入回收站");
          refresh();
        }
        catch (e) { toast(e.message, "error"); }
      };
      var doDl = function(p) {
        var token = encodeURIComponent(localStorage.getItem(TOKEN_KEY) || "");
        location.href = "/api/v1/files/download?path=" + encodeURIComponent(p) + "&token=" + token;
      };
      var doExtract = async function(p) {
        toast("解压中...", "info");
        try {
          var r = await api("/files/extract", { method: "POST", body: { src: p } });
          toast("已解压到 " + (r.dest || "目标目录"));
          refresh();
        } catch (e) { toast(e.message, "error"); }
      };
      var doZip = async function(p) {
        var def = (p.split("/").filter(Boolean).pop() || "archive") + ".zip";
        var r = await promptDlg("压缩", [{ key: "name", label: "压缩包名", required: true, value: def }]);
        if (!r) return;
        toast("压缩中...", "info");
        try { await api("/files/compress", { method: "POST", body: { dst: self.cwd + "/" + r.name, sources: [p] } }); toast("压缩完成"); refresh(); }
        catch (e) { toast(e.message, "error"); }
      };
      // 更多菜单(固定定位,避免被表格滚动裁剪)
      var closeMoreMenu = function() {
        var m = qs("#fm-more-menu");
        if (m) m.remove();
        document.removeEventListener("click", onDocClickMore);
      };
      var onDocClickMore = function(ev) {
        var m = qs("#fm-more-menu");
        if (m && !m.contains(ev.target)) closeMoreMenu();
      };
      var openMoreMenu = function(btn) {
        closeMoreMenu();
        var p = btn.getAttribute("data-fm-more"), name = btn.getAttribute("data-name"), isDir = btn.getAttribute("data-dir") === "1";
        var cur = btn.getAttribute("data-mode") || "";
        var menu = document.createElement("div");
        menu.id = "fm-more-menu";
        menu.className = "fm-more-menu";
        menu.innerHTML =
          '<button data-act="rename">' + I("edit") + " 重命名</button>" +
          '<button data-act="chmod">' + I("key") + " 权限</button>" +
          '<button data-act="zip">' + I("archive") + " 压缩" + (isDir ? "" : " (单文件)") + "</button>" +
          "<hr>" +
          '<button data-act="del" class="danger">' + I("trash") + " 删除</button>";
        document.body.appendChild(menu);
        var rc = btn.getBoundingClientRect();
        var mw = menu.offsetWidth, mh = menu.offsetHeight;
        var left = Math.min(rc.left, window.innerWidth - mw - 8);
        var top = rc.bottom + 6;
        if (top + mh > window.innerHeight - 8) top = Math.max(8, rc.top - mh - 6);
        menu.style.left = left + "px";
        menu.style.top = top + "px";
        menu.classList.add("show");
        menu.querySelector('[data-act="rename"]').onclick = function() { closeMoreMenu(); doRename(p, name); };
        menu.querySelector('[data-act="chmod"]').onclick = function() { closeMoreMenu(); doChmod(p, cur); };
        menu.querySelector('[data-act="zip"]').onclick = function() { closeMoreMenu(); doZip(p); };
        menu.querySelector('[data-act="del"]').onclick = function() { closeMoreMenu(); doDel(p, name); };
        setTimeout(function() { document.addEventListener("click", onDocClickMore); }, 0);
      };
      qsa("[data-fm-more]", el).forEach(function(b) {
        b.onclick = function(ev) { ev.stopPropagation(); openMoreMenu(b); };
      });
      // 行内按钮
      qsa("[data-fm-dl]", el).forEach(function(b) {
        b.onclick = function() { doDl(b.getAttribute("data-fm-dl")); };
      });
      qsa("[data-fm-extract]", el).forEach(function(b) {
        b.onclick = function() { doExtract(b.getAttribute("data-fm-extract")); };
      });
      qsa("[data-fm-zipdir]", el).forEach(function(b) {
        b.onclick = function() { doZip(b.getAttribute("data-fm-zipdir")); };
      });
    }
  },
  /* 逐个上传文件(选择/拖拽共用),完成后刷新 */
  async uploadFiles(el, files) {
    var self = this;
    var list = Array.prototype.slice.call(files);
    if (!list.length) return;
    toast("正在上传 " + list.length + " 个文件...", "info");
    var ok = 0, fail = 0;
    for (var i = 0; i < list.length; i++) {
      var fd = new FormData();
      fd.append("path", self.cwd);
      fd.append("file", list[i]);
      try { await api("/files/upload", { method: "POST", body: fd, timeout: 0 }); ok++; }
      catch (e) { fail++; toast(list[i].name + ": " + e.message, "error", 5000); }
    }
    if (ok) toast("上传完成(" + ok + " 个)" + (fail ? ",失败 " + fail + " 个" : ""));
    self.load(el);
  },
  async batchOp(op, el) {
    var paths = Object.keys(this.sel);
    var self = this;
    var refresh = function() { self.load(el); };
    if (op === "cancel") { this.sel = {}; refresh(); return; }
    if (!paths.length) return;
    if (op === "delete") {
      if (!(await confirmDlg("删除选中的 <b>" + paths.length + "</b> 项?<br><span style='font-size:12px;color:var(--text-3)'>将移入回收站,可随时恢复</span>", { danger: true }))) return;
      for (var i = 0; i < paths.length; i++) {
        try { await api("/files/delete", { method: "POST", body: { path: paths[i] } }); }
        catch (e) { toast(paths[i] + ": " + e.message, "error"); }
      }
      toast("批量删除完成(已移入回收站)");
      this.sel = {}; refresh();
    } else if (op === "compress") {
      var def = "batch_" + Date.now() % 100000 + ".zip";
      var r = await promptDlg("压缩选中项(" + paths.length + " 个)", [{ key: "name", label: "压缩包名", required: true, value: def }]);
      if (!r) return;
      toast("压缩中...", "info");
      try { await api("/files/compress", { method: "POST", body: { dst: this.cwd + "/" + r.name, sources: paths } }); toast("压缩完成"); this.sel = {}; refresh(); }
      catch (e) { toast(e.message, "error"); }
    } else if (op === "copy" || op === "move") {
      var r2 = await promptDlg(op === "copy" ? "复制到" : "移动到", [{ key: "dst", label: "目标目录(绝对路径)", required: true, value: this.cwd }]);
      if (!r2) return;
      for (var j = 0; j < paths.length; j++) {
        try {
          await api("/files/" + op, { method: "POST", body: { src: paths[j], dst: r2.dst + "/" + basename(paths[j]) } });
        } catch (e) { toast(paths[j] + ": " + e.message, "error"); }
      }
      toast(op === "copy" ? "批量复制完成" : "批量移动完成");
      this.sel = {}; refresh();
    } else if (op === "chmod") {
      var r3 = await promptDlg("批量修改权限(" + paths.length + " 项)", [{ key: "mode", label: "权限(八进制,如 644 / 755)", required: true }]);
      if (!r3) return;
      var v = parseInt(r3.mode, 8);
      if (isNaN(v) || v <= 0) { toast("权限格式错误", "error"); return; }
      for (var k = 0; k < paths.length; k++) {
        try { await api("/files/chmod", { method: "POST", body: { path: paths[k], mode: v } }); }
        catch (e) { toast(paths[k] + ": " + e.message, "error"); }
      }
      toast("权限已批量修改");
      refresh();
    }
  },
  /* 图片预览(lightbox) */
  async imagePreview(path) {
    var m = modal({
      title: "图片预览 · " + basename(path), width: 720,
      body: '<div style="text-align:center;background:repeating-conic-gradient(#f0f2f5 0 25%,#fff 0 50%) 0 0/16px 16px;border-radius:10px;padding:10px">' +
        '<div id="fp-preview-box"><div class="boot-loading" style="height:80px"><div class="spinner"></div></div></div></div>' +
        '<div class="help mono" style="margin-top:8px">' + esc(path) + "</div>",
      footer: '<button class="btn btn-plain" data-act="dl">' + I("download") + ' 下载</button><button class="btn btn-primary" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    qs('[data-act="dl"]', m.el).onclick = function() {
      var token = encodeURIComponent(localStorage.getItem(TOKEN_KEY) || "");
      location.href = "/api/v1/files/download?path=" + encodeURIComponent(path) + "&token=" + token;
    };
    try {
      const d = await api("/files/image-preview?path=" + encodeURIComponent(path));
      const box = qs("#fp-preview-box", m.el);
      if (box) box.innerHTML = '<img src="' + d.data + '" style="max-width:100%;max-height:64vh;border-radius:6px">';
    } catch (e) {
      const box = qs("#fp-preview-box", m.el);
      if (box) box.innerHTML = '<div class="help" style="padding:30px">加载失败: ' + esc(e.message) + "</div>";
    }
  },
  async editFile(path, btn) {
    var self = this;
    // 防重复点击:大文件拉取+渲染需要数秒,期间重复点击只会叠加弹窗
    if (self._fmEditing) { toast("文件正在打开中,请稍候...", "info"); return; }
    self._fmEditing = true;
    if (btn) {
      btn.disabled = true;
      btn.dataset.orig = btn.innerHTML;
      btn.innerHTML = '<span class="spinner" style="width:11px;height:11px;border-width:2px;margin:0;border-color:rgba(0,0,0,.12);border-top-color:var(--text-3)"></span> 打开中';
    }
    // 先弹加载态弹窗,内容到位后再填充编辑器
    var m = modal({
      title: "编辑: " + basename(path),
      width: 1080,
      body: '<div class="boot-loading" style="height:320px"><div class="spinner" style="width:34px;height:34px;border-width:3px;border-color:rgba(0,0,0,.1);border-top-color:var(--primary)"></div><div style="margin-top:14px;color:var(--text-3);font-size:13px">正在加载文件内容,大文件可能需要几秒...</div></div>',
      footer: '<span class="fm-editor-path mono" style="margin-right:auto;font-size:11px;color:var(--text-3)">' + esc(path) + "</span>" +
        '<button class="btn btn-plain" data-act="no">取 消</button>',
    });
    qs('[data-act="no"]', m.el).onclick = function() { m.close(); };
    var restoreBtn = function() {
      if (btn) { btn.disabled = false; btn.innerHTML = btn.dataset.orig; }
    };
    var content = "";
    try {
      var d = await api("/files/content?path=" + encodeURIComponent(path), { timeout: 0 });
      content = d.content || "";
    } catch (e) {
      m.close();
      self._fmEditing = false;
      restoreBtn();
      toast(e.message, "error", 6000);
      return;
    }
    var useCM = typeof CodeMirror !== "undefined" && content.length < 2 * 1024 * 1024;
    // 内容到位:替换弹窗主体为编辑器(cm-body = flex 布局,编辑器填满剩余高度)
    var mbody = qs(".modal-body", m.el);
    mbody.classList.add("cm-body");
    mbody.innerHTML =
      '<div class="cm-toolbar">' +
      (useCM
        ? '<button class="btn btn-sm btn-plain" id="cme-save">' + I("edit") + " 保存 <span class=help style=margin:0>Ctrl+S</span></button>" +
          '<button class="btn btn-sm btn-plain" id="cme-history">' + I("clock") + " 历史版本</button>" +
          '<button class="btn btn-sm btn-plain" id="cme-find">查找 <span class=help style=margin:0>Ctrl+F</span></button>' +
          '<button class="btn btn-sm btn-plain" id="cme-fullscreen">全屏</button>' +
          '<span class="cm-fontsize"><button class="btn btn-sm btn-plain" id="cme-fs-dec" title="减小字号">A-</button>' +
          '<button class="btn btn-sm btn-plain" id="cme-fs-inc" title="增大字号">A+</button></span>'
        : "") +
      '<span class="help mono" id="cme-status" style="margin:0 0 0 auto"></span></div>' +
      '<div id="cme-host"><textarea class="textarea mono" id="fm-editor" rows="22" spellcheck="false" style="font-size:12.5px;line-height:1.55">' + esc(content) + "</textarea></div>";
    qs(".modal-footer", m.el).innerHTML =
      '<span class="fm-editor-path mono" style="margin-right:auto;font-size:11px;color:var(--text-3)">' + esc(path) + "</span>" +
      '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="save">' + I("edit") + " 保存</button>";
    var getValue = function() {
      return (useCM && window.__cme) ? window.__cme.getValue() : qs("#fm-editor", m.el).value;
    };
    var doSave = async function() {
      var sb = qs('[data-act="save"]', m.el);
      if (sb) { sb.disabled = true; sb.innerHTML = "保存中..."; }
      try {
        await api("/files/save", { method: "POST", body: { path: path, content: getValue() }, timeout: 0 });
        toast("已保存(旧版本已存入历史)");
        if (useCM && window.__cme) window.__cme.markClean();
      } catch (e) { toast(e.message, "error", 6000); }
      if (sb) { sb.disabled = false; sb.innerHTML = I("edit") + " 保存"; }
    };
    if (useCM) {
      var dark = document.documentElement.getAttribute("data-theme") === "dark";
      var cm = CodeMirror(qs("#cme-host", m.el), {
        value: content,
        mode: cmModeOf(path),
        theme: dark ? "material-darker" : "default",
        lineNumbers: true,
        styleActiveLine: true,
        autoCloseBrackets: true,
        matchBrackets: true,
        indentUnit: 4,
        tabSize: 4,
        lineWrapping: false,
        extraKeys: {
          "Ctrl-S": function() { doSave(); },
          "Cmd-S": function() { doSave(); },
          "Ctrl-F": "findPersistent",
          "Cmd-F": "findPersistent",
          "Ctrl-/": "toggleComment",
          "Cmd-/": "toggleComment",
          "F11": function() { m.el.classList.toggle("modal-max"); setTimeout(function() { cm.refresh(); }, 50); },
          "Esc": function() { if (m.el.classList.contains("modal-max")) { m.el.classList.remove("modal-max"); setTimeout(function() { cm.refresh(); }, 50); } },
        },
      });
      window.__cme = cm;
      qs("#fm-editor", m.el).style.display = "none"; // CM 已替代原生 textarea
      // 字号调节(持久化,A- / A+)
      var cmFs = parseInt(localStorage.getItem("kp_cm_fs") || "13", 10);
      var applyFs = function() {
        cmFs = Math.max(11, Math.min(20, cmFs));
        cm.getWrapperElement().style.fontSize = cmFs + "px";
        cm.refresh();
        localStorage.setItem("kp_cm_fs", String(cmFs));
      };
      applyFs();
      var dec = qs("#cme-fs-dec", m.el), inc = qs("#cme-fs-inc", m.el);
      if (dec) dec.onclick = function() { cmFs -= 1; applyFs(); };
      if (inc) inc.onclick = function() { cmFs += 1; applyFs(); };
      // 未保存改动保护:尝试关闭时若编辑器是脏的,弹确认
      m.setVeto(function() {
        if (cm.isClean()) return false;
        confirmDlg("编辑器有<b>未保存的修改</b>,确定放弃并关闭吗?", { danger: true }).then(function(ok) {
          if (ok) { cm.markClean(); m.doClose(); }
        });
        return true;
      });
      var updStatus = function() {
        var c = cm.getCursor();
        var st = qs("#cme-status", m.el);
        if (st) st.textContent = "行 " + (c.line + 1) + ", 列 " + (c.ch + 1) + " · " + cm.getValue().length + " 字符 · " + (cmModeOf(path) === "nginx" ? "nginx" : String(cm.getOption("mode") || ""));
      };
      updStatus();
      cm.on("cursorActivity", updStatus);
      var sv = qs("#cme-save", m.el);
      if (sv) sv.onclick = function() { doSave(); };
      var fs2 = qs("#cme-fullscreen", m.el);
      if (fs2) fs2.onclick = function() {
        // 复用弹窗最大化(保留工具栏);CM 原生 fixed 全屏会被遮罩 backdrop-filter 破坏,弃用
        m.el.classList.toggle("modal-max");
        setTimeout(function() { cm.refresh(); }, 50);
      };
      var fd2 = qs("#cme-find", m.el);
      if (fd2) fd2.onclick = function() { cm.execCommand("findPersistent"); };
      var histBtn = qs("#cme-history", m.el);
      if (histBtn) histBtn.onclick = function() { self.historyDialog(path, cm); };
    }
    var saveBtn = qs('[data-act="save"]', m.el);
    if (saveBtn) saveBtn.onclick = doSave;
    qs('[data-act="no"]', m.el).onclick = function() { m.close(); };
    self._fmEditing = false;
    restoreBtn();
  },
  /* 历史版本列表 / 查看 / 恢复 */
  async historyDialog(path, cm) {
    var items = [];
    try { items = (await api("/files/history?path=" + encodeURIComponent(path))) || []; }
    catch (e) { toast(e.message, "error"); return; }
    var m = modal({
      title: "历史版本 · " + basename(path),
      width: 640,
      body: items.length
        ? '<div class="table-wrap"><table class="table"><thead><tr><th>保存时间</th><th style="width:110px">大小</th><th style="width:170px">操作</th></tr></thead><tbody>' +
          items.map(function(it) {
            return '<tr><td>' + esc(it.time) + '</td><td>' + fmtBytes(it.size) + "</td>" +
              '<td><button class="btn btn-xs btn-plain" data-hview="' + esc(it.ts) + '">查看</button>' +
              '<button class="btn btn-xs btn-warning" data-hrestore="' + esc(it.ts) + '">恢复此版本</button></td></tr>';
          }).join("") + "</tbody></table></div>" +
          '<div class="help" style="margin-top:8px">恢复前会自动把当前内容存入历史,不会丢失改动。</div>'
        : emptyState("clock", "暂无历史版本", "每次保存都会自动留存上一版"),
      footer: '<button class="btn btn-plain" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    qsa("[data-hview]", m.el).forEach(function(b) {
      b.onclick = async function() {
        try {
          var d = await api("/files/history/content?path=" + encodeURIComponent(path) + "&ts=" + encodeURIComponent(b.getAttribute("data-hview")));
          window.open("", "_blank").document.write("<pre style=\"white-space:pre-wrap;font-size:12px\">" + d.content.replace(/&/g, "&amp;").replace(/</g, "&lt;") + "</pre>");
        } catch (e) { toast(e.message, "error"); }
      };
    });
    qsa("[data-hrestore]", m.el).forEach(function(b) {
      b.onclick = async function() {
        var ts = b.getAttribute("data-hrestore");
        if (!(await confirmDlg("确定恢复到该版本吗?当前内容会先自动存入历史。"))) return;
        try {
          await api("/files/history/restore", { method: "POST", body: { path: path, ts: ts } });
          toast("已恢复");
          m.close();
          if (cm) {
            var d = await api("/files/content?path=" + encodeURIComponent(path));
            cm.setValue(d.content || "");
          }
        } catch (e) { toast(e.message, "error"); }
      };
    });
  },
  /* 回收站弹窗 */
  async trashDialog(el) {
    var m = modal({
      title: "回收站", width: 720,
      body: '<div id="trash-body">' + skeletonTable(4, 4) + "</div>",
      footer: '<button class="btn btn-danger-plain btn-sm" data-act="clear">' + I("trash") + ' 清空回收站</button><button class="btn btn-plain" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    var self = this;
    var load = async function() {
      var box = qs("#trash-body", m.el);
      var items = [];
      try { items = (await api("/files/trash")) || []; }
      catch (e) { box.innerHTML = emptyState("warn", "加载失败", e.message); return; }
      if (!items.length) {
        box.innerHTML = emptyState("trash", "回收站是空的", "删除的文件会先移入这里,可随时恢复");
        return;
      }
      box.innerHTML = '<div class="table-wrap"><table class="table trash-table"><thead><tr><th>名称</th><th>原位置</th><th style="width:90px">大小</th><th style="width:150px">删除时间</th><th style="width:130px">操作</th></tr></thead><tbody>' +
        items.map(function(it) {
          return '<tr><td>' + (it.is_dir ? I("folder", "fm-ico") : I("file", "fm-ico")) + " <b>" + esc(it.name) + "</b></td>" +
            '<td class="mono fm-ellipsis" title="' + esc(it.orig_path) + '">' + esc(it.orig_path) + "</td>" +
            "<td>" + (it.is_dir ? "-" : fmtBytes(it.size)) + "</td>" +
            "<td>" + fmTime(it.deleted_at) + "</td>" +
            '<td><button class="btn btn-xs btn-plain" data-trash-restore="' + esc(it.id) + '">恢复</button>' +
            '<button class="btn btn-xs btn-danger-plain" data-trash-del="' + esc(it.id) + '" data-name="' + esc(it.name) + '">彻底删除</button></td></tr>';
        }).join("") + "</tbody></table></div>";
      qsa("[data-trash-restore]", box).forEach(function(b) {
        b.onclick = async function() {
          try {
            await api("/files/trash/restore", { method: "POST", body: { id: b.getAttribute("data-trash-restore") } });
            toast("已恢复到原位置");
            load();
          } catch (e) { toast(e.message, "error"); }
        };
      });
      qsa("[data-trash-del]", box).forEach(function(b) {
        b.onclick = async function() {
          if (!(await confirmDlg("彻底删除 <b>" + esc(b.getAttribute("data-name")) + "</b>?此操作不可恢复!", { danger: true }))) return;
          try {
            await api("/files/trash/delete", { method: "POST", body: { id: b.getAttribute("data-trash-del") } });
            toast("已彻底删除");
            load();
          } catch (e) { toast(e.message, "error"); }
        };
      });
    };
    load();
    qs('[data-act="clear"]', m.el).onclick = async function() {
      if (!(await confirmDlg("确定<b>清空回收站</b>吗?所有文件将被彻底删除,不可恢复!", { danger: true }))) return;
      try {
        await api("/files/trash/clear", { method: "POST", body: {} });
        toast("回收站已清空");
        load();
      } catch (e) { toast(e.message, "error"); }
    };
  },
};

/* ================= 网络工具箱 ================= */
PAGES.nettools = {
  title: "网络工具",
  tab: "ping",
  busy: false,
  async render(el) {
    var self = this;
    var tabs = [["ping", "Ping"], ["trace", "路由追踪"], ["dns", "DNS 解析"], ["portscan", "端口扫描"], ["http", "HTTP 检测"]];
    el.innerHTML = '<div class="card"><div class="card-title">网络工具箱</div>' +
      '<div class="tabs">' + tabs.map(function(t) {
        return '<div class="tab ' + (self.tab === t[0] ? "active" : "") + '" data-tab="' + t[0] + '">' + t[1] + "</div>";
      }).join("") + "</div>" +
      '<div id="nt-body"></div></div>';
    qsa(".tab", el).forEach(function(t) {
      t.onclick = function() { self.tab = t.getAttribute("data-tab"); self.render(el); };
    });
    this.renderTab(qs("#nt-body", el));
  },
  renderTab(box) {
    var self = this;
    var hostPh = { ping: "如 1.1.1.1 或 example.com", trace: "如 example.com", dns: "如 example.com", portscan: "如 127.0.0.1", http: "如 http://example.com" }[this.tab] || "";
    var btnText = { ping: "开始 Ping", trace: "开始追踪", dns: "开始解析", portscan: "开始扫描", http: "发起请求" }[this.tab] || "执行";
    box.innerHTML = '<div class="nt-form">' +
      '<input class="input mono" id="nt-host" placeholder="' + esc(hostPh) + '" style="flex:1;min-width:220px">' +
      '<button class="btn btn-primary" id="nt-run">' + I("bolt") + " " + btnText + "</button></div>" +
      '<div id="nt-result" class="nt-result">' + emptyState("netUp", "输入目标后点击执行", "Ping / 追踪最多 30 秒") + "</div>";
    var input = qs("#nt-host", box);
    input.focus();
    var run = async function() {
      if (self.busy) { toast("正在执行中,请稍候", "info"); return; }
      var target = (input.value || "").trim();
      if (!target) { toast("请输入目标地址", "error"); return; }
      var res = qs("#nt-result", box);
      self.busy = true;
      var btn = qs("#nt-run", box);
      btn.disabled = true;
      res.innerHTML = '<div class="nt-loading">' + skeletonBlock(14, "70%") + skeletonBlock(14, "50%") + skeletonBlock(14, "60%") + "</div>";
      var ep = { ping: "/nettools/ping", trace: "/nettools/trace", dns: "/nettools/dns", portscan: "/nettools/portscan", http: "/nettools/http" }[self.tab];
      try {
        var d = await api(ep, { method: "POST", body: { host: target, url: target }, timeout: self.tab === "trace" ? 60000 : 30000 });
        self.renderResult(res, d);
      } catch (e) {
        res.innerHTML = emptyState("warn", "执行失败", e.message);
      }
      self.busy = false;
      btn.disabled = false;
    };
    qs("#nt-run", box).onclick = run;
    input.onkeydown = function(e) { if (e.key === "Enter") run(); };
  },
  renderResult(res, d) {
    if (this.tab === "ping" || this.tab === "trace") {
      var okTag = d.success === false
        ? '<span class="tag tag-danger">不通</span>'
        : d.success === true ? '<span class="tag tag-success">连通</span>' : "";
      res.innerHTML = '<div class="nt-head"><b class="mono">' + esc(d.target) + "</b>" + okTag + "</div>" +
        '<pre class="log-view mono" style="max-height:420px">' + esc(d.output || "(无输出)") + "</pre>";
    } else if (this.tab === "dns") {
      var recs = d.records || [];
      res.innerHTML = '<div class="nt-head"><b class="mono">' + esc(d.target) + "</b>" +
        '<span class="tag tag-info">' + recs.length + " 条记录</span></div>" +
        (recs.length
          ? '<div class="table-wrap"><table class="table"><thead><tr><th style="width:80px">#</th><th>解析结果</th></tr></thead><tbody>' +
            recs.map(function(r, i) { return '<tr><td>' + (i + 1) + '</td><td class="mono">' + esc(r) + "</td></tr>"; }).join("") +
            "</tbody></table></div>"
          : emptyState("info", "没有解析记录"));
    } else if (this.tab === "portscan") {
      var ports = d.ports || [];
      res.innerHTML = '<div class="nt-head"><b class="mono">' + esc(d.target) + "</b>" +
        '<span class="tag ' + (ports.length ? "tag-warning" : "tag-info") + '">开放 ' + ports.length + " 个端口</span></div>" +
        (ports.length
          ? '<div class="table-wrap"><table class="table"><thead><tr><th style="width:100px">端口</th><th style="width:100px">状态</th><th>服务</th></tr></thead><tbody>' +
            ports.map(function(p) {
              return '<tr><td class="mono"><b>' + p.port + "</b></td>" +
                '<td><span class="tag tag-success">开放</span></td><td>' + esc(p.name || "Unknown") + "</td></tr>";
            }).join("") + "</tbody></table></div>"
          : emptyState("shield", "未发现开放端口", "仅扫描 23 个常用端口"));
    } else if (this.tab === "http") {
      var sc = d.status_code || 0;
      var scCls = sc >= 200 && sc < 400 ? "tag-success" : "tag-danger";
      var hdrs = d.headers || {};
      var hdrRows = Object.keys(hdrs).map(function(k) {
        return '<tr><td class="mono" style="width:180px">' + esc(k) + '</td><td class="mono">' + esc(hdrs[k]) + "</td></tr>";
      }).join("");
      res.innerHTML = '<div class="nt-head"><b class="mono">' + esc(d.url) + "</b>" +
        '<span class="tag ' + scCls + '">HTTP ' + sc + "</span>" +
        '<span class="tag tag-info">' + (d.time_ms || 0) + " ms</span></div>" +
        (d.redirect_url ? '<div class="nt-note">重定向至: <span class="mono">' + esc(d.redirect_url) + "</span></div>" : "") +
        (hdrRows
          ? '<div class="table-wrap"><table class="table"><thead><tr><th>响应头</th><th>值</th></tr></thead><tbody>' + hdrRows + "</tbody></table></div>"
          : "");
    }
  },
};

/* ================= 进程管理 ================= */
PAGES.processes = {
  title: "进程管理",
  sort: "cpu",
  kw: "",
  auto: true,
  async render(el) {
    var self = this;
    el.innerHTML = '<div class="card"><div class="card-title">进程管理' +
      '<div style="display:flex;gap:8px;align-items:center">' +
      '<label class="fm-switch-label"><input type="checkbox" id="pm-auto" checked> 自动刷新</label>' +
      '<button class="btn btn-sm btn-plain" id="pm-refresh">' + I("refresh") + " 刷新</button></div></div>" +
      '<div class="svc-toolbar"><div class="svc-search">' + I("search") +
      '<input id="pm-search" placeholder="搜索进程名 / 命令行..." value="' + esc(this.kw || "") + '"></div>' +
      '<div class="pm-sort">' +
      '<button class="btn btn-sm ' + (this.sort === "cpu" ? "btn-primary" : "btn-plain") + '" data-sort="cpu">按 CPU</button>' +
      '<button class="btn btn-sm ' + (this.sort === "mem" ? "btn-primary" : "btn-plain") + '" data-sort="mem">按内存</button>' +
      '<button class="btn btn-sm ' + (this.sort === "pid" ? "btn-primary" : "btn-plain") + '" data-sort="pid">按 PID</button>' +
      "</div></div>" +
      '<div id="pm-body">' + skeletonTable(12, 6) + "</div></div>";
    qs("#pm-refresh", el).onclick = function() { self.fetch(el); };
    qs("#pm-auto", el).onchange = function() { self.auto = this.checked; };
    qs("#pm-search", el).oninput = debounce(function(e) {
      self.kw = e.target.value.trim().toLowerCase();
      self.draw(qs("#pm-body", el));
    }, 300);
    qsa("[data-sort]", el).forEach(function(b) {
      b.onclick = function() {
        self.sort = b.getAttribute("data-sort");
        qsa("[data-sort]", el).forEach(function(x) { x.className = "btn btn-sm " + (x.getAttribute("data-sort") === self.sort ? "btn-primary" : "btn-plain"); });
        self.fetch(el);
      };
    });
    setPageTimer(function() { if (self.auto && document.visibilityState === "visible") self.fetch(el, true); }, 5000);
    await this.fetch(el);
  },
  async fetch(el, silent) {
    var body = qs("#pm-body", el);
    if (!body) return;
    var self = this;
    var list;
    try { list = await api("/processes?sort=" + this.sort + "&limit=300"); }
    catch (e) {
      if (!silent) body.innerHTML = emptyState("warn", "加载失败", e.message);
      return;
    }
    if (!qs("#pm-body", el)) return; // 页面已切换
    this.all = list || [];
    this.draw(body);
  },
  draw(body) {
    var rows = this.all || [];
    if (rows.length && typeof rows[0] === "object" && !Array.isArray(rows[0])) { /* ok */ }
    if (Array.isArray(rows)) { /* api returns array directly */ }
    else rows = [];
    var kw = this.kw || "";
    if (kw) {
      rows = rows.filter(function(p) {
        return (p.name || "").toLowerCase().indexOf(kw) >= 0 || (p.cmdline || "").toLowerCase().indexOf(kw) >= 0 || String(p.pid).indexOf(kw) >= 0;
      });
    }
    if (!rows.length) {
      body.innerHTML = emptyState("search", kw ? "未找到匹配进程" : "没有进程数据");
      return;
    }
    var html = '<div class="pm-meta"><span class="tag tag-info">共 ' + rows.length + " 个进程</span></div>";
    html += '<div class="table-wrap"><table class="table"><thead><tr>' +
      "<th>PID</th><th>名称</th><th>用户</th><th style=\"width:120px\">CPU</th><th style=\"width:150px\">内存</th><th style=\"width:90px\">线程</th><th style=\"width:160px\">操作</th></tr></thead><tbody>";
    var self2 = this;
    rows.forEach(function(p) {
      var cpuCls = p.cpu_percent >= 50 ? "pm-val hot" : p.cpu_percent >= 15 ? "pm-val warm" : "pm-val";
      html += '<tr><td class="mono">' + p.pid + "</td>" +
        '<td class="pm-name" title="' + esc(p.cmdline || p.name) + '"><b>' + esc(p.name || "-") + "</b></td>" +
        "<td>" + esc(p.user || "-") + "</td>" +
        '<td><div class="pm-bar"><div class="pm-bar-in" style="width:' + Math.min(100, p.cpu_percent || 0) + '%"></div></div><span class="' + cpuCls + '">' + (p.cpu_percent || 0).toFixed(1) + "%</span></td>" +
        '<td><div class="pm-bar"><div class="pm-bar-in mem" style="width:' + Math.min(100, p.mem_percent || 0) + '%"></div></div><span class="pm-val">' + fmtBytes(p.mem_rss || 0) + "</span></td>" +
        "<td>" + (p.threads || 0) + "</td>" +
        '<td><button class="btn btn-xs btn-danger-plain" data-pm-kill="' + p.pid + '" data-name="' + esc(p.name || "") + '">结束</button>' +
        '<button class="btn btn-xs btn-plain" data-pm-kill9="' + p.pid + '" data-name="' + esc(p.name || "") + '">强制</button></td></tr>';
    });
    html += "</tbody></table></div>";
    body.innerHTML = html;
    qsa("[data-pm-kill], [data-pm-kill9]", body).forEach(function(b) {
      b.onclick = async function() {
        var force = b.hasAttribute("data-pm-kill9");
        var pid = parseInt(b.getAttribute(force ? "data-pm-kill9" : "data-pm-kill"), 10);
        var name = b.getAttribute("data-name");
        if (!(await confirmDlg("确定结束进程 <b>" + esc(name) + "</b> (PID " + pid + ")" + (force ? "(SIGKILL 强制)" : "(SIGTERM)") + " 吗?", { danger: true }))) return;
        try {
          await api("/processes/kill", { method: "POST", body: { pid: pid, force: force } });
          toast("进程 " + pid + " 已终止");
          self2.fetch(qs("#content"), true);
        } catch (e) { toast(e.message, "error"); }
      };
    });
  },
};

/* ================= 系统防火墙 ================= */
PAGES.firewall = {
  title: "系统防火墙",
  async render(el) {
    el.innerHTML = '<div class="card"><div class="card-title">系统防火墙' +
      '<button class="btn btn-sm btn-plain" id="fw-refresh">' + I("refresh") + " 刷新</button></div>" +
      '<div class="help" style="margin:-6px 0 12px;padding:8px 12px;background:var(--warning-light,#fff7e6);border:1px solid var(--warning,#fbbf24);border-radius:8px">⚠ Docker 发布的端口(应用商店应用、容器 -p 映射)不经过本页规则,直接在 iptables DOCKER 链放行——如需限制外部访问,请在应用的 compose 中改绑 127.0.0.1 或使用宿主防火墙工具直接操作 DOCKER 链</div>' +
      '<div id="fw-body">' + skeletonTable(6, 4) + "</div></div>";
    var self = this;
    qs("#fw-refresh", el).onclick = function() { self.fetch(el); };
    await this.fetch(el);
  },
  async fetch(el) {
    var body = qs("#fw-body", el);
    if (!body) return;
    var self = this;
    var st;
    try { st = await api("/firewall"); }
    catch (e) { body.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    var kindName = { ufw: "UFW", firewalld: "Firewalld", none: "未检测到" }[st.kind] || st.kind;
    var html = '<div class="fw-status">' +
      '<div class="fw-status-item"><span class="fw-label">防火墙类型</span><b>' + esc(kindName) + "</b></div>" +
      '<div class="fw-status-item"><span class="fw-label">状态</span>' + stateTag(st.active, "已启用", "未启用") + "</div>" +
      (st.kind !== "none"
        ? '<div class="fw-status-item"><button class="btn btn-sm ' + (st.active ? "btn-danger-plain" : "btn-primary") + '" id="fw-toggle">' +
          (st.active ? "关闭防火墙" : "启用防火墙") + "</button></div>"
        : '<div class="fw-status-item"><span class="help">未检测到防火墙,一键安装:</span>' +
          '<button class="btn btn-sm btn-primary" id="fw-install-ufw">安装 UFW</button>' +
          '<button class="btn btn-sm btn-plain" id="fw-install-fwd">安装 Firewalld</button></div>') +
      "</div>";
    if (st.kind === "none") {
      html += '<div class="help" style="margin:-6px 0 14px">UFW 适合 Debian/Ubuntu;Firewalld 适合 CentOS/RHEL/Rocky。安装完成后刷新本页即可管理端口。</div>';
    }
    if (st.kind !== "none") {
      html += '<div class="fw-add"><input class="input mono" id="fw-port" placeholder="端口,如 80 / 443 / 4000:4010 / 53/udp" style="width:280px">' +
        '<input class="input" id="fw-comment" placeholder="备注(可选)" style="width:200px">' +
        '<button class="btn btn-primary" id="fw-allow">' + I("plus") + " 放行端口</button></div>" +
        '<div class="fw-add" style="margin-top:-8px"><span class="help" style="margin:0">常用:</span>' +
        [["80/tcp", "HTTP"], ["443/tcp", "HTTPS"], ["22/tcp", "SSH"], ["3306/tcp", "MySQL"], ["6379/tcp", "Redis"], ["8080/tcp", "8080"]].map(function(q) {
          return '<span class="tag tag-info" style="cursor:pointer" data-fw-quick="' + q[0] + '">' + q[1] + " " + q[0] + "</span>";
        }).join("") + "</div>";
      html += '<div class="table-wrap"><table class="table"><thead><tr>' +
        "<th>端口 / 规则</th><th style=\"width:90px\">协议</th><th style=\"width:100px\">动作</th><th style=\"width:150px\">来源</th><th style=\"width:100px\">操作</th></tr></thead><tbody>";
      var rules = st.rules || [];
      if (!rules.length) {
        html += '<tr><td colspan="5" style="text-align:center;padding:26px;color:var(--text-3)">暂无自定义规则</td></tr>';
      }
      rules.forEach(function(r) {
        var isSvc = String(r.port || "").indexOf("svc:") === 0;
        var actionTag = r.action === "deny"
          ? '<span class="tag tag-danger">拒绝</span>'
          : '<span class="tag tag-success">放行</span>';
        html += "<tr><td><b>" + esc(r.port) + "</b>" + (r.comment ? ' <span class="help">' + esc(r.comment) + "</span>" : "") + "</td>" +
          "<td>" + esc(r.proto ? r.proto.toUpperCase() : "ANY") + "</td>" +
          "<td>" + actionTag + "</td><td>" + esc(r.from || "Anywhere") + "</td>" +
          "<td>" + (isSvc
            ? '<span class="help">系统服务</span>'
            : '<button class="btn btn-xs btn-danger-plain" data-fw-del="' + esc(r.port) + '">移除</button>') + "</td></tr>";
      });
      html += "</tbody></table></div>";
    }

    // ping(ICMP)开关
    html += '<div class="fw-add" style="margin-top:4px;align-items:center">' +
      '<span class="help" style="margin:0">ICMP Ping 响应:</span><span id="fw-ping-state" class="help" style="margin:0">检测中…</span>' +
      '<button class="btn btn-sm btn-plain" id="fw-ping-toggle">切换</button>' +
      '<span class="help" style="margin:0">禁止 ping 可降低被扫描发现的概率(不影响正常服务;部分机房边缘设备会代答 ICMP,表现为外部仍可 ping 但内核已不响应)</span></div>';

    // 容器端口管控(DOCKER-USER)
    html += '<div id="fw-docker-section"></div>';
    body.innerHTML = html;

    // ping 状态加载与切换
    (async function() {
      try {
        var d = await api("/firewall/ping");
        var el2 = qs("#fw-ping-state", body);
        if (el2) el2.innerHTML = d.ping ? '<b style="color:var(--success,#16a34a)">允许</b>' : '<b style="color:var(--danger)">已禁止</b>';
        var btn = qs("#fw-ping-toggle", body);
        if (btn) btn.onclick = async function() {
          try {
            var cur = (await api("/firewall/ping")).ping;
            var nd = await api("/firewall/ping", { method: "POST", body: { on: !cur } });
            toast(nd.ping ? "已允许 ping" : "已禁止 ping");
            btn.previousElementSibling && (btn.previousElementSibling.innerHTML = nd.ping ? '<b style="color:var(--success,#16a34a)">允许</b>' : '<b style="color:var(--danger)">已禁止</b>');
          } catch (e) { toast(e.message, "error"); }
        };
      } catch (e) {}
    })();

    // 容器端口管控(DOCKER-USER)
    (async function() {
      var sec = qs("#fw-docker-section", body);
      if (!sec) return;
      var dp;
      try { dp = await api("/firewall/docker-ports"); } catch (e) { sec.innerHTML = ""; return; }
      if (!dp.available) { sec.innerHTML = ""; return; }
      var h = '<div class="card" style="margin-top:14px"><div class="card-title"><span>容器端口管控</span><span class="help" style="margin:0">Docker 端口绕过系统防火墙,此处经 DOCKER-USER 链真正拦截外部访问;本机/内网访问不受影响;规则在面板重启后自动重放</span></div>';
      h += '<div class="table-wrap"><table class="table"><thead><tr><th>容器</th><th>宿主机端口</th><th>协议</th><th style="width:110px">公网访问</th><th style="width:100px">操作</th></tr></thead><tbody>';
      if (!dp.ports || !dp.ports.length) {
        h += '<tr><td colspan="5" style="text-align:center;padding:26px;color:var(--text-3)">暂无映射到宿主机的容器端口</td></tr>';
      }
      (dp.ports || []).forEach(function(x) {
        h += "<tr><td><b>" + esc(x.container) + "</b></td><td class=\"mono\">" + esc(x.host_port) + "</td><td>" + esc(x.proto.toUpperCase()) + "</td>" +
          '<td>' + (x.blocked
            ? '<span class="tag tag-danger">已封禁</span>'
            : '<span class="tag tag-success">开放</span>') + "</td>" +
          '<td><button class="btn btn-xs ' + (x.blocked ? "btn-plain" : "btn-danger-plain") + '" data-dfw="' + esc(x.host_port) + '" data-proto="' + esc(x.proto) + '" data-block="' + (!x.blocked) + '">' +
          (x.blocked ? "解封" : "封禁公网") + "</button></td></tr>";
      });
      h += "</tbody></table></div></div>";
      sec.innerHTML = h;
      qsa("[data-dfw]", sec).forEach(function(b) {
        b.onclick = async function() {
          var port = b.getAttribute("data-dfw"), proto = b.getAttribute("data-proto");
          var block = b.getAttribute("data-block") === "true";
          if (block && !(await confirmDlg("确定<b>封禁</b>端口 " + esc(port) + " 的公网访问吗?<br><span style='font-size:12px;color:var(--text-3)'>本机与内网访问不受影响;规则持久化,面板重启自动重放。</span>", { danger: true }))) return;
          try {
            await api("/firewall/docker-block", { method: "POST", body: { port: port, proto: proto, block: block } });
            toast(block ? "端口 " + port + " 已封禁公网访问" : "端口 " + port + " 已解封");
            self.fetch(el);
          } catch (e) { toast(e.message, "error"); }
        };
      });
    })();
    var doAllow = async function(port, comment) {
      if (!port) { toast("请输入端口", "error"); return; }
      try {
        await api("/firewall/allow", { method: "POST", body: { port: port, comment: comment } });
        toast("端口 " + port + " 已放行");
        self.fetch(el);
      } catch (e) { toast(e.message, "error"); }
    };
    var allowBtn = qs("#fw-allow", body);
    if (allowBtn) allowBtn.onclick = async function() {
      await doAllow((qs("#fw-port", body).value || "").trim(), (qs("#fw-comment", body).value || "").trim());
    };
    qsa("[data-fw-quick]", body).forEach(function(q) {
      q.onclick = function() { doAllow(q.getAttribute("data-fw-quick"), "常用端口"); };
    });
    qsa("[data-fw-del]", body).forEach(function(b) {
      b.onclick = async function() {
        var port = b.getAttribute("data-fw-del");
        if (!(await confirmDlg("确定移除端口 <b>" + esc(port) + "</b> 的放行规则吗?", { danger: true }))) return;
        try {
          await api("/firewall/deny", { method: "POST", body: { port: port } });
          toast("规则已移除");
          self.fetch(el);
        } catch (e) { toast(e.message, "error"); }
      };
    });
    var tg = qs("#fw-toggle", body);
    if (tg) tg.onclick = async function() {
      var on = !st.active;
      if (!(await confirmDlg(on
        ? "确定<b>启用</b>防火墙吗?<br><span style='font-size:12px;color:var(--text-3)'>将自动放行:SSH 端口、面板端口、Web(80/443)。其余端口默认拒绝,更安全。</span>"
        : "确定<b>关闭</b>防火墙吗?服务器所有端口将直接暴露,存在安全风险!", { danger: !on }))) return;
      try {
        await api("/firewall/toggle", { method: "POST", body: { on: on } });
        toast(on ? "防火墙已启用" : "防火墙已关闭");
        self.fetch(el);
      } catch (e) { toast(e.message, "error"); }
    };
    // 一键安装(UFW / Firewalld)
    var instU = qs("#fw-install-ufw", body), instF = qs("#fw-install-fwd", body);
    var doInstall = function(kind, btn) {
      btn.disabled = true;
      api("/firewall/install", { method: "POST", body: { kind: kind } }).then(function(d) {
        taskModal(d.task_id, function(status) {
          if (status === "success") toast("防火墙安装完成");
          self.fetch(el);
        });
      }).catch(function(e) { toast(e.message, "error"); btn.disabled = false; });
    };
    if (instU) instU.onclick = function() { doInstall("ufw", instU); };
    if (instF) instF.onclick = function() { doInstall("firewalld", instF); };
  },
};

/* ================= 拨测监控 ================= */
PAGES.upmon = {
  title: "拨测监控",
  timer: null,
  async render(el) {
    var self = this;
    el.innerHTML = '<div class="card"><div class="card-title">网站拨测' +
      '<button class="btn btn-sm btn-primary" id="up-add">' + I("plus") + ' 添加监控</button></div>' +
      '<div class="help" style="margin-bottom:14px">每分钟探测一次,连续失败 2 次判定宕机,恢复或宕机时通过告警渠道(Webhook/邮件)通知;保留 24 小时历史。</div>' +
      '<div id="up-body">' + skeletonTable(3, 4) + "</div></div>";
    qs("#up-add", el).onclick = function() { self.addDialog(el); };
    var draw = async function() {
      var body = qs("#up-body", el);
      if (!body) return;
      var list;
      try { list = (await api("/uptime/monitors")) || []; }
      catch (e) { body.innerHTML = emptyState("warn", "加载失败", e.message); return; }
      if (!qs("#up-body", el)) return;
      if (!list.length) {
        body.innerHTML = emptyState("pulse", "还没有监控目标", "添加网站 URL 或服务端口,掌握可用性状态");
        return;
      }
      var html = '<div class="up-grid">';
      list.forEach(function(m) {
        var upTag = !m.enabled
          ? '<span class="tag tag-info">已停用</span>'
          : m.up ? '<span class="tag tag-success"><span class="dot on"></span>正常</span>'
                 : '<span class="tag tag-danger"><span class="dot"></span>宕机</span>';
        var bars = "";
        var hist = m.history || [];
        var step = Math.max(1, Math.floor(hist.length / 60));
        var shown = [];
        for (var i = hist.length - 1; i >= 0; i -= step) { shown.unshift(hist[i]); if (shown.length >= 60) break; }
        shown.forEach(function(s) {
          bars += '<div class="up-bar ' + (s.up ? "ok" : "fail") + '" title="' + fmTime(new Date(s.ts * 1000)) + (s.err ? " · " + esc(s.err) : "") + '"></div>';
        });
        if (!bars) bars = '<div class="help">等待首次探测...</div>';
        html += '<div class="card up-card" style="margin:0">' +
          '<div class="up-head"><b>' + esc(m.name) + '</b>' + upTag + '</div>' +
          '<div class="mono up-target" title="' + esc(m.target) + '">' + esc(m.target) + "</div>" +
          '<div class="up-stats"><span>可用率 <b>' + (m.enabled ? (m.uptime_pct || 0).toFixed(1) : "-") + '%</b></span>' +
          "<span>平均延迟 <b>" + (m.avg_ms || 0) + ' ms</b></span>' +
          "<span>最近 <b>" + (m.last_check || "-") + "</b></span></div>" +
          '<div class="up-bars">' + bars + "</div>" +
          '<div class="up-ops"><button class="btn btn-xs btn-plain" data-up-toggle="' + esc(m.id) + '">' + (m.enabled ? "停用" : "启用") + "</button>" +
          '<button class="btn btn-xs btn-danger-plain" data-up-del="' + esc(m.id) + '" data-name="' + esc(m.name) + '">删除</button></div>' +
          "</div>";
      });
      html += "</div>";
      body.innerHTML = html;
      qsa("[data-up-toggle]", body).forEach(function(b) {
        b.onclick = async function() {
          var id = b.getAttribute("data-up-toggle");
          var cur = list.find(function(x) { return x.id === id; });
          try {
            await api("/uptime/monitors/" + id + "/toggle", { method: "POST", body: { enabled: !(cur && cur.enabled) } });
            toast("已更新");
            draw();
          } catch (e) { toast(e.message, "error"); }
        };
      });
      qsa("[data-up-del]", body).forEach(function(b) {
        b.onclick = async function() {
          if (!(await confirmDlg("删除监控 <b>" + esc(b.getAttribute("data-name")) + "</b>?", { danger: true }))) return;
          try { await api("/uptime/monitors/" + b.getAttribute("data-up-del"), { method: "DELETE" }); toast("已删除"); draw(); }
          catch (e) { toast(e.message, "error"); }
        };
      });
    };
    await draw();
    setPageTimer(function() { if (document.visibilityState === "visible") draw(); }, 60000);
  },
  addDialog(el) {
    var m = modal({
      title: "添加拨测监控", width: 520,
      body: '<div class="form-item required"><label>名称</label><input class="input" id="up-name" placeholder="如 官网首页"></div>' +
        '<div class="form-item"><label>类型</label>' +
        '<label class="radio-line"><input type="radio" name="up-kind" value="http" checked> HTTP(S) 网址</label>' +
        '<label class="radio-line"><input type="radio" name="up-kind" value="tcp"> TCP 端口</label></div>' +
        '<div class="form-item required"><label>目标</label><input class="input" id="up-target" placeholder="http://example.com 或 TCP: 1.2.3.4:3306">' +
        '<div class="help">HTTP 状态码低于 500 即视为存活(含 401/403);TCP 探测端口连通性</div></div>',
      footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">添 加</button>',
    });
    qs('[data-act="no"]', m.el).onclick = m.close;
    qs('[data-act="yes"]', m.el).onclick = async function() {
      var kind = (qsa('input[name="up-kind"]', m.el).find(function(r) { return r.checked; }) || {}).value || "http";
      try {
        await api("/uptime/monitors", { method: "POST", body: {
          name: qs("#up-name", m.el).value.trim(),
          kind: kind,
          target: qs("#up-target", m.el).value.trim(),
        }});
        toast("已添加,正在首次探测");
        m.close();
        PAGES.upmon.render(el);
      } catch (e) { toast(e.message, "error"); }
    };
  },
};

/* ================= 安全中心(WAF / 系统防火墙 / SSH / 日志) ================= */
PAGES.securityhub = {
  title: "安全",
  tab: "waf",
  tabsDef: [
    { key: "waf", label: "WAF 防火墙", page: "plugins" },
    { key: "firewall", label: "系统防火墙", page: "firewall" },
    { key: "ssh", label: "SSH 安全", page: "ssh" },
    { key: "logs", label: "日志", page: "logs" },
  ],
  async render(el) {
    clearPageTimers();
    var self = this;
    // 旧深链兼容:#/firewall、#/logs、#/plugins 直接落到对应页签
    var cur = (location.hash || "").split("?")[0].replace(/^#\//, "");
    for (var i = 0; i < this.tabsDef.length; i++) {
      if (this.tabsDef[i].page === cur) this.tab = this.tabsDef[i].key;
    }
    var html = '<div class="card"><div class="card-title">安全中心</div>' +
      '<div class="hub-tabs">';
    this.tabsDef.forEach(function(t) {
      html += '<div class="hub-tab ' + (self.tab === t.key ? "active" : "") + '" data-tab="' + t.key + '">' + t.label + "</div>";
    });
    html += '</div><div id="hub-body">' + skeletonTable(5, 4) + "</div></div>";
    el.innerHTML = html;
    var body = qs("#hub-body", el);
    qsa(".hub-tab", el).forEach(function(t) {
      t.onclick = function() {
        self.tab = t.getAttribute("data-tab");
        qsa(".hub-tab", el).forEach(function(x) { x.classList.remove("active"); });
        t.classList.add("active");
        clearPageTimers();
        self.renderTab(qs("#hub-body", el));
      };
    });
    this.renderTab(body);
  },
  renderTab(body) {
    var def = this.tabsDef.find(function(t) { return t.key === this.tab; }, this) || this.tabsDef[0];
    var page = PAGES[def.page];
    if (page && page.render) page.render(body);
    else body.innerHTML = emptyState("warn", "页面缺失");
  },
};

/* ================= 监控中心(拨测 / 进程 / 服务 / 网络工具) ================= */
PAGES.monitorhub = {
  title: "监控",
  tab: "upmon",
  tabsDef: [
    { key: "upmon", label: "拨测监控", page: "upmon" },
    { key: "processes", label: "进程管理", page: "processes" },
    { key: "services", label: "系统服务", page: "services" },
    { key: "nettools", label: "网络工具", page: "nettools" },
  ],
  async render(el) {
    clearPageTimers();
    var self = this;
    var cur = (location.hash || "").split("?")[0].replace(/^#\//, "");
    for (var i = 0; i < this.tabsDef.length; i++) {
      if (this.tabsDef[i].page === cur) this.tab = this.tabsDef[i].key;
    }
    var html = '<div class="card"><div class="card-title">监控中心</div>' +
      '<div class="hub-tabs">';
    this.tabsDef.forEach(function(t) {
      html += '<div class="hub-tab ' + (self.tab === t.key ? "active" : "") + '" data-tab="' + t.key + '">' + t.label + "</div>";
    });
    html += '</div><div id="hub-body">' + skeletonTable(5, 4) + "</div></div>";
    el.innerHTML = html;
    qsa(".hub-tab", el).forEach(function(t) {
      t.onclick = function() {
        self.tab = t.getAttribute("data-tab");
        qsa(".hub-tab", el).forEach(function(x) { x.classList.remove("active"); });
        t.classList.add("active");
        clearPageTimers();
        self.renderTab(qs("#hub-body", el));
      };
    });
    this.renderTab(qs("#hub-body", el));
  },
  renderTab(body) {
    var self = this;
    var def = this.tabsDef.find(function(t) { return t.key === self.tab; }) || this.tabsDef[0];
    var page = PAGES[def.page];
    if (page && page.render) page.render(body);
    else body.innerHTML = emptyState("warn", "页面缺失");
  },
};

/* ================= 容器(Docker) ================= */
PAGES.docker = {
  title: "容器",
  tab: "containers",
  async render(el) {
    el.innerHTML = '<div class="card"><div class="card-title">Docker 环境</div>' + skeletonTable(4, 4) + "</div>";
    var self = this;
    var info;
    try { info = await api("/docker/status"); }
    catch (e) { el.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    if (el.dataset.route !== "docker") return;

    if (!info.installed) {
      el.innerHTML = '<div class="card" style="max-width:520px;margin:48px auto;text-align:center;padding:48px 36px">' +
        '<div style="margin-bottom:18px">' + iconBox("cube", 68) + "</div>" +
        '<h3 style="font-size:18px;margin-bottom:8px">Docker 容器环境</h3>' +
        '<p style="color:var(--text-3);font-size:13.5px;margin-bottom:24px">安装后可管理容器、镜像、网络与数据卷,应用商店全部应用依赖 Docker</p>' +
        '<div style="display:flex;flex-direction:column;gap:12px;align-items:center">' +
        '<select class="select" id="dk-mirror" style="width:100%;max-width:320px;padding:10px 12px;border-radius:10px;font-size:13px">' +
        '<option value="auto">自动选择镜像源</option>' +
        '<option value="">国际官方源</option>' +
        '<option value="aliyun">阿里云镜像</option>' +
        '<option value="ustc">中科大镜像</option></select>' +
        '<button class="btn btn-primary" id="dk-install" style="width:100%;max-width:320px;padding:12px;font-size:14px">' + I("download") + " 一键安装 Docker</button>" +
        '</div></div>';
      qs("#dk-install", el).onclick = function() {
        var btn = this;
        btn.disabled = true;
        btn.innerHTML = '<span class="spinner" style="width:14px;height:14px;border-width:2px;display:inline-block;vertical-align:middle;margin-right:6px"></span> 正在安装…';
        api("/docker/install", { method: "POST", body: { mirror: qs("#dk-mirror", el).value } }).then(function(d) {
          taskModal(d.id, function(st) { if (st === "success") self.render(el); });
        }).catch(function(e) {
          btn.disabled = false;
          btn.innerHTML = I("download") + " 一键安装 Docker";
          toast(e.message, "error", 8000);
        });
      };
      return;
    }

    var head = '<div class="card"><div class="card-title">Docker ' + esc(info.version || "") +
      '<div style="display:flex;gap:8px"><button class="btn btn-sm btn-plain" id="dk-refresh">' + I("refresh") + " 刷新</button>" +
      '<button class="btn btn-sm btn-plain" id="dk-logrotate">' + I("gear") + ' 日志轮转</button>' +
      '<button class="btn btn-sm btn-plain" id="dk-prune">' + I("trash") + ' 清理悬空镜像</button></div></div>' +
      '<div class="kpi-strip" style="grid-template-columns:repeat(4,1fr)">' +
      '<div class="card kpi-card" style="cursor:default"><span class="kpi-ico" style="color:#0ea5e9">' + I("play") + '</span><div class="kpi-num"><b>' + (info.runningCount || 0) + "</b><span>运行中容器</span></div></div>" +
      '<div class="card kpi-card" style="cursor:default"><span class="kpi-ico" style="color:#94a3b8">' + I("cube") + '</span><div class="kpi-num"><b>' + (info.containers || 0) + "</b><span>全部容器</span></div></div>" +
      '<div class="card kpi-card" style="cursor:default"><span class="kpi-ico" style="color:#a78bfa">' + I("stack") + '</span><div class="kpi-num"><b>' + (info.images || 0) + "</b><span>镜像</span></div></div>" +
      '<div class="card kpi-card" style="cursor:default"><span class="kpi-ico" style="color:' + (info.running ? "#4ade80" : "#f56c6c") + '">' + I("bolt") + '</span><div class="kpi-num"><b>' + (info.running ? "正常" : "停止") + "</b><span>引擎状态</span></div></div>" +
      '</div><div class="hub-tabs" style="margin-top:14px">';
    var tabsDef = [["containers", "容器"], ["images", "镜像"], ["networks", "网络"], ["volumes", "数据卷"]];
    tabsDef.forEach(function(t) {
      head += '<div class="hub-tab ' + (self.tab === t[0] ? "active" : "") + '" data-tab="' + t[0] + '">' + t[1] + "</div>";
    });
    head += '</div><div id="dk-body">' + skeletonTable(5, 5) + "</div></div>";
    el.innerHTML = head;
    qs("#dk-refresh", el).onclick = function() { self.render(el); };
    qs("#dk-logrotate", el).onclick = async function() {
        var st = {};
        try { st = await api("/docker/log-rotate"); } catch (e) {}
        var mm = modal({
          title: "容器日志轮转", width: 560,
          body: '<div class="alert alert-warning page-fade"><span class="alert-ico">' + I("warn") + "</span>" +
            "容器日志默认不轮转,单个容器输出大量日志会<b>无限增长撑爆磁盘</b>。配置后对所有<b>新启动</b>的容器生效(已有容器需重建一次才生效)。</div>" +
            '<div class="settings-section"><div class="settings-section-title">当前状态</div>' +
            '<div class="help" style="margin:0">' + (st.configured ? "已配置:单容器 " + esc(st.max_size) + " × " + esc(String(st.max_file)) + " 个文件" : "未配置(日志无限增长)") + "</div></div>" +
            '<div class="settings-section"><div class="settings-section-title">轮转参数</div>' +
            '<div style="display:flex;gap:10px;align-items:end;flex-wrap:wrap">' +
            '<div><label>单文件上限</label><input class="input" id="lr-size" value="' + esc(st.max_size || "50m") + '" style="width:110px" placeholder="50m"></div>' +
            '<div><label>保留文件数</label><input class="input" id="lr-files" type="number" value="' + (Number(st.max_file) || 3) + '" style="width:90px"></div></div>' +
            '<div class="help" style="margin-top:6px">写入 /etc/docker/daemon.json</div></div>',
          footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">保存并重启 Docker</button>',
        });
        qs('[data-act="no"]', mm.el).onclick = mm.close;
        qs('[data-act="yes"]', mm.el).onclick = async function() {
          var files = Number(qs("#lr-files", mm.el).value) || 3;
          if (!(await confirmDlg("保存配置并<b>重启 Docker</b>?<br><span style='font-size:12px;color:var(--danger)'>所有容器将重启几秒钟,网站短暂中断。</span>", { danger: true }))) return;
          this.disabled = true; this.textContent = "执行中...";
          try {
            await api("/docker/log-rotate", { method: "POST", body: {
              max_size: qs("#lr-size", mm.el).value.trim(), max_file: files, restart: true }, timeout: 60000 });
            toast("日志轮转已配置,Docker 已重启");
            mm.close(); self.render(el);
          } catch (e) { toast(e.message, "error", 6000); this.disabled = false; this.textContent = "保存并重启 Docker"; }
        };
      };
      qs("#dk-prune", el).onclick = async function() {
        if (!(await confirmDlg("清理全部悬空镜像?不影响在用镜像。"))) return;
        try {
          var r = await api("/docker/prune", { method: "POST", body: {} });
          toast("已清理,释放 " + (r.reclaimed || "0"), "success", 4000);
          self.render(el);
        } catch (e) { toast(e.message, "error"); }
      };
    qsa(".hub-tab", el).forEach(function(t) {
      t.onclick = function() {
        self.tab = t.getAttribute("data-tab");
        qsa(".hub-tab", el).forEach(function(x) { x.classList.remove("active"); });
        t.classList.add("active");
        self.renderTab(qs("#dk-body", el));
      };
    });
    this.renderTab(qs("#dk-body", el));
  },
  async renderTab(body) {
    var self = this;
    if (this.tab === "containers") return this.tabContainers(body);
    if (this.tab === "images") return this.tabImages(body);
    var ep = this.tab === "networks" ? "/docker/networks" : "/docker/volumes";
    var list;
    try { list = (await api(ep)) || []; }
    catch (e) { body.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    if (!body.isConnected) return;
    if (!list.length) { body.innerHTML = emptyState("cube", "暂无数据"); return; }
    var isVol = this.tab === "volumes";
    body.innerHTML = '<div class="table-wrap"><table class="table"><thead><tr><th>名称</th><th>驱动</th><th>范围</th><th>详情</th>' + (isVol ? '<th style="width:90px">操作</th>' : "") + '</tr></thead><tbody>' +
      list.map(function(n) {
        return "<tr><td><b>" + esc(n.name) + "</b></td><td>" + esc(n.driver || "-") + "</td><td>" + esc(n.scope || "-") + '</td><td class="mono fm-ellipsis" title="' + esc(n.detail || "") + '">' + esc(n.detail || "-") + "</td>" +
          (isVol ? '<td><button class="btn btn-xs btn-danger-plain" data-vol-rm="' + esc(n.name) + '">删除</button></td>' : "") + "</tr>";
      }).join("") + "</tbody></table></div>";
    if (isVol) {
      qsa("[data-vol-rm]", body).forEach(function(b) {
        b.onclick = async function() {
          var name = b.getAttribute("data-vol-rm");
          if (!(await confirmDlg("删除数据卷 <b class=mono>" + esc(name) + "</b>?<br><span style='font-size:12px;color:var(--text-3)'>卷内数据将<b>永久删除且不可恢复</b>;若卷仍被容器使用,删除会失败。</span>", { danger: true }))) return;
          b.disabled = true;
          try {
            await api("/docker/volumes/remove", { method: "POST", body: { name: name } });
            toast("数据卷已删除");
            self.renderTab(body);
          } catch (e) {
            toast(e.message, "error", 8000);
            b.disabled = false;
          }
        };
      });
    }
  },
  async tabContainers(body) {
    var self = this;
    var rs = await Promise.all([
      api("/docker/containers").catch(function() { return []; }),
      api("/docker/stats").catch(function() { return []; }),
    ]);
    var list = rs[0] || [], stats = rs[1] || [];
    if (!body.isConnected) return;
    var statMap = {};
    stats.forEach(function(st) { statMap[st.name] = st; });
    var html = '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px;gap:10px;flex-wrap:wrap">' +
      '<span class="tag tag-info">共 ' + list.length + " 个容器</span>" +
      '<button class="btn btn-sm btn-primary" id="dk-create">' + I("plus") + " 创建容器</button></div>";
    if (!list.length) {
      html += emptyState("cube", "还没有容器", "去应用商店一键部署,或点击创建容器");
    } else {
      html += '<div class="table-wrap"><table class="table"><thead><tr><th>名称</th><th>镜像</th><th style="width:110px">状态</th><th style="width:150px">CPU / 内存</th><th>端口</th><th style="width:250px">操作</th></tr></thead><tbody>';
      list.forEach(function(c) {
        var running = c.state === "running";
        var st = statMap[c.names] || {};
        html += "<tr><td><b>" + esc(c.names) + "</b><div class='help'>" + esc(c.id) + "</div></td>" +
          '<td class="mono fm-ellipsis" style="max-width:170px" title="' + esc(c.image) + '">' + esc(c.image) + "</td>" +
          "<td>" + (running
            ? '<span class="tag tag-success"><span class="dot on"></span>运行中</span>'
            : '<span class="tag tag-info">' + esc(c.status || "已停止") + "</span>") + "</td>" +
          '<td class="mono">' + esc(st.cpu || "-") + " / " + esc(st.mem || "-") + "</td>" +
          '<td class="mono fm-ellipsis" style="max-width:170px" title="' + esc(c.ports) + '">' + esc(c.ports || "-") + "</td>" +
          "<td><div class='fm-ops'>" +
          (running
            ? '<button class="btn btn-xs btn-plain" data-dk-act="stop" data-id="' + esc(c.id) + '" data-name="' + esc(c.names) + '">停止</button>' +
              '<button class="btn btn-xs btn-plain" data-dk-act="restart" data-id="' + esc(c.id) + '" data-name="' + esc(c.names) + '">重启</button>'
            : '<button class="btn btn-xs btn-plain" data-dk-act="start" data-id="' + esc(c.id) + '" data-name="' + esc(c.names) + '">启动</button>') +
          '<button class="btn btn-xs btn-plain" data-dk-edit="' + esc(c.id) + '" data-name="' + esc(c.names) + '">编辑</button>' +
          '<button class="btn btn-xs btn-plain" data-dk-logs="' + esc(c.id) + '" data-name="' + esc(c.names) + '">日志</button>' +
          '<button class="btn btn-xs btn-plain" data-dk-logclr="' + esc(c.id) + '" data-name="' + esc(c.names) + '">清日志</button>' +
          '<button class="btn btn-xs btn-danger-plain" data-dk-rm="' + esc(c.id) + '" data-name="' + esc(c.names) + '">删除</button>' +
          "</div></td></tr>";
      });
      html += "</tbody></table></div>";
    }
    body.innerHTML = html;
    qsa("[data-dk-act]", body).forEach(function(b) {
      b.onclick = async function() {
        var act = b.getAttribute("data-dk-act"), id = b.getAttribute("data-id"), name = b.getAttribute("data-name");
        var cn = { start: "启动", stop: "停止", restart: "重启" }[act];
        if (!(await confirmDlg("确定" + cn + "容器 <b>" + esc(name) + "</b> 吗?", { danger: act === "stop" }))) return;
        try {
          await api("/docker/containers/" + encodeURIComponent(id) + "/" + act, { method: "POST", body: {} });
          toast(cn + "成功");
          self.tabContainers(body);
        } catch (e) { toast(e.message, "error"); }
      };
    });
    qsa("[data-dk-edit]", body).forEach(function(b) {
      b.onclick = function() { self.containerEditDialog(b.getAttribute("data-dk-edit"), b.getAttribute("data-name")); };
    });
    qsa("[data-dk-logclr]", body).forEach(function(b) {
      b.onclick = async function() {
        var id = b.getAttribute("data-dk-logclr"), name = b.getAttribute("data-name");
        if (!(await confirmDlg("清空容器 <b>" + esc(name) + "</b> 的日志文件?不影响运行。", { danger: true }))) return;
        try {
          await api("/docker/containers/" + encodeURIComponent(id) + "/logs/clear", { method: "POST", body: {} });
          toast("日志已清理");
        } catch (e) { toast(e.message, "error", 6000); }
      };
    });
    qsa("[data-dk-rm]", body).forEach(function(b) {
      b.onclick = async function() {
        var id = b.getAttribute("data-dk-rm"), name = b.getAttribute("data-name");
        if (!(await confirmDlg("删除容器 <b>" + esc(name) + "</b>?容器数据卷不会删除。", { danger: true }))) return;
        try {
          await api("/docker/containers/" + encodeURIComponent(id), { method: "DELETE" });
          toast("已删除");
          self.tabContainers(body);
        } catch (e) { toast(e.message, "error"); }
      };
    });
    qsa("[data-dk-logs]", body).forEach(function(b) {
      b.onclick = function() { self.logsDialog(b.getAttribute("data-dk-logs"), b.getAttribute("data-name")); };
    });
    var createBtn = qs("#dk-create", body);
    if (createBtn) createBtn.onclick = function() { self.createDialog(body); };
  },
  logsDialog(id, name) {
    var m = modal({
      title: "容器日志 · " + name, width: 860,
      body: '<div class="log-view mono" id="dk-log-view" style="max-height:56vh">加载中...</div>',
      footer: '<button class="btn btn-sm btn-plain" data-act="refresh">刷新</button><button class="btn btn-primary" data-act="close">关 闭</button>',
    });
    qs('[data-act="close"]', m.el).onclick = m.close;
    var load = function() {
      api("/docker/containers/" + encodeURIComponent(id) + "/logs?tail=300").then(function(content) {
        var v = qs("#dk-log-view", m.el);
        if (!v) return;
        v.textContent = content || "(空)";
        v.scrollTop = v.scrollHeight;
      }).catch(function(e) { var v = qs("#dk-log-view", m.el); if (v) v.textContent = "加载失败: " + e.message; });
    };
    load();
    qs('[data-act="refresh"]', m.el).onclick = load;
  },
  createDialog(body) {
    var m = modal({
      title: "创建容器", width: 560,
      body: '<div class="form-item required"><label>镜像</label><input class="input mono" id="dkc-image" placeholder="如 nginx:latest"></div>' +
        '<div class="form-item required"><label>容器名</label><input class="input mono" id="dkc-name" placeholder="如 my-nginx"></div>' +
        '<div class="form-item"><label>端口映射(每行一条 host:container)</label><textarea class="textarea mono" id="dkc-ports" rows="2" placeholder="8080:80"></textarea></div>' +
        '<div class="form-item"><label>目录挂载(每行一条 /host:/container)</label><textarea class="textarea mono" id="dkc-vols" rows="2" placeholder="/opt/data:/usr/share/nginx/html"></textarea></div>' +
        '<div class="form-item"><label>重启策略</label><select class="select" id="dkc-restart">' +
        '<option value="unless-stopped">unless-stopped(推荐)</option><option value="always">always</option><option value="no">不自动重启</option></select></div>',
      footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">创建并启动</button>',
    });
    qs('[data-act="no"]', m.el).onclick = m.close;
    qs('[data-act="yes"]', m.el).onclick = async function() {
      var image = qs("#dkc-image", m.el).value.trim(), name = qs("#dkc-name", m.el).value.trim();
      if (!image || !name) { toast("镜像与容器名必填", "error"); return; }
      var ports = qs("#dkc-ports", m.el).value.split("\n").map(function(x) { return x.trim(); }).filter(Boolean);
      var vols = qs("#dkc-vols", m.el).value.split("\n").map(function(x) { return x.trim(); }).filter(Boolean);
      try {
        var d = await api("/docker/create", { method: "POST", body: {
          image: image, name: name, ports: ports, volumes: vols, env: {}, restart: qs("#dkc-restart", m.el).value,
        }});
        m.close();
        taskModal(d.id, function(st) { if (st === "success") toast("容器已创建并启动"); PAGES.docker.renderTab(body); });
      } catch (e) { toast(e.message, "error"); }
    };
  },
  /* 容器编辑:资源限制 / 重启策略 */
  async containerEditDialog(id, name) {
    let d;
    try { d = await api("/docker/containers/" + encodeURIComponent(id) + "/info"); }
    catch (e) { toast(e.message, "error"); return; }

    const cpuCur = d.cpu_limit > 0 ? d.cpu_limit.toFixed(1) : "";
    const memCur = d.mem_limit > 0 ? Math.round(d.mem_limit / 1048576) : "";
    const rp = d.restart_policy || "no";
    const rpNames = { no: "不重启", always: "总是重启", "unless-stopped": "除非手动停止", "on-failure": "异常退出时重启" };

    const m = modal({
      title: "编辑容器 · " + (name || id), width: 560,
      body:
        '<div class="settings-section"><div class="settings-section-title">资源限制<span class="help" style="margin:0">填 0 或留空 = 不限制;修改立即生效无需重启容器</span></div>' +
        '<div class="settings-grid">' +
        '<div><label>CPU 核数上限</label><input class="input" id="ce-cpu" type="number" min="0" step="0.1" value="' + (cpuCur || "") + '" placeholder="如 1.5"></div>' +
        '<div><label>内存上限 (MB)</label><input class="input" id="ce-mem" type="number" min="0" value="' + (memCur || "") + '" placeholder="如 512"></div></div>' +
        '<div class="help">当前值:' + (cpuCur ? "CPU " + cpuCur + " 核" : "CPU 不限") + ' · ' + (memCur ? "内存 " + memCur + " MB" : "内存不限") + '</div></div>' +
        '<div class="settings-section"><div class="settings-section-title">重启策略</div>' +
        '<select class="select" id="ce-rp" style="max-width:280px">' +
        ["no","always","unless-stopped","on-failure"].map(function(v) { return '<option value="' + v + '"' + (rp === v ? " selected" : "") + ">" + (rpNames[v] || v) + " (" + v + ")</option>"; }).join("") +
        '</select><div class="help">always = 容器退出后总是自动重启(生产推荐)</div></div>' +
        '<div class="settings-hint" style="margin-top:12px">修改通过 docker update 热生效,无需重启容器;端口/挂载等结构性变更请通过应用商店的 compose 编辑完成</div>',
      footer: '<button class="btn btn-plain" data-act="no">取 消</button><button class="btn btn-primary" data-act="yes">保存修改</button>',
    });
    qs("#ce-rp", m.el).value = rp;
    qs('[data-act="no"]', m.el).onclick = m.close;
    qs('[data-act="yes"]', m.el).onclick = async function() {
      const cpus = parseFloat(qs("#ce-cpu", m.el).value) || 0;
      const memMB = parseInt(qs("#ce-mem", m.el).value) || 0;
      const rp = qs("#ce-rp", m.el).value;
      try {
        await api("/docker/containers/" + encodeURIComponent(id) + "/update", { method: "POST", body: {
          cpus: cpus, memory_mb: memMB, restart_policy: rp,
        }});
        toast("容器配置已更新");
        m.close();
      } catch (e) { toast(e.message, "error", 8000); }
    };
  },
  async tabImages(body) {
    var self = this;
    var list;
    try { list = (await api("/docker/images")) || []; }
    catch (e) { body.innerHTML = emptyState("warn", "加载失败", e.message); return; }
    if (!body.isConnected) return;
    var html = '<div style="display:flex;gap:10px;margin-bottom:12px;flex-wrap:wrap">' +
      '<input class="input mono" id="dk-pull-image" placeholder="拉取镜像,如 redis:7" style="flex:1;min-width:200px">' +
      '<button class="btn btn-primary btn-sm" id="dk-pull">' + I("download") + " 拉取</button></div>";
    if (!list.length) {
      html += emptyState("stack", "还没有镜像");
    } else {
      html += '<div class="table-wrap"><table class="table"><thead><tr><th>仓库</th><th>标签</th><th style="width:110px">大小</th><th style="width:150px">创建时间</th><th style="width:90px">操作</th></tr></thead><tbody>';
      list.forEach(function(img) {
        html += "<tr><td class='mono fm-ellipsis' style='max-width:220px' title=\"" + esc(img.repository) + '"><b>' + esc(img.repository) + "</b></td>" +
          "<td class='mono'>" + esc(img.tag) + "</td><td>" + esc(img.size) + "</td><td>" + esc(img.created_at) + "</td>" +
          '<td><button class="btn btn-xs btn-danger-plain" data-dk-rmi="' + esc(img.id) + '" data-name="' + esc(img.repository + ":" + img.tag) + '">删除</button></td></tr>';
      });
      html += "</tbody></table></div>";
    }
    body.innerHTML = html;
    var pullBtn = qs("#dk-pull", body);
    pullBtn.onclick = function() {
      var image = qs("#dk-pull-image", body).value.trim();
      if (!image) { toast("请输入镜像名", "error"); return; }
      api("/docker/pull", { method: "POST", body: { image: image } }).then(function(d) {
        taskModal(d.data.id, function(st) { if (st === "success") self.tabImages(body); });
      }).catch(function(e) { toast(e.message, "error"); });
    };
    qsa("[data-dk-rmi]", body).forEach(function(b) {
      b.onclick = async function() {
        if (!(await confirmDlg("删除镜像 <b>" + esc(b.getAttribute("data-name")) + "</b>?", { danger: true }))) return;
        try {
          await api("/docker/images/" + encodeURIComponent(b.getAttribute("data-dk-rmi")), { method: "DELETE" });
          toast("已删除");
          self.tabImages(body);
        } catch (e) { toast(e.message, "error"); }
      };
    });
  },
};


/* 把已有 textarea 升级为 CodeMirror(隐藏原节点并双向同步,现有保存逻辑无需改动) */
function cmUpgrade(ta, mode, extra) {
  if (typeof CodeMirror === "undefined" || !ta || ta._cm) return null;
  var dark = document.documentElement.getAttribute("data-theme") === "dark";
  var cm = CodeMirror(function(node) { ta.parentNode.insertBefore(node, ta); }, {
    value: ta.value,
    mode: mode,
    theme: dark ? "material-darker" : "default",
    lineNumbers: true,
    styleActiveLine: true,
    matchBrackets: true,
    autoCloseBrackets: true,
    indentUnit: 4,
    extraKeys: extra || {},
  });
  ta.style.display = "none";
  cm.on("change", function() { ta.value = cm.getValue(); });
  ta._cm = cm;
  return cm;
}
/* 按扩展名映射 CodeMirror 模式 */
function cmModeOf(path) {
  var ext = (path.split(".").pop() || "").toLowerCase();
  var map = {
    html: "htmlmixed", htm: "htmlmixed",
    js: "javascript", mjs: "javascript",
    json: { name: "javascript", json: true },
    css: "css",
    php: "application/x-httpd-php",
    sql: "sql",
    sh: "shell", bash: "shell",
    md: "markdown", markdown: "markdown",
    yml: "yaml", yaml: "yaml",
    ini: "properties", conf: "nginx", env: "properties", cfg: "properties", htaccess: "properties",
    py: "python",
    xml: "xml", svg: "xml",
  };
  return map[ext] || (ext === "txt" ? "" : "text/plain");
}
