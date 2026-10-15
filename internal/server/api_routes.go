package server

import "net/http"

// registerAPI 注册全部 API 路由
func (s *Server) registerAPI() {
	m := s.mux
	wrap := s.auth

	// 认证
	m.Handle("GET /api/v1/auth/captcha", http.HandlerFunc(s.handleCaptcha))
	m.Handle("POST /api/v1/auth/login", http.HandlerFunc(s.handleLogin))
	m.Handle("POST /api/v1/auth/otp", http.HandlerFunc(s.handleOtpVerify))
	m.Handle("POST /api/v1/auth/logout", wrap(http.HandlerFunc(s.handleLogout)))
	m.Handle("POST /api/v1/auth/totp/setup", wrap(http.HandlerFunc(s.handleTotpSetup)))
	m.Handle("POST /api/v1/auth/totp/enable", wrap(http.HandlerFunc(s.handleTotpEnable)))
	m.Handle("POST /api/v1/auth/totp/disable", wrap(http.HandlerFunc(s.handleTotpDisable)))

	// 概览 / 系统
	m.Handle("GET /api/v1/dashboard", wrap(http.HandlerFunc(s.handleDashboard)))
	m.Handle("GET /api/v1/system/quick", wrap(http.HandlerFunc(s.handleSystemQuick)))
	m.Handle("GET /api/v1/settings", wrap(http.HandlerFunc(s.handleGetSettings)))
	m.Handle("POST /api/v1/settings", wrap(http.HandlerFunc(s.handleSaveSettings)))
	m.Handle("POST /api/v1/settings/password", wrap(http.HandlerFunc(s.handleChangePassword)))
	m.Handle("POST /api/v1/panel/restart", wrap(http.HandlerFunc(s.handlePanelRestart)))

	// 用户头像
	m.Handle("GET /api/v1/avatar", wrap(http.HandlerFunc(s.handleAvatarGet)))
	m.Handle("POST /api/v1/settings/avatar", wrap(http.HandlerFunc(s.handleAvatarUpload)))
	m.Handle("DELETE /api/v1/settings/avatar", wrap(http.HandlerFunc(s.handleAvatarRemove)))

	// Docker
	m.Handle("GET /api/v1/docker/status", wrap(http.HandlerFunc(s.handleDockerStatus)))
	m.Handle("POST /api/v1/docker/install", wrap(http.HandlerFunc(s.handleDockerInstall)))
	m.Handle("GET /api/v1/docker/stats", wrap(http.HandlerFunc(s.handleDockerStats)))
	m.Handle("GET /api/v1/docker/containers", wrap(http.HandlerFunc(s.handleContainers)))
	m.Handle("POST /api/v1/docker/containers/{id}/{action}", wrap(http.HandlerFunc(s.handleContainerAction)))
	m.Handle("DELETE /api/v1/docker/containers/{id}", wrap(http.HandlerFunc(s.handleContainerRemove)))
	m.Handle("GET /api/v1/docker/containers/{id}/logs", wrap(http.HandlerFunc(s.handleContainerLogs)))
	m.Handle("GET /api/v1/docker/containers/{id}/info", wrap(http.HandlerFunc(s.handleContainerInfoDocker)))
	m.Handle("POST /api/v1/docker/containers/{id}/logs/clear", wrap(http.HandlerFunc(s.handleContainerLogClear)))
	m.Handle("GET /api/v1/docker/images", wrap(http.HandlerFunc(s.handleImages)))
	m.Handle("DELETE /api/v1/docker/images/{id}", wrap(http.HandlerFunc(s.handleImageRemove)))
	m.Handle("GET /api/v1/docker/networks", wrap(http.HandlerFunc(s.handleNetworks)))
	m.Handle("GET /api/v1/docker/volumes", wrap(http.HandlerFunc(s.handleVolumes)))
	m.Handle("POST /api/v1/docker/volumes/remove", wrap(http.HandlerFunc(s.handleVolumeRemove)))

	// 任务(安装/部署进度)
	m.Handle("GET /api/v1/tasks", wrap(http.HandlerFunc(s.handleTaskList)))
	m.Handle("GET /api/v1/tasks/{id}", wrap(http.HandlerFunc(s.handleTaskGet)))

	// 面板在线更新(对接 GitHub Releases)
	m.Handle("GET /api/v1/panel/update", wrap(http.HandlerFunc(s.handlePanelUpdateCheck)))
	m.Handle("POST /api/v1/panel/update/apply", wrap(http.HandlerFunc(s.handlePanelUpdateApply)))

	// 应用商店
	m.Handle("GET /api/v1/apps/templates", wrap(http.HandlerFunc(s.handleAppTemplates)))
	m.Handle("GET /api/v1/apps/hub-tags", wrap(http.HandlerFunc(s.handleAppHubTags)))
	m.Handle("GET /api/v1/apps/instances/{key}/runtime", wrap(http.HandlerFunc(s.handleAppRuntimeInfo)))
	m.Handle("POST /api/v1/apps/instances/{key}/runtime/{action}", wrap(http.HandlerFunc(s.handleAppRuntimeAction)))
	m.Handle("GET /api/v1/apps/instances", wrap(http.HandlerFunc(s.handleAppInstances)))
	m.Handle("POST /api/v1/apps/install", wrap(http.HandlerFunc(s.handleAppInstall)))
	m.Handle("POST /api/v1/apps/custom", wrap(http.HandlerFunc(s.handleCustomAdd)))
	m.Handle("DELETE /api/v1/apps/custom/{key}", wrap(http.HandlerFunc(s.handleCustomDelete)))
	m.Handle("POST /api/v1/apps/instances/{key}/{action}", wrap(http.HandlerFunc(s.handleAppAction)))

	// 网站
	m.Handle("GET /api/v1/websites", wrap(http.HandlerFunc(s.handleSiteList)))
	m.Handle("GET /api/v1/websites/nginx", wrap(http.HandlerFunc(s.handleNginxStatus)))
	m.Handle("GET /api/v1/websites/nginx-logs", wrap(http.HandlerFunc(s.handleNginxLogs)))
	m.Handle("POST /api/v1/websites/nginx-logs/clear", wrap(http.HandlerFunc(s.handleNginxLogsClear)))
	m.Handle("GET /api/v1/websites/nginx-logs/autoclean", wrap(http.HandlerFunc(s.handleNginxLogsAutoClean)))
	m.Handle("POST /api/v1/websites/nginx-logs/autoclean", wrap(http.HandlerFunc(s.handleNginxLogsAutoClean)))
	m.Handle("POST /api/v1/websites", wrap(http.HandlerFunc(s.handleSiteAdd)))
	m.Handle("POST /api/v1/websites/{id}/toggle", wrap(http.HandlerFunc(s.handleSiteToggle)))
	m.Handle("POST /api/v1/websites/{id}/ssl", wrap(http.HandlerFunc(s.handleSiteSSL)))
	m.Handle("DELETE /api/v1/websites/{id}", wrap(http.HandlerFunc(s.handleSiteDelete)))
	m.Handle("GET /api/v1/websites/{id}/backup", wrap(http.HandlerFunc(s.handleSiteBackup)))
	m.Handle("GET /api/v1/websites/{id}/backups", wrap(http.HandlerFunc(s.handleSiteBackupList)))
	m.Handle("POST /api/v1/websites/{id}/backups", wrap(http.HandlerFunc(s.handleSiteBackupCreate)))
	m.Handle("POST /api/v1/websites/{id}/backups/upload", wrap(http.HandlerFunc(s.handleSiteBackupUpload)))
	m.Handle("GET /api/v1/websites/{id}/backups/{file}/download", wrap(http.HandlerFunc(s.handleSiteBackupDownloadStored)))
	m.Handle("POST /api/v1/websites/{id}/backups/restore", wrap(http.HandlerFunc(s.handleSiteBackupRestore)))
	m.Handle("DELETE /api/v1/websites/{id}/backups/{file}", wrap(http.HandlerFunc(s.handleSiteBackupDelete)))
	m.Handle("GET /api/v1/files/search", wrap(http.HandlerFunc(s.handleFileSearch)))
	m.Handle("GET /api/v1/files/grep", wrap(http.HandlerFunc(s.handleFileGrep)))

	// 回收站
	m.Handle("GET /api/v1/files/trash", wrap(http.HandlerFunc(s.handleTrashList)))
	m.Handle("POST /api/v1/files/trash/restore", wrap(http.HandlerFunc(s.handleTrashRestore)))
	m.Handle("POST /api/v1/files/trash/delete", wrap(http.HandlerFunc(s.handleTrashDelete)))
	m.Handle("POST /api/v1/files/trash/clear", wrap(http.HandlerFunc(s.handleTrashClear)))

	// SSH 安全
	m.Handle("GET /api/v1/ssh", wrap(http.HandlerFunc(s.handleSSHStatus)))
	m.Handle("POST /api/v1/ssh/port", wrap(http.HandlerFunc(s.handleSSHPort)))
	m.Handle("POST /api/v1/ssh/hardening", wrap(http.HandlerFunc(s.handleSSHHardening)))
	m.Handle("POST /api/v1/ssh/keys", wrap(http.HandlerFunc(s.handleSSHKeyAdd)))
	m.Handle("DELETE /api/v1/ssh/keys/{index}", wrap(http.HandlerFunc(s.handleSSHKeyRemove)))

	// 站点密码访问
	m.Handle("POST /api/v1/websites/{id}/auth", wrap(http.HandlerFunc(s.handleSiteAuth)))
	// 站点域名管理 / 配置文件
	m.Handle("GET /api/v1/websites/{id}/domains", wrap(http.HandlerFunc(s.handleSiteDomains)))
	m.Handle("POST /api/v1/websites/{id}/domains", wrap(http.HandlerFunc(s.handleSiteDomains)))
	m.Handle("GET /api/v1/websites/{id}/conf", wrap(http.HandlerFunc(s.handleSiteConfGet)))
	m.Handle("POST /api/v1/websites/{id}/conf", wrap(http.HandlerFunc(s.handleSiteConfSave)))

	// SSL 证书
	m.Handle("GET /api/v1/ssl/certs", wrap(http.HandlerFunc(s.handleCertList)))
	m.Handle("POST /api/v1/ssl/upload", wrap(http.HandlerFunc(s.handleCertUpload)))
	m.Handle("POST /api/v1/ssl/selfsign", wrap(http.HandlerFunc(s.handleCertSelfSign)))
	m.Handle("DELETE /api/v1/ssl/certs/{id}", wrap(http.HandlerFunc(s.handleCertDelete)))
	m.Handle("GET /api/v1/ssl/certs/{id}/download", wrap(http.HandlerFunc(s.handleCertDownload)))

	// Let's Encrypt (ACME)
	m.Handle("GET /api/v1/ssl/acme/config", wrap(http.HandlerFunc(s.handleAcmeGet)))
	m.Handle("GET /api/v1/ssl/acme/providers", wrap(http.HandlerFunc(s.handleAcmeProviders)))
	m.Handle("POST /api/v1/ssl/acme/config", wrap(http.HandlerFunc(s.handleAcmeSet)))
	m.Handle("POST /api/v1/ssl/acme/issue", wrap(http.HandlerFunc(s.handleAcmeIssue)))
	m.Handle("POST /api/v1/ssl/acme/renew", wrap(http.HandlerFunc(s.handleAcmeRenew)))

	// 站点编辑
	m.Handle("PUT /api/v1/websites/{id}", wrap(http.HandlerFunc(s.handleSiteUpdate)))

	// 应用实例 compose 编辑
	m.Handle("GET /api/v1/apps/instances/{key}/compose", wrap(http.HandlerFunc(s.handleAppComposeGet)))
	m.Handle("GET /api/v1/apps/instances/{key}/container-info", wrap(http.HandlerFunc(s.handleAppContainerInfo)))
	m.Handle("GET /api/v1/apps/instances/{key}/container-log", wrap(http.HandlerFunc(s.handleAppContainerLog)))
	m.Handle("POST /api/v1/apps/instances/{key}/compose", wrap(http.HandlerFunc(s.handleAppComposeSave)))

	// Docker 镜像拉取 / 清理
	m.Handle("POST /api/v1/docker/pull", wrap(http.HandlerFunc(s.handleDockerPull)))
	m.Handle("POST /api/v1/docker/create", wrap(http.HandlerFunc(s.handleDockerCreate)))
	m.Handle("POST /api/v1/docker/prune", wrap(http.HandlerFunc(s.handleDockerPrune)))

	// 数据库快捷操作
	m.Handle("GET /api/v1/db/{key}/tables", wrap(http.HandlerFunc(s.handleDbTables)))
	m.Handle("POST /api/v1/db/{key}/table-data", wrap(http.HandlerFunc(s.handleDbTableData)))
	m.Handle("POST /api/v1/db/select", wrap(http.HandlerFunc(s.handleDbSelect)))
	m.Handle("POST /api/v1/db/{key}/create-db", wrap(http.HandlerFunc(s.handleDbCreate)))
	m.Handle("POST /api/v1/db/{key}/drop-db", wrap(http.HandlerFunc(s.handleDbDrop)))
	m.Handle("POST /api/v1/db/{key}/root-password", wrap(http.HandlerFunc(s.handleDbRootPassword)))

	// 网站流量统计 + 监控历史
	m.Handle("GET /api/v1/websites/web-stats", wrap(http.HandlerFunc(s.handleWebStats)))
	m.Handle("GET /api/v1/mon/history", wrap(http.HandlerFunc(s.handleMonHistory)))

	// 系统服务
	m.Handle("GET /api/v1/services", wrap(http.HandlerFunc(s.handleServicesList)))
	m.Handle("POST /api/v1/services/{name}/{action}", wrap(http.HandlerFunc(s.handleServiceAction)))

	// 数据库管理
	m.Handle("GET /api/v1/db/instances", wrap(http.HandlerFunc(s.handleDbInstances)))
	m.Handle("POST /api/v1/db/query", wrap(http.HandlerFunc(s.handleDbQuery)))
	m.Handle("GET /api/v1/db/{key}/databases", wrap(http.HandlerFunc(s.handleDbDatabases)))
	m.Handle("POST /api/v1/db/backup", wrap(http.HandlerFunc(s.handleDbBackup)))
	m.Handle("GET /api/v1/db/{key}/backups", wrap(http.HandlerFunc(s.handleDbBackupList)))
	m.Handle("GET /api/v1/db/{key}/backups/download", wrap(http.HandlerFunc(s.handleDbBackupDownload)))
	m.Handle("DELETE /api/v1/db/{key}/backups/{file}", wrap(http.HandlerFunc(s.handleDbBackupDelete)))

	// 面板备份/恢复 + 系统清理
	m.Handle("GET /api/v1/panel/backup", wrap(http.HandlerFunc(s.handlePanelBackup)))
	m.Handle("POST /api/v1/panel/restore", wrap(http.HandlerFunc(s.handlePanelRestore)))
	m.Handle("POST /api/v1/system/cleanup", wrap(http.HandlerFunc(s.handleSystemCleanup)))

	// 告警
	m.Handle("GET /api/v1/alert", wrap(http.HandlerFunc(s.handleAlertGet)))
	m.Handle("POST /api/v1/alert/settings", wrap(http.HandlerFunc(s.handleAlertSave)))
	m.Handle("POST /api/v1/alert/test", wrap(http.HandlerFunc(s.handleAlertTest)))

	// 文件
	m.Handle("GET /api/v1/files", wrap(http.HandlerFunc(s.handleFileList)))
	m.Handle("GET /api/v1/files/content", wrap(http.HandlerFunc(s.handleFileContent)))
	m.Handle("POST /api/v1/files/save", wrap(http.HandlerFunc(s.handleFileSave)))
	m.Handle("GET /api/v1/files/history", wrap(http.HandlerFunc(s.handleFileHistory)))
	m.Handle("GET /api/v1/files/history/content", wrap(http.HandlerFunc(s.handleFileHistoryContent)))
	m.Handle("POST /api/v1/files/history/restore", wrap(http.HandlerFunc(s.handleFileHistoryRestore)))
	m.Handle("POST /api/v1/files/mkdir", wrap(http.HandlerFunc(s.handleFileMkdir)))
	m.Handle("POST /api/v1/files/rename", wrap(http.HandlerFunc(s.handleFileRename)))
	m.Handle("POST /api/v1/files/delete", wrap(http.HandlerFunc(s.handleFileDelete)))
	m.Handle("POST /api/v1/files/copy", wrap(http.HandlerFunc(s.handleFileCopy)))
	m.Handle("POST /api/v1/files/move", wrap(http.HandlerFunc(s.handleFileMove)))
	m.Handle("POST /api/v1/files/chmod", wrap(http.HandlerFunc(s.handleFileChmod)))
	m.Handle("POST /api/v1/files/compress", wrap(http.HandlerFunc(s.handleFileCompress)))
	m.Handle("POST /api/v1/files/extract", wrap(http.HandlerFunc(s.handleFileExtract)))
	m.Handle("GET /api/v1/files/download", wrap(http.HandlerFunc(s.handleFileDownload)))
	m.Handle("POST /api/v1/files/upload", wrap(http.HandlerFunc(s.handleFileUpload)))
	m.Handle("POST /api/v1/files/remote-download", wrap(http.HandlerFunc(s.handleFileRemoteDownload)))
	m.Handle("GET /api/v1/files/image-preview", wrap(http.HandlerFunc(s.handleFileImagePreview)))

	// 计划任务
	m.Handle("GET /api/v1/crons", wrap(http.HandlerFunc(s.handleCronList)))
	m.Handle("POST /api/v1/crons", wrap(http.HandlerFunc(s.handleCronAdd)))
	m.Handle("PUT /api/v1/crons", wrap(http.HandlerFunc(s.handleCronUpdate)))
	m.Handle("DELETE /api/v1/crons/{id}", wrap(http.HandlerFunc(s.handleCronDelete)))
	m.Handle("POST /api/v1/crons/{id}/run", wrap(http.HandlerFunc(s.handleCronRun)))
	m.Handle("POST /api/v1/crons/{id}/toggle", wrap(http.HandlerFunc(s.handleCronToggle)))
	m.Handle("GET /api/v1/crons/{id}/log", wrap(http.HandlerFunc(s.handleCronLog)))
	m.Handle("POST /api/v1/crons/{id}/log/clear", wrap(http.HandlerFunc(s.handleCronLogClear)))
	m.Handle("GET /api/v1/remote-backup/settings", wrap(http.HandlerFunc(s.handleRemoteGet)))
	m.Handle("POST /api/v1/remote-backup/settings", wrap(http.HandlerFunc(s.handleRemoteSave)))
	m.Handle("POST /api/v1/remote-backup/test", wrap(http.HandlerFunc(s.handleRemoteTest)))
	m.Handle("POST /api/v1/panel/backup/remote", wrap(http.HandlerFunc(s.handlePanelBackupRemote)))
	m.Handle("POST /api/v1/db/import", wrap(http.HandlerFunc(s.handleDbImport)))
	m.Handle("POST /api/v1/db/import-backup", wrap(http.HandlerFunc(s.handleDbImportBackup)))
	m.Handle("GET /api/v1/notifications", wrap(http.HandlerFunc(s.handleNotifyList)))
	m.Handle("GET /api/v1/panel/ssl", wrap(http.HandlerFunc(s.handlePanelSSLGet)))
	m.Handle("POST /api/v1/panel/ssl", wrap(http.HandlerFunc(s.handlePanelSSLSave)))
	m.Handle("POST /api/v1/panel/ssl/selfsign", wrap(http.HandlerFunc(s.handlePanelSSLSelfSign)))
	m.Handle("GET /api/v1/panel/checkup", wrap(http.HandlerFunc(s.handlePanelCheckup)))
	m.Handle("POST /api/v1/notifications/clear", wrap(http.HandlerFunc(s.handleNotifyClear)))
	m.Handle("POST /api/v1/websites/{id}/hotlink", wrap(http.HandlerFunc(s.handleSiteHotlink)))
	m.Handle("POST /api/v1/websites/{id}/extras", wrap(http.HandlerFunc(s.handleSiteExtras)))
	m.Handle("GET /api/v1/websites/gzip", wrap(http.HandlerFunc(s.handleGzipGet)))
	m.Handle("POST /api/v1/websites/gzip", wrap(http.HandlerFunc(s.handleGzipSet)))
	m.Handle("GET /api/v1/websites/{id}/rewrite", wrap(http.HandlerFunc(s.handleSiteRewriteGet)))
	m.Handle("POST /api/v1/websites/{id}/rewrite", wrap(http.HandlerFunc(s.handleSiteRewriteSave)))
	m.Handle("GET /api/v1/websites/{id}/upstreams", wrap(http.HandlerFunc(s.handleSiteUpstreamsGet)))
	m.Handle("POST /api/v1/websites/{id}/upstreams", wrap(http.HandlerFunc(s.handleSiteUpstreamsSave)))
	m.Handle("GET /api/v1/websites/anti-parsing", wrap(http.HandlerFunc(s.handleAntiParsingGet)))
	m.Handle("POST /api/v1/websites/anti-parsing", wrap(http.HandlerFunc(s.handleAntiParsingSet)))

	// 日志
	m.Handle("GET /api/v1/logs/{kind}", wrap(http.HandlerFunc(s.handleLogs)))
	m.Handle("GET /api/v1/logs/{kind}/export", wrap(http.HandlerFunc(s.handleLogsExport)))
	m.Handle("POST /api/v1/logs/{kind}/clear", wrap(http.HandlerFunc(s.handleLogsClear)))

	// 插件系统
	m.Handle("GET /api/v1/plugins", wrap(http.HandlerFunc(s.handlePluginList)))
	m.Handle("POST /api/v1/plugins/{key}/{action}", wrap(http.HandlerFunc(s.handlePluginAction)))

	// WAF 防火墙
	m.Handle("GET /api/v1/waf", wrap(http.HandlerFunc(s.handleWafInfo)))
	m.Handle("POST /api/v1/waf/settings", wrap(http.HandlerFunc(s.handleWafSettings)))
	m.Handle("GET /api/v1/waf/logs", wrap(http.HandlerFunc(s.handleWafLogs)))
	m.Handle("GET /api/v1/migration/bt/scan", wrap(http.HandlerFunc(s.handleMigrateScan)))
	m.Handle("POST /api/v1/migration/bt/apply", wrap(http.HandlerFunc(s.handleMigrateApply)))
	m.Handle("POST /api/v1/migration/bt/switch", wrap(http.HandlerFunc(s.handleMigrateSwitch)))
	m.Handle("POST /api/v1/migration/bt/rollback", wrap(http.HandlerFunc(s.handleMigrateRollback)))
	m.Handle("GET /api/v1/migration/bt/status", wrap(http.HandlerFunc(s.handleMigrateStatus)))
	m.Handle("GET /api/v1/waf/apache", wrap(http.HandlerFunc(s.handleWafApacheGet)))
	m.Handle("POST /api/v1/waf/apache", wrap(http.HandlerFunc(s.handleWafApacheSet)))
	m.Handle("POST /api/v1/waf/logs/clear", wrap(http.HandlerFunc(s.handleWafLogsClear)))
	m.Handle("GET /api/v1/waf/stats", wrap(http.HandlerFunc(s.handleWafStats)))

	// 终端(WebSocket,自鉴权:一次性票据或访问令牌)
	m.Handle("GET /api/v1/terminal", http.HandlerFunc(s.handleTerminal))
	m.Handle("POST /api/v1/terminal/ticket", wrap(http.HandlerFunc(s.handleTerminalTicket)))

	// 拨测监控
	m.Handle("GET /api/v1/uptime/monitors", wrap(http.HandlerFunc(s.handleUpmonList)))
	m.Handle("POST /api/v1/uptime/monitors", wrap(http.HandlerFunc(s.handleUpmonAdd)))
	m.Handle("POST /api/v1/uptime/monitors/{id}/toggle", wrap(http.HandlerFunc(s.handleUpmonToggle)))
	m.Handle("DELETE /api/v1/uptime/monitors/{id}", wrap(http.HandlerFunc(s.handleUpmonDelete)))

	// 网络工具箱
	m.Handle("POST /api/v1/nettools/ping", wrap(http.HandlerFunc(s.handleNetPing)))
	m.Handle("POST /api/v1/nettools/trace", wrap(http.HandlerFunc(s.handleNetTrace)))
	m.Handle("POST /api/v1/nettools/dns", wrap(http.HandlerFunc(s.handleNetDNS)))
	m.Handle("POST /api/v1/nettools/portscan", wrap(http.HandlerFunc(s.handleNetPortScan)))
	m.Handle("POST /api/v1/nettools/http", wrap(http.HandlerFunc(s.handleNetHTTP)))

	// 进程管理器
	m.Handle("GET /api/v1/processes", wrap(http.HandlerFunc(s.handleProcList)))
	m.Handle("POST /api/v1/processes/kill", wrap(http.HandlerFunc(s.handleProcKill)))

	// 防火墙管理
	m.Handle("GET /api/v1/firewall", wrap(http.HandlerFunc(s.handleFwStatus)))
	m.Handle("POST /api/v1/firewall/allow", wrap(http.HandlerFunc(s.handleFwAllow)))
	m.Handle("POST /api/v1/firewall/deny", wrap(http.HandlerFunc(s.handleFwDeny)))
	m.Handle("POST /api/v1/firewall/toggle", wrap(http.HandlerFunc(s.handleFwToggle)))
	m.Handle("POST /api/v1/firewall/install", wrap(http.HandlerFunc(s.handleFwInstall)))
	m.Handle("GET /api/v1/firewall/docker-ports", wrap(http.HandlerFunc(s.handleFwDockerPorts)))
	m.Handle("POST /api/v1/firewall/docker-block", wrap(http.HandlerFunc(s.handleFwDockerBlock)))
	m.Handle("GET /api/v1/firewall/ping", wrap(http.HandlerFunc(s.handleFwPingStatus)))
	m.Handle("POST /api/v1/firewall/ping", wrap(http.HandlerFunc(s.handleFwPing)))
	m.Handle("GET /api/v1/waf/site-off", wrap(http.HandlerFunc(s.handleWafSiteOff)))
	m.Handle("POST /api/v1/waf/site-toggle", wrap(http.HandlerFunc(s.handleWafSiteToggle)))
	m.Handle("GET /api/v1/waf/auto-ban", wrap(http.HandlerFunc(s.handleWafAutoBanStatus)))
	m.Handle("POST /api/v1/waf/auto-ban/run", wrap(http.HandlerFunc(s.handleWafAutoBanRun)))
	m.Handle("POST /api/v1/waf/auto-ban/clear", wrap(http.HandlerFunc(s.handleWafAutoBanClear)))
}
