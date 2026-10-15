-- 坤面板 WAF 引擎 v2 (access_by_lua_file)
-- 模式: block(拦截) / alert(告警放行) / monitor(仅监控)
-- 功能: CC 防护(限流/5秒跳转/JS无感/点击/验证码) / IP黑白名单 / URL白名单 / UA黑名单 / 规则组独立开关 / IP临时封禁
local json = require "cjson.safe"

local CONF_PATH = "/usr/local/kunwaf/config.json"
local LOG_DIR   = "/usr/local/kunwaf/logs"
local LOG_PATH  = LOG_DIR .. "/waf.log"
local LOG_MAX   = 5 * 1024 * 1024
local BODY_MAX  = 1024 * 1024

local conf_cache, conf_at = nil, 0
local bad_rules = {}

local RULE_LABELS = {
    sql = "SQL 注入", xss = "XSS 跨站脚本", lfi = "文件包含/穿越",
    cmd = "命令执行", scan = "扫描器", cc = "CC 攻击",
    ip_black = "IP 黑名单", ua_black = "UA 黑名单", ip_ban = "IP 临时封禁",
    sensitive = "敏感文件", method = "危险方法", custom = "自定义规则",
}

local MODE_LABELS = { block = "拦截", alert = "告警", monitor = "监控" }

local function load_conf()
    local now = ngx.now()
    if conf_cache and (now - conf_at) < 5 then return conf_cache end
    local f = io.open(CONF_PATH, "r")
    if not f then return nil end
    local body = f:read("*a"); f:close()
    local conf = json.decode(body)
    if type(conf) == "table" then conf_cache, conf_at = conf, now; return conf end
    return nil
end

local function rotate_if_needed()
    local f = io.open(LOG_PATH, "r")
    if not f then return end
    local size = f:seek("end"); f:close()
    if size > LOG_MAX then os.rename(LOG_PATH, LOG_PATH .. ".old") end
end

local function write_log(rule, detail, action)
    pcall(function()
        os.execute("mkdir -p " .. LOG_DIR)
        rotate_if_needed()
        local entry = json.encode({
            time = os.date("%Y-%m-%d %H:%M:%S"),
            ip = ngx.var.remote_addr, host = ngx.var.host,
            method = ngx.req.get_method(), uri = ngx.var.request_uri,
            ua = ngx.var.http_user_agent or "",
            rule = rule, label = RULE_LABELS[rule] or rule,
            detail = detail or "", action = action or "block",
            mode = MODE_LABELS[ngx.var.waf_mode or "block"],
        })
        if entry then
            local w = io.open(LOG_PATH, "a")
            if w then w:write(entry, "\n"); w:close() end
        end
    end)
end

local bad_rules = {}
local function rule_match(value, pat)
    if not value or value == "" or pat == "" then return false end
    local ok, from, to, err = pcall(ngx.re.find, value, pat, "ijo")
    if not ok or err then
        if not bad_rules[pat] then bad_rules[pat] = true; write_log("rule_error", pat) end
        return false
    end
    return from ~= nil
end

local function in_list(value, list)
    if type(list) ~= "table" or value == nil or value == "" then return false end
    for _, pat in ipairs(list) do
        if pat ~= "" and rule_match(value, pat) then return true end
    end
    return false
end

-- IPv4 → 32 位整数(非法返回 nil)
local function ipv4_to_int(s)
    local a, b, c, d = s:match("^(%d+)%.(%d+)%.(%d+)%.(%d+)$")
    if not a then return nil end
    a, b, c, d = tonumber(a), tonumber(b), tonumber(c), tonumber(d)
    if a > 255 or b > 255 or c > 255 or d > 255 then return nil end
    return ((a * 256 + b) * 256 + c) * 256 + d
end

-- CIDR 匹配(IPv4):"10.0.0.0/8" 形式
local function cidr_match(ip, cidr)
    local net, bits = cidr:match("^(%d+%.%d+%.%d+%.%d+)/(%d+)$")
    if not net then return false end
    bits = tonumber(bits)
    if not bits or bits < 0 or bits > 32 then return false end
    local i1 = ipv4_to_int(ip)
    local i2 = ipv4_to_int(net)
    if not i1 or not i2 then return false end
    if bits == 0 then return true end
    local shift = 32 - bits
    local mask = bit.lshift(1, shift) - 1
    return bit.band(i1, bit.bnot(mask)) == bit.band(i2, bit.bnot(mask))
end

-- IP 列表匹配:支持单 IP / 正则 / CIDR 网段("1.2.3.0/24")
local function ip_in_list(ip, list)
    if type(list) ~= "table" or ip == nil or ip == "" then return false end
    for _, pat in ipairs(list) do
        if pat ~= "" then
            if pat:find("/", 1, true) then
                if cidr_match(ip, pat) then return true end
            elseif rule_match(ip, pat) then
                return true
            end
        end
    end
    return false
end

local function block_page(rule_label, ip)
    ngx.status = 403
    ngx.header.content_type = "text/html; charset=utf-8"
    ngx.say('<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>请求被拦截</title>' ..
        '<style>body{font-family:-apple-system,"PingFang SC",sans-serif;background:#f2f4f7;display:flex;align-items:center;justify-content:center;height:96vh;margin:0}' ..
        '.card{background:#fff;border-radius:16px;padding:52px 64px;text-align:center;box-shadow:0 12px 40px rgba(0,60,160,.10);max-width:92vw}' ..
        '.ic{width:72px;height:72px;margin:0 auto 18px;border-radius:50%;background:#fef2f2;display:flex;align-items:center;justify-content:center;font-size:34px;color:#ef4444}' ..
        'h1{font-size:20px;color:#1a202c;margin:0 0 10px}p{color:#909399;font-size:13.5px;margin:4px 0}</style></head>' ..
        '<body><div class="card"><div class="ic">!</div><h1>请求已被防火墙拦截</h1>' ..
        '<p>您的请求触发了网站安全防护规则</p>' ..
        '<p>规则: ' .. (rule_label or "") .. ' &nbsp;|&nbsp; IP: ' .. (ip or "") .. ' &nbsp;|&nbsp; ' .. os.date("%Y-%m-%d %H:%M:%S") .. '</p>' ..
        '<p style="margin-top:14px;color:#c0c4cc;font-size:12px">KunPanel WAF</p></div></body></html>')
    return ngx.exit(403)
end

-- 根据模式决定动作
local function handle_detection(conf, rule, detail)
    local mode = conf.mode or "block"
    write_log(rule, detail, mode)
    if mode == "monitor" then return end  -- 监控模式仅记录
    if mode == "alert" then
        -- 告警模式:记录但放行,计违规数
        local dict = ngx.shared.kunwaf_viol
        if dict and conf.ip_ban_enable then
            local key = "v:" .. ngx.var.remote_addr
            local newval = dict:incr(key, 1, 0, 3600)
            if newval and newval >= (conf.ip_ban_threshold or 10) then
                -- 加入临时封禁
                local banDict = ngx.shared.kunwaf_ban
                if banDict then banDict:set("b:" .. ngx.var.remote_addr, true, conf.ip_ban_duration or 600) end
                dict:delete(key)
                write_log("ip_ban", "auto ban after " .. newval .. " violations")
            end
        end
    end
    -- 拦截模式在调用方处理
end

local function block_or_alert(conf, rule, detail)
    local mode = conf.mode or "block"
    if mode == "alert" then
        write_log(rule, detail, "alert")
        return  -- 告警模式放行
    end
    write_log(rule, detail)
    ngx.status = 403
    ngx.header.content_type = "text/html; charset=utf-8"
    local ip = ngx.var.remote_addr or ""
    ngx.say('<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>请求被拦截</title>' ..
        '<style>body{font-family:-apple-system,"PingFang SC",sans-serif;background:#f2f4f7;display:flex;align-items:center;justify-content:center;height:96vh;margin:0}' ..
        '.card{background:#fff;border-radius:16px;padding:52px 64px;text-align:center;box-shadow:0 12px 40px rgba(0,60,160,.10);max-width:92vw}' ..
        '.ic{width:72px;height:72px;margin:0 auto 18px;border-radius:50%;background:#fef2f2;display:flex;align-items:center;justify-content:center;font-size:34px;color:#ef4444}' ..
        'h1{font-size:20px;color:#1a202c;margin:0 0 10px}p{color:#909399;font-size:13.5px;margin:4px 0}</style></head>' ..
        '<body><div class="card"><div class="ic">!</div><h1>请求已被防火墙拦截</h1>' ..
        '<p>规则: ' .. (RULE_LABELS[rule] or rule) .. ' | IP: ' .. ip .. ' | ' .. os.date("%Y-%m-%d %H:%M:%S") .. '</p>' ..
        '<p style="margin-top:14px;color:#c0c4cc;font-size:12px">KunPanel WAF</p></div></body></html>')
    return ngx.exit(403)
end

-- ============ CC 人机验证模式 ============
-- cc_mode: rate(默认限流403) / jump(5秒跳转) / js(JS无感) / click(点击) / captcha(算术验证码)
-- 超限后: rate 直接 403;其余模式发挑战页,验证通过颁发 HMAC 防伪 Cookie 放行一段时间
-- 注意:本区块函数需引用 ip/uri,必须在此处声明局部变量(定义点之后才成为 upvalue)
local ip = ngx.var.remote_addr
local uri = ngx.var.request_uri

local function to_hex(s)
    return (string.gsub(s, ".", function(ch)
        return string.format("%02x", string.byte(ch))
    end))
end

local function cc_hmac(conf, data)
    return to_hex(ngx.hmac_sha1(conf.cc_secret or "", data))
end

-- 校验放行 Cookie:kun_cc=<hmac(ip|issue)>.<issue>
local function cc_cookie_valid(conf)
    local val = string.match(ngx.var.http_cookie or "", "kun_cc=([^;]+)")
    if not val then return false end
    local sig, issue = string.match(val, "^([%x]+)%.?(%d*)$")
    if not sig or sig == "" then return false end
    local issue_n = tonumber(issue) or 0
    local now = ngx.now()
    local ttl = tonumber(conf.cc_clear_ttl) or 3600
    if (now - issue_n) > ttl or (issue_n - now) > 30 then return false end
    -- jump 模式强制真的等待了 5 秒(留 1 秒时钟容差);cf 模式强制停留 1 秒
    local ccm = conf.cc_mode or "rate"
    if ccm == "jump" and (now - issue_n) < 4 then return false end
    if ccm == "cf" and (now - issue_n) < 1 then return false end
    if cc_hmac(conf, ip .. "|" .. issue) ~= sig then return false end
    return true
end

local function challenge_html(title, inner, auto_reload)
    return '<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>' .. title .. '</title>' ..
    '<style>body{font-family:-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;background:#f2f4f7;display:flex;align-items:center;justify-content:center;height:96vh;margin:0}' ..
    '.card{background:#fff;border-radius:16px;padding:44px 56px;text-align:center;box-shadow:0 12px 40px rgba(0,60,160,.10);max-width:92vw}' ..
    '.ring{width:56px;height:56px;margin:0 auto 18px;border:4px solid #e8eef5;border-top-color:#3b82f6;border-radius:50%;animation:sp 1s linear infinite}@keyframes sp{to{transform:rotate(360deg)}}' ..
    'h1{font-size:18px;color:#1a202c;margin:0 0 10px}p{color:#7a8499;font-size:13.5px;margin:6px 0}' ..
    '.q{font-size:26px;font-weight:700;color:#1a202c;margin:14px 0;letter-spacing:.05em}' ..
    '.inp{font-size:17px;width:110px;text-align:center;padding:9px 10px;border:1.5px solid #d8dee8;border-radius:9px;outline:none}' ..
    '.inp:focus{border-color:#3b82f6}' ..
    '.btn{display:inline-block;margin-top:14px;padding:10px 30px;background:#3b82f6;color:#fff;border:none;border-radius:9px;font-size:14px;cursor:pointer}' ..
    '.btn:hover{background:#2563eb}.brand{margin-top:16px;color:#c0c4cc;font-size:12px}' ..
    '</style></head><body><div class="card">' .. inner .. '<p class="brand">KunPanel WAF · 人机验证</p></div>' ..
    (auto_reload or "") .. '</body></html>'
end

local function serve_challenge(conf, kind)
    local issue = tostring(math.floor(ngx.now()))
    local inner, script = "", ""
    local host_uri = string.match(uri, "^([^?]*)") or "/"

    if kind == "captcha" then
        -- 算术验证码:答案 HMAC 绑定 IP,不可伪造
        local seed = math.floor((ngx.now() * 1000) % 1000000) + ngx.worker.pid()
        local a = seed % 8 + 2
        local b = math.floor(seed / 7) % 8 + 2
        local payload = issue .. "|" .. a .. "|" .. b
        local tok = ngx.encode_base64(payload) .. "." .. cc_hmac(conf, payload .. "|" .. ip)
        inner = '<h1>访问验证</h1><p>检测到异常请求频率,请输入算式结果继续访问</p><div class="q">' .. a .. ' + ' .. b .. ' = ?</div>' ..
            '<form method="GET" action="' .. host_uri .. '"><input type="hidden" name="__kcc_tok" value="' .. tok .. '">' ..
            '<input class="inp" name="__kcc_ans" autocomplete="off" inputmode="numeric" required> ' ..
            '<button class="btn" type="submit">验 证</button></form>'
    elseif kind == "jump" then
        local sig = cc_hmac(conf, ip .. "|" .. issue)
        inner = '<h1>安全验证中</h1><p>检测到异常请求频率</p><p>请等待 <b id="kcc-s" style="color:#3b82f6;font-size:16px">5</b> 秒,页面将自动跳转…</p>' ..
            '<div class="ring"></div>'
        script = '<script>(function(){var s=5,tok="' .. sig .. '",is=' .. issue .. ';var e=document.getElementById("kcc-s");' ..
            'var t=setInterval(function(){s--;if(e)e.textContent=s;if(s<=0){clearInterval(t);' ..
            'document.cookie="kun_cc="+tok+"."+is+";Path=/;Max-Age=" + (Number(' .. (tonumber(conf.cc_clear_ttl) or 3600) .. ')||3600) + ";SameSite=Lax";' ..
            'location.replace("' .. host_uri .. '");}},1000);})();</script>'
    elseif kind == "click" then
        local sig = cc_hmac(conf, ip .. "|" .. issue)
        inner = '<h1>继续访问前请确认</h1><p>检测到异常请求频率,点击按钮完成人机验证</p>' ..
            '<button class="btn" onclick="document.cookie=\'kun_cc=' .. sig .. '.' .. issue .. ';Path=/;Max-Age=' .. (tonumber(conf.cc_clear_ttl) or 3600) .. ';SameSite=Lax\';location.replace(\'' .. host_uri .. '\')">我已了解,继续访问</button>'
    elseif kind == "cf" then
        -- Cloudflare 风格无感验证:全屏 Verifying 动画页,停留约 1.2 秒后自动颁发 Cookie 回跳
        local sig = cc_hmac(conf, ip .. "|" .. issue)
        local ttl = tonumber(conf.cc_clear_ttl) or 3600
        inner = '<style>body{background:#fff!important;display:flex!important;align-items:center;justify-content:center;height:100vh!important;margin:0!important}' ..
            '.card{box-shadow:none!important;background:#fff!important;padding:0!important;max-width:none!important}' ..
            '.cf-spin{width:52px;height:52px;position:relative;margin:0 auto 26px}' ..
            '.cf-spin i{position:absolute;inset:0;animation:cfblink 1.04s linear infinite}' ..
            '.cf-spin i::after{content:"";position:absolute;top:0;left:50%;width:10px;height:10px;margin-left:-5px;border-radius:50%;background:#2fae4a}' ..
            '.cf-spin i:nth-child(2){transform:rotate(45deg)}.cf-spin i:nth-child(3){transform:rotate(90deg)}.cf-spin i:nth-child(4){transform:rotate(135deg)}' ..
            '.cf-spin i:nth-child(5){transform:rotate(180deg)}.cf-spin i:nth-child(6){transform:rotate(225deg)}.cf-spin i:nth-child(7){transform:rotate(270deg)}.cf-spin i:nth-child(8){transform:rotate(315deg)}' ..
            '.cf-spin i:nth-child(1){animation-delay:-1.04s}.cf-spin i:nth-child(2){animation-delay:-.91s}.cf-spin i:nth-child(3){animation-delay:-.78s}.cf-spin i:nth-child(4){animation-delay:-.65s}' ..
            '.cf-spin i:nth-child(5){animation-delay:-.52s}.cf-spin i:nth-child(6){animation-delay:-.39s}.cf-spin i:nth-child(7){animation-delay:-.26s}.cf-spin i:nth-child(8){animation-delay:-.13s}' ..
            '@keyframes cfblink{0%{opacity:1}100%{opacity:.12}}' ..
            '.cf-t{font-size:27px;color:#313131;font-weight:600;letter-spacing:.01em}' ..
            '.cf-f{margin-top:30px;text-align:right}' ..
            '.cf-f b{font-size:15px;color:#f6821f;letter-spacing:.06em}' ..
            '.cf-f span{display:block;color:#c0c4cc;font-size:11px;margin-top:2px}</style>' ..
            '<div class="cf-spin"><i></i><i></i><i></i><i></i><i></i><i></i><i></i><i></i></div>' ..
            '<div class="cf-t">Verifying…</div>' ..
            '<div class="cf-f"><b>CLOUDFLARE 风格</b><span>KunPanel WAF · 人机验证</span></div>'
        script = '<script>setTimeout(function(){document.cookie="kun_cc=' .. sig .. '.' .. issue .. ';Path=/;Max-Age=' .. ttl .. ';SameSite=Lax";location.replace("' .. host_uri .. '")},1200);</script>' ..
            '<noscript><meta http-equiv="refresh" content="3"></noscript>'
    else -- js 无感验证
        local sig = cc_hmac(conf, ip .. "|" .. issue)
        inner = '<h1>安全验证中</h1><p>正在验证浏览器环境,请稍候…</p><div class="ring"></div>'
        script = '<script>document.cookie="kun_cc=' .. sig .. '.' .. issue .. ';Path=/;Max-Age=' .. (tonumber(conf.cc_clear_ttl) or 3600) .. ';SameSite=Lax";location.replace("' .. host_uri .. '");</script>' ..
            '<noscript><meta http-equiv="refresh" content="3"></noscript>'
    end

    ngx.header.content_type = "text/html; charset=utf-8"
    ngx.header["Cache-Control"] = "no-store"
    ngx.status = 200
    ngx.say(challenge_html("安全验证", inner, script))
    return ngx.exit(200)
end

-- 验证码答案校验(验证码模式的表单回跳)
local function cc_captcha_verify(conf)
    -- arg_ 变量在此环境下不保证 URL 解码(%3D 实测未解码),显式解码一次
    local ans = ngx.unescape_uri(ngx.var.arg___kcc_ans or "")
    local tok = ngx.unescape_uri(ngx.var.arg___kcc_tok or "")
    if ans == "" or tok == "" then return end
    local b64, sig = string.match(tok, "^([^%.]+)%.([^%.]+)$")
    if not b64 or not sig then return end
    local payload = ngx.decode_base64(b64)
    if not payload then return end
    if cc_hmac(conf, payload .. "|" .. ip) ~= sig then return end
    local issue, a, b = string.match(payload, "^(%d+)|(%d+)|(%d+)$")
    if not issue then return end
    local now = ngx.now()
    if (now - tonumber(issue)) > 300 then return end -- 验证码 5 分钟内有效
    if tonumber(ans) ~= tonumber(a) + tonumber(b) then return end
    -- 通过:颁发 HttpOnly 放行 Cookie 并跳回
    local val = cc_hmac(conf, ip .. "|" .. issue)
    ngx.header["Set-Cookie"] = "kun_cc=" .. val .. "." .. issue .. "; Path=/; Max-Age=" .. (tonumber(conf.cc_clear_ttl) or 3600) .. "; HttpOnly; SameSite=Lax"
    local host_uri = string.match(uri, "^([^?]*)") or "/"
    return ngx.redirect(host_uri)
end

-- CC 防护
local function cc_check(conf)
    if not conf.cc_enable then return end
    local dict = ngx.shared.kunwaf_cc
    if not dict then return end
    local window = tonumber(conf.cc_interval) or 10
    local limit = tonumber(conf.cc_limit) or 120
    local bucket = math.floor(ngx.now() / window)
    local key = ip .. "|" .. bucket
    local newval = dict:incr(key, 1, 0, window * 3)
    if newval and newval > limit then
        write_log("cc", newval .. "/" .. window .. "s")
        if conf.mode == "alert" then return end
        local ccmode = conf.cc_mode or "rate"
        if ccmode ~= "rate" then
            -- 人机验证模式:已验证(Cookie 有效)放行,否则出挑战页
            if cc_cookie_valid(conf) then return end
            return serve_challenge(conf, ccmode)
        end
        ngx.status = 403
        ngx.header.content_type = "text/plain"
        ngx.say("403 Too Many Requests")
        return ngx.exit(403)
    end
end

-- ============ 主流程 ============
local conf = load_conf()
if not conf or not conf.enabled then return end
-- cc_mode 可能是空串(Lua 中空串为真值,'' or "rate" 会返回 ''),统一归一
if not conf.cc_mode or conf.cc_mode == "" then conf.cc_mode = "rate" end

local mode = conf.mode or "block"
local ip = ngx.var.remote_addr
local uri = ngx.var.request_uri
local ua = ngx.var.http_user_agent or ""

-- 白名单优先
if ip_in_list(ip, conf.ip_white) then return end
if in_list(uri, conf.url_white) then return end

-- 蜘蛛反向 DNS 验证:PTR 记录 → 正向解析 → 必须包含客户端 IP。
-- 结果按 IP 缓存 1 小时,避免每请求 DNS 查询。查询失败按"未验证"处理(不放行)。
local spider_cache = {}
local function spider_dns_verified(ip)
    local now = ngx.now()
    local c = spider_cache[ip]
    if c and (now - c[2]) < 3600 then return c[1] end
    local ok = false
    local okload, dns = pcall(require, "resty.dns.resolver")
    if okload and dns then
        local res, rerr = dns:new({
            nameservers = { { "127.0.0.11", 53 }, { "8.8.8.8", 53 } },
            timeout = 1500, retrans = 2,
        })
        if res and not rerr then
            local p1, p2, p3, p4 = ip:match("^(%d+)%.(%d+)%.(%d+)%.(%d+)$")
            if p1 then
                local ptr_answers = res:query(p4 .. "." .. p3 .. "." .. p2 .. "." .. p1 .. ".in-addr.arpa", { qtype = dns.TYPE_PTR })
                if ptr_answers and not ptr_answers.errcode then
                    local host = ptr_answers[1] and ptr_answers[1].ptr
                    if host then
                        local fwd = res:query(host, { qtype = dns.TYPE_A })
                        if fwd and not fwd.errcode then
                            for _, ans in ipairs(fwd) do
                                if ans.address == ip then ok = true break end
                            end
                        end
                    end
                end
            end
        end
    end
    if true then
        -- 防缓存无限增长:超过 2000 条清空重建(宽松限位,分布式多 IP 场景兜底)
        local n = 0
        for _ in pairs(spider_cache) do n = n + 1 end
        if n > 2000 then spider_cache = {} end
    end
    spider_cache[ip] = { ok, now }
    return ok
end

-- 搜索引擎蜘蛛白名单:爬虫完全跳过检测(CC 人机验证/封禁均不涉及)
-- 严格模式(spider_verify_dns):UA 命中后做反向 DNS + 正向确认,
-- 验证失败的伪装蜘蛛(伪造 Googlebot UA)回落到正常检测
if conf.spider_white_enable and in_list(ua, conf.spider_white) then
    if not conf.spider_verify_dns or spider_dns_verified(ip) then return end
end

-- 验证码模式:处理挑战表单回跳(答对颁发 Cookie)
if conf.cc_enable and (conf.cc_mode or "rate") == "captcha" and ngx.var.arg___kcc_ans then
    cc_captcha_verify(conf)
end

-- IP 临时封禁检查(alert 模式下积累的封禁)
local banDict = ngx.shared.kunwaf_ban
if banDict and banDict:get("b:" .. ip) then
    write_log("ip_ban", "banned ip request")
    ngx.status = 403
    ngx.header.content_type = "text/plain"
    ngx.say("403 Forbidden (banned)")
    return ngx.exit(403)
end

-- IP 黑名单
if ip_in_list(ip, conf.ip_black) then
    write_log("ip_black", ip)
    ngx.status = 403; ngx.header.content_type = "text/plain"; ngx.say("403 Forbidden")
    return ngx.exit(403)
end

-- UA 黑名单
if in_list(ua, conf.ua_black) then
    write_log("ua_black", ua)
    ngx.status = 403; ngx.header.content_type = "text/plain"; ngx.say("403 Forbidden")
    return ngx.exit(403)
end

-- 危险 HTTP 方法拦截(TRACE/TRACK/CONNECT 等)
if conf.method_block_enable and type(conf.method_block_list) == "table" then
    local method = ngx.req.get_method()
    for _, bm in ipairs(conf.method_block_list) do
        if method == bm then
            write_log("method", method)
            ngx.status = 405
            ngx.header.content_type = "text/plain"
            ngx.say("405 Method Not Allowed")
            return ngx.exit(405)
        end
    end
end

-- CC 防护
cc_check(conf)

-- 规则扫描
local targets = {}
local function add_target(raw)
    if not raw or raw == "" then return end
    targets[#targets + 1] = raw
    local ok, decoded = pcall(ngx.unescape_uri, raw)
    if ok and decoded and decoded ~= raw then
        targets[#targets + 1] = decoded
        local spaced = decoded:gsub("%+", " ")
        if spaced ~= decoded then targets[#targets + 1] = spaced end
    end
end
add_target(uri)
add_target(ngx.var.args or "")
add_target(ua)
add_target(ngx.var.http_referer or "")
local method = ngx.req.get_method()
if method == "POST" or method == "PUT" then
    pcall(function()
        ngx.req.read_body()
        local body = ngx.req.get_body_data()
        if body and #body <= BODY_MAX then targets[#targets + 1] = body end
    end)
end

local rg = conf.rule_groups or {}
local rules = conf.rules or {}
for group, pats in pairs(rules) do
    -- 规则组独立开关
    if rg[group] ~= false and type(pats) == "table" then
        for _, t in ipairs(targets) do
            if t and t ~= "" then
                for _, pat in ipairs(pats) do
                    if rule_match(t, pat) then
                        -- 统一走 block_or_alert:两种模式都记录日志(此前拦截模式不写日志,日志页看不到拦截记录)
                        block_or_alert(conf, group, string.sub(t, 1, 200))
                        break
                    end
                end
            end
        end
    end
end

-- 用户自定义规则(面板可增删,保存即热更新)
-- 注意:Go 侧 nil 切片序列化为 null,而 ngx.null 是真值 userdata,必须显式判型
local customs = conf.custom_rules
if type(customs) ~= "table" then customs = {} end
for _, cr in ipairs(customs) do
    if cr and cr.pattern and cr.pattern ~= "" then
        for _, t in ipairs(targets) do
            if t and t ~= "" then
                if rule_match(t, cr.pattern) then
                    block_or_alert(conf, "custom", (cr.label and (cr.label .. " | ") or "") .. string.sub(t, 1, 200))
                    break
                end
            end
        end
    end
end
