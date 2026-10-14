@echo off
rem ============================================================
rem  坤面板 KunPanel - Windows 一键安装入口
rem  右键"以管理员身份运行"本文件即可
rem ============================================================
chcp 65001 >nul
net session >nul 2>&1
if %errorlevel% neq 0 (
  echo [信息] 正在请求管理员权限 ...
  powershell -NoProfile -Command "Start-Process cmd -Verb RunAs -ArgumentList '/c \"%~f0\" %*'"
  exit /b
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
pause
