package server

import (
	"net/http"
	"time"

	"kunpanel/internal/authx"
	"kunpanel/internal/alertx"
	"kunpanel/internal/captcha"
	"kunpanel/internal/notifyx"
	"kunpanel/internal/config"
	"kunpanel/internal/logsx"
)

// CaptchaStore 全局验证码存储;CaptchaFreq 限制每 IP 获取频率
// UserLimiter 用户名维度的失败限流(防止多 IP 分布式爆破管理员账号)
var (
	CaptchaStore = captcha.NewStore()
	CaptchaFreq  = captcha.NewFreq(20, time.Minute)
	UserLimiter  = authx.NewLimiterOpt(20, 15*time.Minute)
)

type loginReq struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	CaptchaID   string `json:"captcha_id"`
	CaptchaCode string `json:"captcha_code"`
}

// handleCaptcha 输出登录验证码(base64 PNG 位图,无可机读文本)
func (s *Server) handleCaptcha(w http.ResponseWriter, r *http.Request) {
	ip := ClientIP(r)
	if !CaptchaFreq.Allow(ip) {
		Fail(w, http.StatusTooManyRequests, "验证码获取过于频繁,请稍后再试")
		return
	}
	id, img := CaptchaStore.New()
	OK(w, map[string]string{"captcha_id": id, "image": img})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	ip := ClientIP(r)
	cfg := config.M.Get()

	// 1) 先判定锁定(避免被锁 IP 继续消耗验证码资源)
	if blocked, wait := s.limiter.Blocked(ip); blocked {
		logsx.AppendLoginResult(ip, false, "尝试过多被锁定")
		Fail(w, http.StatusTooManyRequests, "失败次数过多,请 "+wait.Round(time.Minute).String()+" 后再试")
		return
	}

	// 2) 验证码(一次性,失败后需刷新)
	if !CaptchaStore.Verify(req.CaptchaID, req.CaptchaCode) {
		logsx.AppendLoginResult(ip, false, "验证码错误")
		Fail(w, http.StatusBadRequest, "验证码错误或已过期")
		return
	}

	// 3) 账号密码(用户名维度也限流,防多 IP 分布式爆破管理员账号)
	if req.Username != cfg.Username || !config.M.CheckPassword(req.Password) {
		s.limiter.Fail(ip)
		UserLimiter.Fail(req.Username)
		logsx.AppendLoginResult(ip, false, "用户名或密码错误")
		Fail(w, http.StatusBadRequest, "用户名或密码错误")
		return
	}
	if blocked, _ := UserLimiter.Blocked(req.Username); blocked {
		logsx.AppendLoginResult(ip, false, "账号尝试过多被锁定")
		Fail(w, http.StatusTooManyRequests, "该账号失败次数过多,请稍后再试")
		return
	}

	// 4) 用户名维度限流计数(登录成功即清零)
	s.userLimiter.Reset(req.Username)

	// 5) 旧版口令哈希透明升级为 PBKDF2
	config.M.UpgradePasswordHash(req.Password)

	// 6) 两步验证:密码通过后先签发 5 分钟 OTP 临时令牌,验证动态码后才发会话令牌
	if cfg.TotpEnabled && cfg.TotpSecret != "" {
		otpToken := authx.NewToken(cfg.JWTSecret, "otp:"+cfg.Username, 5*time.Minute)
		OK(w, map[string]any{"need_otp": true, "otp_token": otpToken, "username": cfg.Username})
		return
	}

	s.limiter.Reset(ip)
	logsx.AppendLoginResult(ip, true, "登录成功")
	// 登录提醒:向用户配置的告警渠道推送(邮件/Webhook),陌生登录第一时间可知
	notifyx.Add("login", "账号登录", "账号 "+cfg.Username+" 从 "+ip+" 登录成功")
	alertx.Broadcast(alertx.Get().Webhook,
		"【坤面板】登录提醒: 账号 "+cfg.Username+" 于 "+time.Now().Format("2006-01-02 15:04:05")+" 从 "+ip+" 登录成功",
		map[string]any{"type": "login", "ip": ip, "user": cfg.Username})

	token := authx.NewToken(cfg.JWTSecret, cfg.Username, 24*time.Hour)
	OK(w, map[string]string{
		"token":    token,
		"username": cfg.Username,
		"panel":    cfg.PanelName,
	})
}

// handleOtpVerify 两步验证动态码校验,通过后签发正式会话令牌
func (s *Server) handleOtpVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OtpToken string `json:"otp_token"`
		Code     string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	ip := ClientIP(r)
	cfg := config.M.Get()

	if blocked, wait := s.limiter.Blocked(ip); blocked {
		Fail(w, http.StatusTooManyRequests, "失败次数过多,请 "+wait.Round(time.Minute).String()+" 后再试")
		return
	}
	// OTP 临时令牌必须有效(用户名绑定 + 5 分钟内)
	if !authx.VerifyToken(cfg.JWTSecret, req.OtpToken, cfg.Username) {
		Fail(w, http.StatusBadRequest, "验证已过期,请重新登录")
		return
	}
	if !authx.VerifyTOTP(cfg.TotpSecret, req.Code) {
		s.limiter.Fail(ip)
		logsx.AppendLoginResult(ip, false, "两步验证码错误")
		Fail(w, http.StatusBadRequest, "动态码错误或已过期")
		return
	}
	s.limiter.Reset(ip)
	logsx.AppendLoginResult(ip, true, "登录成功(两步验证)")

	token := authx.NewToken(cfg.JWTSecret, cfg.Username, 24*time.Hour)
	OK(w, map[string]string{
		"token":    token,
		"username": cfg.Username,
		"panel":    cfg.PanelName,
	})
}

// handleTotpSetup 生成两步验证密钥(需验证码确认后才真正启用)
func (s *Server) handleTotpSetup(w http.ResponseWriter, r *http.Request) {
	secret, err := authx.GenerateTOTPSecret()
	if err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := config.M.Update(func(c *config.Config) {
		c.TotpSecret = secret
		c.TotpEnabled = false
	}); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"secret": secret})
}

type totpEnableReq struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

// handleTotpEnable 确认动态码,正式启用两步验证
func (s *Server) handleTotpEnable(w http.ResponseWriter, r *http.Request) {
	var req totpEnableReq
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	cfg := config.M.Get()
	if cfg.TotpSecret == "" {
		Fail(w, http.StatusBadRequest, "请先生成密钥")
		return
	}
	if !authx.VerifyTOTP(cfg.TotpSecret, req.Code) {
		Fail(w, http.StatusBadRequest, "动态码错误,请确认验证器时间与密钥")
		return
	}
	if err := config.M.Update(func(c *config.Config) { c.TotpEnabled = true }); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

// handleTotpDisable 关闭两步验证(需验证登录密码)
func (s *Server) handleTotpDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "参数错误")
		return
	}
	if !config.M.CheckPassword(req.Password) {
		Fail(w, http.StatusBadRequest, "密码错误")
		return
	}
	if err := config.M.Update(func(c *config.Config) {
		c.TotpEnabled = false
		c.TotpSecret = ""
	}); err != nil {
		Fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, nil)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	logsx.AppendLoginResult(ClientIP(r), true, "退出登录")
	OK(w, nil)
}
