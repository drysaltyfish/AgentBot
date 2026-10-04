@echo off
chcp 65001 >nul
setlocal EnableDelayedExpansion
title AgentBot 一键启动

REM ============================================================
REM  AgentBot 一键启动：登录 QQ(NapCat) -> 等 OneBot 端口 -> 起 AgentBot
REM
REM  用法：
REM    start.bat              正常启动
REM    start.bat --no-napcat  只起 AgentBot（NapCat 已在跑）
REM    start.bat --build      先重新编译再启动
REM ============================================================

REM ---- 路径配置（换机器只改这三行）----
set "BOT_DIR=C:\AgentBot"
set "NAPCAT_DIR=C:\NapCat.Shell"
set "QQ_ACCOUNT=10001"

set "NO_NAPCAT="
set "DO_BUILD="
for %%a in (%*) do (
    if /I "%%a"=="--no-napcat" set "NO_NAPCAT=1"
    if /I "%%a"=="--build"     set "DO_BUILD=1"
)

echo.
echo ============================================================
echo   AgentBot 启动中
echo ============================================================
echo.

cd /d "!BOT_DIR!"
if errorlevel 1 (
    echo [错误] 找不到目录 !BOT_DIR!
    pause & exit /b 1
)

if not exist "config.yaml" (
    echo [错误] 缺少 config.yaml
    echo        先复制：copy config.example.yaml config.yaml
    pause & exit /b 1
)

REM ---- 1/5 编译（有现成的就跳过）----
if "!DO_BUILD!"=="1" goto do_build
if exist "bin\agentbot.exe" (
    echo [1/5] 使用已有 bin\agentbot.exe  ^(重新编译请加 --build^)
    goto after_build
)
:do_build
echo [1/5] 编译中...
go build -o bin\agentbot.exe ./cmd/server
if errorlevel 1 (
    echo [错误] 编译失败
    pause & exit /b 1
)
:after_build

REM ---- 2/5 校验配置 ----
echo [2/5] 校验配置...
"bin\agentbot.exe" --config config.yaml --check-config >nul 2>"%TEMP%\agentbot_check.txt"
if errorlevel 1 (
    echo.
    echo [错误] 配置校验失败：
    type "%TEMP%\agentbot_check.txt"
    del "%TEMP%\agentbot_check.txt" >nul 2>&1
    echo.
    pause & exit /b 1
)
del "%TEMP%\agentbot_check.txt" >nul 2>&1
echo       配置 OK

REM ---- 3/5 已有实例？ ----
tasklist /FI "IMAGENAME eq agentbot.exe" 2>nul | find /I "agentbot.exe" >nul
if not errorlevel 1 (
    echo.
    echo [提示] agentbot.exe 已在运行。要重启请先运行 stop.bat
    echo.
    pause & exit /b 0
)

REM ---- 4/5 NapCat：没在监听 3001 就拉起来（需管理员权限）----
if "!NO_NAPCAT!"=="1" (
    echo [3/5] 跳过 NapCat ^(--no-napcat^)
    goto wait_port
)
netstat -ano | findstr /R /C:"127.0.0.1:3001 .*LISTENING" >nul
if not errorlevel 1 (
    echo [3/5] NapCat 已在监听 3001，跳过启动
    goto wait_port
)
echo [3/5] 启动 NapCat ^(会弹 UAC，请点「是」^)...
if not exist "!NAPCAT_DIR!\quickLogin.bat" (
    echo [错误] 找不到 !NAPCAT_DIR!\quickLogin.bat
    pause & exit /b 1
)
start "NapCat" /D "!NAPCAT_DIR!" cmd /c "quickLogin.bat !QQ_ACCOUNT!"

:wait_port
echo [4/5] 等待 OneBot 端口 3001 ...
set /a WAIT=0
:wait_loop
netstat -ano | findstr /R /C:"127.0.0.1:3001 .*LISTENING" >nul
if not errorlevel 1 goto port_ready
set /a WAIT+=1
if !WAIT! GEQ 90 goto port_timeout
timeout /t 1 /nobreak >nul
<nul set /p "=."
goto wait_loop

:port_timeout
echo.
echo [警告] 90 秒内没看到 3001 监听：
echo        - NapCat 若弹了登录窗口，请先完成登录
echo        - 仍会启动 AgentBot（它会自动重连）

:port_ready
echo.
echo       OneBot 端口就绪

REM ---- 5/5 启动 AgentBot（独立窗口 + 日志落盘）----
echo [5/5] 启动 AgentBot ...
REM 用 PowerShell 的 Start-Process：cmd 的 start 在这里要同时处理
REM 「工作目录 + 重定向 + 引号嵌套」，实测会静默失败（窗口一闪、进程没起）。
REM Start-Process 的 -WorkingDirectory / -RedirectStandardOutput 把这些拆成显式参数，
REM 既不依赖引号层次，也省掉一个中间 cmd 进程。
powershell -NoProfile -Command "Start-Process -FilePath 'bin\agentbot.exe' -ArgumentList '--config','config.yaml' -WorkingDirectory '!BOT_DIR!' -RedirectStandardOutput '!BOT_DIR!\bin\run.log' -RedirectStandardError '!BOT_DIR!\bin\run.err' -WindowStyle Minimized"

timeout /t 5 /nobreak >nul
tasklist /FI "IMAGENAME eq agentbot.exe" 2>nul | find /I "agentbot.exe" >nul
if errorlevel 1 (
    echo.
    echo [错误] AgentBot 启动失败，请查看 bin\run.log
    pause & exit /b 1
)

echo.
echo ============================================================
echo   已启动
echo     AgentBot    窗口「AgentBot」；日志追加到 bin\run.log
echo     就绪探针    http://127.0.0.1:9090/readyz
echo     指标        http://127.0.0.1:9090/metrics
echo     NapCat 面板 http://127.0.0.1:6099
echo.
echo   停止：stop.bat
echo   看日志：type bin\run.log
echo ============================================================
echo.
pause
endlocal
