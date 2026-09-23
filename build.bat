@echo off
chcp 65001 >nul
setlocal

rem 定位 Go 工具链（首次已安装到 %USERPROFILE%\.go-sdk\go）
set GOROOT=%USERPROFILE%\.go-sdk\go
if not exist "%GOROOT%\bin\go.exe" (
    echo [错误] 未找到 Go 工具链：%GOROOT%\bin\go.exe
    echo 请先安装 Go 1.27+ 或修改本脚本中的 GOROOT 路径。
    pause
    exit /b 1
)
set PATH=%GOROOT%\bin;%USERPROFILE%\go\bin;%PATH%
set GOPROXY=https://goproxy.cn,direct

cd /d "%~dp0"

echo [1/3] 生成资源文件（manifest 视觉样式 + app.ico 图标，缺图标会导致任务栏/快捷方式显示默认图标）...
rsrc -manifest app.manifest -ico app.ico -o rsrc.syso
if errorlevel 1 goto :fail

echo [2/3] 整理依赖...
go mod tidy
if errorlevel 1 goto :fail

echo [3/3] 编译单文件 exe（无控制台窗口）...
go build -ldflags "-H windowsgui -s -w" -o ApiCluster.exe .
if errorlevel 1 goto :fail

echo.
echo ============================================
echo  编译成功：%~dp0ApiCluster.exe
echo  双击运行，桌面窗口 + 系统托盘图标。
echo ============================================
pause
exit /b 0

:fail
echo.
echo 编译失败，请检查上方错误信息。
pause
exit /b 1
