package server

// phpExtMeta PHP 扩展元数据:安装方式(bundled=源码树内置 / pecl=PECL 仓库)与所需系统依赖
type phpExtMeta struct {
	Pecl bool   `json:"pecl"`           // true=pecl install,false=docker-php-ext-install
	Deps string `json:"deps,omitempty"` // 需要先 apt 安装的系统库(空格分隔)
	Conf string `json:"conf,omitempty"` // docker-php-ext-configure 附加参数(如 gd 的 freetype/jpeg)
	Desc string `json:"desc"`
	Cat  string `json:"cat"`
}

// phpExtCatalog 可安装扩展目录(借鉴宝塔面板;条目在 PHP 8.x 官方 Docker 镜像上可编译)
func phpExtCatalog() map[string]phpExtMeta {
	return map[string]phpExtMeta{
		// ---- 数据库 ----
		"mysqli":    {Desc: "MySQL 改进版扩展(常与 pdo_mysql 二选一)", Cat: "数据库"},
		"pdo_mysql": {Desc: "PDO MySQL 驱动(推荐,主流框架使用)", Cat: "数据库"},
		"pgsql":     {Deps: "libpq-dev", Desc: "PostgreSQL 数据库连接", Cat: "数据库"},
		"pdo_pgsql": {Deps: "libpq-dev", Desc: "PDO PostgreSQL 驱动", Cat: "数据库"},
		"pdo_dblib": {Deps: "freetds-dev", Desc: "PDO MSSQL/Sybase 驱动", Cat: "数据库"},
		"redis":     {Pecl: true, Desc: "Redis 客户端(缓存/队列最常用)", Cat: "数据库"},
		"mongodb":   {Pecl: true, Desc: "MongoDB 客户端", Cat: "数据库"},
		"rdkafka":   {Pecl: true, Deps: "librdkafka-dev", Desc: "Kafka 消息队列客户端", Cat: "数据库"},
		// ---- 缓存 / 性能 ----
		"opcache":   {Desc: "字节码缓存(生产环境强烈推荐)", Cat: "缓存/性能"},
		"apcu":      {Pecl: true, Desc: "本机用户缓存(小型站点秒级提速)", Cat: "缓存/性能"},
		"memcached": {Pecl: true, Deps: "libmemcached-dev zlib1g-dev", Desc: "Memcached 客户端", Cat: "缓存/性能"},
		"igbinary":  {Pecl: true, Desc: "高效二进制序列化(配合 redis 使用)", Cat: "缓存/性能"},
		"msgpack":   {Pecl: true, Desc: "MessagePack 序列化", Cat: "缓存/性能"},
		"yac":       {Pecl: true, Desc: "无锁共享内存缓存", Cat: "缓存/性能"},
		// ---- 图像 ----
		"gd":      {Deps: "libfreetype6-dev libjpeg62-turbo-dev libpng-dev", Conf: "--with-freetype --with-jpeg", Desc: "图像处理(验证码/缩略图/水印)", Cat: "图像"},
		"imagick": {Pecl: true, Deps: "libmagickwand-dev", Desc: "ImageMagick 图像处理(格式全/质量高)", Cat: "图像"},
		"exif":    {Desc: "读取照片 EXIF 信息", Cat: "图像"},
		// ---- 压缩 / 归档 ----
		"zip": {Deps: "libzip-dev", Desc: "ZIP 压缩包读写", Cat: "压缩/归档"},
		"bz2": {Deps: "libbz2-dev", Desc: "bzip2 压缩", Cat: "压缩/归档"},
		// ---- 国际化 / 文本 ----
		"intl":    {Deps: "libicu-dev", Desc: "国际化(多语言排序/日期/数字格式化)", Cat: "国际化/文本"},
		"gettext": {Desc: "多语言翻译(i18n)", Cat: "国际化/文本"},
		"gmp":     {Deps: "libgmp-dev", Desc: "大数精确运算", Cat: "国际化/文本"},
		"bcmath":  {Desc: "任意精度数学(金额计算必备)", Cat: "国际化/文本"},
		"pspell":  {Deps: "libpspell-dev aspell-en", Desc: "拼写检查", Cat: "国际化/文本"},
		"xmlrpc":  {Pecl: true, Desc: "XML-RPC 协议(PHP 8 移出源码树)", Cat: "国际化/文本"},
		// ---- 网络 / 通信 ----
		"ftp":     {Desc: "FTP 客户端", Cat: "网络/通信"},
		"ldap":    {Deps: "libldap2-dev libsasl2-dev", Desc: "LDAP 目录服务(企业域认证)", Cat: "网络/通信"},
		"snmp":    {Deps: "libsnmp-dev", Desc: "SNMP 网络管理", Cat: "网络/通信"},
		"sockets": {Desc: "原生 Socket 编程", Cat: "网络/通信"},
		// ---- Web / 文档 ----
		"soap":      {Deps: "libxml2-dev", Desc: "SOAP WebService", Cat: "Web/文档"},
		"xsl":       {Deps: "libxslt1-dev", Desc: "XSLT 样式表转换", Cat: "Web/文档"},
		"tidy":      {Deps: "libtidy-dev", Desc: "HTML 清理与修复", Cat: "Web/文档"},
		"xlswriter": {Pecl: true, Deps: "zlib1g-dev", Desc: "高性能 Excel 读写", Cat: "Web/文档"},
		// ---- 系统 / 进程 ----
		"pcntl":    {Desc: "多进程控制(队列/守护进程)", Cat: "系统/进程"},
		"posix":    {Desc: "POSIX 系统接口", Cat: "系统/进程"},
		"shmop":    {Desc: "共享内存段操作", Cat: "系统/进程"},
		"sysvmsg":  {Desc: "System V 消息队列", Cat: "系统/进程"},
		"sysvsem":  {Desc: "System V 信号量", Cat: "系统/进程"},
		"sysvshm":  {Desc: "System V 共享内存", Cat: "系统/进程"},
		"dba":      {Desc: "dbm 键值数据库抽象层", Cat: "系统/进程"},
		"calendar": {Desc: "历法转换函数", Cat: "系统/进程"},
		// ---- 调试 / 工具 ----
		"xdebug": {Pecl: true, Desc: "调试分析器(仅开发环境安装)", Cat: "调试/工具"},
	}
}
