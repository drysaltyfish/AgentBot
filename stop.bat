@echo off
chcp 65001 >nul
setlocal EnableDelayedExpansion
title AgentBot 停止

REM ============================================================
REM  停止 AgentBot（以及可选的 NapCat / QQ）
REM    用法：
REM      stop.bat            只停 AgentBot
REM      stop.bat --all      连 NapCat 与 QQ 一起停
REM ============================================================

set "STOP_ALL="
for %%a in (%*) do (
    if /I "%%a"=="--all" set "STOP_ALL=1"
)

echo.
echo [1/2] 停止 AgentBot ...
tasklist /FI "IMAGENAME eq agentbot.exe" 2>nul | find /I "agentbot.exe" >nul
if errorlevel 1 (
    echo       AgentBot 未在运行
) else (
    taskkill /IM agentbot.exe /F >nul 2>&1
    if errorlevel 1 (
        echo       [警告] 停止失败，可能需要管理员权限
    ) else (
        echo       已停止
    )
)

if "!STOP_ALL!"=="" (
    echo.
    echo 提示：加 --all 可连同 NapCat 与 QQ 一起停
    echo.
    pause & exit /b 0
)

echo [2/2] 停止 NapCat 与 QQ ...
taskkill /IM NapCatWinBootMain.exe /F >nul 2>&1
if not errorlevel 1 echo       NapCat 已停止

REM QQ 有多个进程；依次结束属正常。KillQQ.bat 是 NapCat 自带的。
taskkill /IM QQ.exe /F >nul 2>&1
if errorlevel 1 (
    echo       QQ 未在运行或已停止
) else (
    echo       QQ 已停止
)

echo.
echo ============================================================
echo   已全部停止
echo ============================================================
echo.
pause
endlocal
