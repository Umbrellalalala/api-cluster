// ApiCluster：本地 OpenAI 兼容聚合代理（桌面版）。
// WebView2 桌面窗口 + 系统托盘常驻，代理入口 http://localhost:3003/v1。
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"github.com/lxn/win"

	"apicluster/internal/config"
	"apicluster/internal/proxy"
	"apicluster/internal/sidecar"
	"apicluster/internal/tunnel"
	"apicluster/internal/webui"
)

const (
	wmShowApp uint32 = win.WM_APP + 0x100 // 自定义消息：显示主窗口
	wmTrayMsg uint32 = win.WM_APP + 0x200 // 托盘图标回调消息
	wmSetIcon uint32 = 0x0080             // WM_SETICON：设置标题栏/任务栏图标
)

const (
	trayCmdOpen    = 1001
	trayCmdRestart = 1002
	trayCmdQuit    = 1003
)

var (
	hwndPtr   atomic.Uintptr
	quitting  atomic.Bool
	oldProc   atomic.Uintptr
	trayNID   win.NOTIFYICONDATA
	trayAdded bool
)

func main() {
	hidden := flag.Bool("hidden", false, "静默启动（开机自启，仅驻留托盘）")
	flag.Parse()

	// 日志写入文件（windowsgui 无控制台）
	logFile, err := os.OpenFile(filepath.Join(config.DataDir(), "apicluster.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		log.SetOutput(logFile)
	}
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// 单实例保护：已有实例运行时，激活其主窗口后本进程直接退出，
	// 从根源上杜绝「同时打开多个」导致的端口占用报错。
	acquireSingleInstance(*hidden)

	_, _, _ = syscall.NewLazyDLL("user32.dll").NewProc("SetProcessDPIAware").Call()

	// 用量统计 / key 轮换位置等高频内存变更定时落盘，崩溃或断电最多丢 60 秒
	config.StartAutoFlush(60 * time.Second)

	cfg := config.Load()
	port := cfg.Settings.ProxyPort
	if port == 0 {
		port = 3003
	}

	// 内置 CLIProxyAPI 边车（订阅账号 OAuth 池）
	sc := sidecar.New()

	// SSH 反向隧道（远程访问本地 API）
	tun := tunnel.New()

	// 本地 HTTP 服务：/v1/* 代理 + UI/API
	serverDone, err := startServer(port, sc, tun)
	if err != nil {
		log.Printf("启动失败：%v", err)
		showError("ApiCluster 启动失败",
			fmt.Sprintf("无法监听本地端口 %d：\n%v\n\n请确认没有其他 ApiCluster 实例正在运行（可检查系统托盘），然后重试。", port, err))
		os.Exit(1)
	}

	// 若已启用则拉起边车（不阻塞窗口显示）
	go sc.StartIfEnabled()

	// 若已启用则自动建立 SSH 反向隧道（不阻塞窗口显示）
	go tun.StartIfEnabled()

	// 优雅退出：捕获 SIGINT / SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("收到信号 %v，正在关闭...", sig)
		sc.Stop()
		tun.Stop()
		close(serverDone)
	}()

	// WebView2 桌面窗口（主线程）
	w := webview.NewWithOptions(webview.WebViewOptions{
		WindowOptions: webview.WindowOptions{
			Title:  "ApiCluster - 本地聚合代理",
			Width:  1280,
			Height: 860,
			Center: true,
		},
	})
	if w == nil {
		// WebView2 Runtime 缺失 → 降级用系统浏览器
		log.Printf("WebView2 不可用，降级为系统浏览器打开管理界面")
		openExternal(fmt.Sprintf("http://localhost:%d", port))
		// 保持代理运行
		select {}
	}
	defer w.Destroy()

	h := uintptr(w.Window())
	hwndPtr.Store(h)
	subclassWndProc(h)
	setWindowIcon(h)
	initTray(h)

	if *hidden {
		// 静默启动（开机自启）：尽早隐藏以减少闪烁，并在消息循环启动后于
		// UI 线程上再隐藏一次——窗口的初始显示流程会覆盖过早的 ShowWindow，
		// 否则开机自启会弹出独立窗口而不是只驻留托盘。
		win.ShowWindow(win.HWND(h), win.SW_HIDE)
		go func() {
			time.Sleep(150 * time.Millisecond)
			w.Dispatch(func() {
				win.ShowWindow(win.HWND(h), win.SW_HIDE)
			})
		}()
	}
	w.Navigate(fmt.Sprintf("http://127.0.0.1:%d", port))

	// 自愈：若 LifeSystem 内嵌宿主被强杀，及时把本窗口还原为独立窗口继续运行
	go watchParentDeath(w, h)

	if !*hidden {
		// 默认最大化（全屏）打开。窗口的初始显示流程会覆盖过早的 ShowWindow，
		// 因此在消息循环启动后于 UI 线程上再最大化一次，保证稳定生效。
		go func() {
			time.Sleep(150 * time.Millisecond)
			w.Dispatch(func() {
				win.ShowWindow(win.HWND(h), win.SW_MAXIMIZE)
			})
		}()
	}
	w.Run()

	// 窗口关闭（退出）：一并结束边车与隧道并落盘配置
	sc.Stop()
	tun.Stop()
	_ = config.Save()
}

// acquireSingleInstance 单实例保护。
// 用命名互斥体（Local\ 前缀 = 每用户会话内唯一）保证同一时间只有一个 ApiCluster 实例；
// 若已有实例在运行：
//   - 手动启动（非 -hidden）：向已有实例的主窗口投递 wmShowApp，把窗口带到前台（托盘中也找得到）；
//   - 开机自启（-hidden）：静默退出，不打扰。
//
// 互斥体句柄持有到进程退出，由操作系统自动释放（崩溃/强杀也不会泄漏）。
func acquireSingleInstance(hidden bool) {
	const mutexName = `Local\ApiCluster.SingleInstance`
	createMutexW := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW")
	namePtr, _ := syscall.UTF16PtrFromString(mutexName)
	_, _, err := createMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if !errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
		return // 首个实例：继续正常启动
	}

	// 已有实例在运行：尝试把它的主窗口带到前台。
	// 注意：窗口可能正被 LifeSystem 内嵌（SetParent 成子窗口），此时 FindWindowW
	// 找不到顶层窗口，需递归枚举子窗口定位；wmShowApp 会触发 detachFromEmbed 解绑显示。
	if !hidden {
		if hwnd := findWindowByTitle("ApiCluster - 本地聚合代理"); hwnd != 0 {
			win.PostMessage(win.HWND(hwnd), wmShowApp, 0, 0)
		}
	}
	log.Printf("检测到已有 ApiCluster 实例运行，本次启动直接退出（单实例保护）")
	os.Exit(0)
}

// findWindowByTitle 枚举所有顶层窗口及其子窗口，返回标题精确匹配的窗口句柄。
// 相比 FindWindowW 只找顶层窗口，这里能定位到被 SetParent 内嵌的子窗口。
func findWindowByTitle(title string) uintptr {
	user32 := syscall.NewLazyDLL("user32.dll")
	enumWindows := user32.NewProc("EnumWindows")
	enumChildWindows := user32.NewProc("EnumChildWindows")
	getTextLen := user32.NewProc("GetWindowTextLengthW")
	getText := user32.NewProc("GetWindowTextW")

	var found uintptr

	match := func(hwnd uintptr) bool {
		n, _, _ := getTextLen.Call(hwnd)
		if n == 0 {
			return false
		}
		buf := make([]uint16, n+1)
		getText.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
		return syscall.UTF16ToString(buf) == title
	}

	// 1) 顶层窗口
	cbTop := syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
		if match(hwnd) {
			found = hwnd
			return 0 // 停止枚举
		}
		return 1
	})
	enumWindows.Call(cbTop, 0)
	if found != 0 {
		return found
	}

	// 2) 各顶层窗口的子窗口（含被内嵌的窗口）
	cbParent := syscall.NewCallback(func(parent, lparam uintptr) uintptr {
		if found != 0 {
			return 0
		}
		cbChild := syscall.NewCallback(func(hwnd, lp uintptr) uintptr {
			if match(hwnd) {
				found = hwnd
				return 0
			}
			return 1
		})
		enumChildWindows.Call(parent, cbChild, 0)
		return 1
	})
	enumWindows.Call(cbParent, 0)
	return found
}

// setWindowIcon 设置窗口标题栏与任务栏图标
func setWindowIcon(h uintptr) {
	icon := loadTrayIcon()
	if icon == 0 {
		return
	}
	win.SendMessage(win.HWND(h), wmSetIcon, 0, uintptr(icon))
	win.SendMessage(win.HWND(h), wmSetIcon, 1, uintptr(icon))
}

// subclassWndProc 子类化窗口过程：点 X 关闭 = 隐藏到托盘；退出时放行
func subclassWndProc(h uintptr) {
	cb := syscall.NewCallback(func(hwin win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case win.WM_CLOSE:
			if !quitting.Load() {
				win.ShowWindow(hwin, win.SW_HIDE)
				return 0
			}
		case wmShowApp:
			showWindowFromTray(hwin)
			return 0
		case wmTrayMsg:
			switch uint32(lParam) {
			case win.WM_LBUTTONUP: // 左键：直接打开窗口
				showWindowFromTray(hwin)
			case win.WM_RBUTTONUP: // 右键：弹出菜单
				showTrayMenu(hwin)
			}
			return 0
		case win.WM_COMMAND:
			switch uint32(wParam & 0xFFFF) {
			case trayCmdOpen:
				showWindowFromTray(hwin)
			case trayCmdRestart:
				restartSelf() // 先安排新实例，再走与「退出」相同的关闭流程
				quitting.Store(true)
				removeTray()
				win.PostMessage(hwin, win.WM_CLOSE, 0, 0)
			case trayCmdQuit:
				quitting.Store(true)
				removeTray()
				win.PostMessage(hwin, win.WM_CLOSE, 0, 0)
			}
			return 0
		}
		return win.CallWindowProc(oldProc.Load(), hwin, msg, wParam, lParam)
	})
	old := win.SetWindowLongPtr(win.HWND(h), win.GWLP_WNDPROC, cb)
	oldProc.Store(old)
}

// startServer 启动代理 + 管理界面（IPv4 / IPv6 loopback），支持优雅退出。
// 若两个地址都无法监听，返回错误（避免出现「窗口开着但后端未监听」的僵尸实例）。
func startServer(port int, sc *sidecar.Manager, tun *tunnel.Manager) (chan struct{}, error) {
	mux := http.NewServeMux()
	p := proxy.New()
	mux.Handle("/v1/", p)
	mux.HandleFunc("/healthz", p.Healthz)
	mux.Handle("/", webui.New(port, sc, tun))

	srv := &http.Server{Handler: mux}

	ln4, err4 := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	ln6, err6 := net.Listen("tcp", fmt.Sprintf("[::1]:%d", port))
	if ln4 == nil && ln6 == nil {
		return nil, fmt.Errorf("127.0.0.1:%d 与 [::1]:%d 均无法监听（%v / %v）", port, port, err4, err6)
	}
	if err4 != nil {
		log.Printf("IPv4 监听失败: %v", err4)
	}
	if err6 != nil {
		log.Printf("IPv6 监听失败（可忽略）: %v", err6)
	}

	done := make(chan struct{})
	go func() {
		if err4 == nil {
			if err := srv.Serve(ln4); err != nil && err != http.ErrServerClosed {
				log.Printf("IPv4 服务错误: %v", err)
			}
		}
	}()
	go func() {
		if err6 == nil {
			if err := srv.Serve(ln6); err != nil && err != http.ErrServerClosed {
				log.Printf("IPv6 服务错误: %v", err)
			}
		}
	}()
	log.Printf("ApiCluster 已启动：代理 http://localhost:%d/v1", port)

	// 优雅退出：监听关闭信号
	go func() {
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		config.Save()
		log.Printf("ApiCluster 已优雅退出")
	}()
	return done, nil
}

// ===== 托盘（手写实现：左键打开 / 右键菜单） =====
func showWindowFromTray(hwin win.HWND) {
	// 若窗口正被 LifeSystem 内嵌（SetParent 成了子窗口），先从内嵌区解绑，
	// 否则从托盘唤起时仍会显示在内嵌区里，造成「内嵌 + 独立」两个界面同时出现。
	detachFromEmbed(hwin)
	win.ShowWindow(hwin, win.SW_SHOW)
	// 保持最大化状态：最大化中则直接还原为最大化，否则恢复普通大小
	if win.IsZoomed(hwin) {
		win.ShowWindow(hwin, win.SW_MAXIMIZE)
	} else {
		win.ShowWindow(hwin, win.SW_RESTORE)
	}
	win.SetForegroundWindow(hwin)
}

// detachFromEmbed 若窗口被外部程序（LifeSystem 内嵌）SetParent 成了子窗口，
// 则解绑回独立顶层窗口（恢复标准窗口样式 + 挂回桌面），使托盘唤起得到独立窗口。
// 也用于父窗口（LifeSystem）被强杀后自愈：还原后显式 show，确保窗口可见可继续使用。
func detachFromEmbed(hwin win.HWND) {
	if win.GetParent(hwin) == 0 {
		return // 本就独立，无需处理
	}
	style := win.GetWindowLongPtr(hwin, win.GWL_STYLE)
	style &^= uintptr(win.WS_CHILD)           // 去掉子窗口标志
	style |= uintptr(win.WS_OVERLAPPEDWINDOW) // 恢复标准顶层窗口样式（标题栏/边框/系统菜单）
	win.SetWindowLongPtr(hwin, win.GWL_STYLE, style)
	win.SetParent(hwin, 0)
	win.SetWindowPos(hwin, 0, 0, 0, 0, 0,
		win.SWP_FRAMECHANGED|win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE)
	win.ShowWindow(hwin, win.SW_SHOW)
}

// watchParentDeath 监视主窗口是否仍被外部程序（LifeSystem）内嵌。
// 若 LifeSystem 被强杀/崩溃退出，其主窗口销毁会连带销毁作为子窗口的本窗口，
// 导致 ApiCluster 进程随 w.Run() 返回而退出、本地代理中断。这里周期检测父窗口
// 是否仍存活，一旦发现父窗口已销毁，立刻在 UI 线程把本窗口还原为独立顶层窗口，
// 保证 ApiCluster 在 LifeSystem 关闭后仍能独立运行。
func watchParentDeath(w webview.WebView, h uintptr) {
	for {
		time.Sleep(500 * time.Millisecond)
		if quitting.Load() {
			return
		}
		if !isWindow(win.HWND(h)) {
			return // 窗口本身已销毁（无法挽救，w.Run 会返回）
		}
		parent := win.GetParent(win.HWND(h))
		if parent == 0 {
			continue // 未内嵌，无需处理
		}
		if !isWindow(win.HWND(parent)) {
			// 父窗口已销毁 → 在 UI 线程还原为独立窗口（继续守护，覆盖再次被内嵌的场景）
			w.Dispatch(func() {
				detachFromEmbed(win.HWND(h))
			})
		}
	}
}

// isWindow 判断窗口句柄是否仍有效（user32.IsWindow 的封装）。
func isWindow(h win.HWND) bool {
	proc := syscall.NewLazyDLL("user32.dll").NewProc("IsWindow")
	r, _, _ := proc.Call(uintptr(h))
	return r != 0
}

// initTray 添加托盘图标（复用主窗口作为消息接收窗口）
func initTray(h uintptr) {
	icon := loadTrayIcon()
	if icon == 0 {
		return
	}
	trayNID = win.NOTIFYICONDATA{
		HWnd:             win.HWND(h),
		UID:              1,
		UFlags:           win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP,
		UCallbackMessage: wmTrayMsg,
		HIcon:            icon,
	}
	trayNID.CbSize = uint32(unsafe.Sizeof(trayNID))
	copy(trayNID.SzTip[:], syscall.StringToUTF16("ApiCluster - 本地聚合代理"))
	trayAdded = win.Shell_NotifyIcon(win.NIM_ADD, &trayNID)
}

func removeTray() {
	if trayAdded {
		win.Shell_NotifyIcon(win.NIM_DELETE, &trayNID)
		trayAdded = false
	}
}

func showTrayMenu(hwin win.HWND) {
	menu := win.CreatePopupMenu()
	if menu == 0 {
		return
	}
	defer win.DestroyMenu(menu)

	appendMenuW(menu, win.MF_STRING, trayCmdOpen, "打开 ApiCluster")
	appendMenuW(menu, win.MF_SEPARATOR, 0, "")
	appendMenuW(menu, win.MF_STRING, trayCmdRestart, "重启")
	appendMenuW(menu, win.MF_STRING, trayCmdQuit, "退出")

	win.SetForegroundWindow(hwin)
	var pt win.POINT
	win.GetCursorPos(&pt)
	win.TrackPopupMenu(menu, win.TPM_RIGHTBUTTON, pt.X, pt.Y, 0, hwin, nil)
}

// restartSelf 重新拉起一个自身实例；调用方随后按「退出」流程结束本进程。
// 新实例必须等本进程退出后再启动——否则会被 acquireSingleInstance 的互斥体
// 判为「已有实例」直接退出，端口也还没让出来。这里交给 PowerShell
// Wait-Process 等到本 PID 消失再 Start-Process，比固定 sleep 更稳。
// 启动参数原样继承（含开机自启的 -hidden）。
func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[restart] 取自身路径失败：%v", err)
		return
	}
	psQuote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	// -WindowStyle Hidden + HideWindow：拉起过程不闪出黑框
	ps := fmt.Sprintf(
		"$ErrorActionPreference='SilentlyContinue'; Wait-Process -Id %d -Timeout 15; Start-Process -FilePath %s -WorkingDirectory %s",
		os.Getpid(), psQuote(exe), psQuote(filepath.Dir(exe)),
	)
	if rest := os.Args[1:]; len(rest) > 0 {
		args := make([]string, 0, len(rest))
		for _, a := range rest {
			args = append(args, psQuote(a))
		}
		ps += " -ArgumentList " + strings.Join(args, ",")
	}
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		log.Printf("[restart] 安排重启失败：%v", err)
	}
}

func appendMenuW(menu win.HMENU, flags uintptr, id uintptr, text string) {
	proc := syscall.NewLazyDLL("user32.dll").NewProc("AppendMenuW")
	var textPtr *uint16
	if text != "" {
		textPtr, _ = syscall.UTF16PtrFromString(text)
	}
	proc.Call(uintptr(menu), flags, id, uintptr(unsafe.Pointer(textPtr)))
}

func loadTrayIcon() win.HICON {
	f, err := os.CreateTemp("", "apicluster-*.ico")
	if err != nil {
		return 0
	}
	path := f.Name()
	if _, err := f.Write(trayIconBytes()); err != nil {
		f.Close()
		os.Remove(path)
		return 0
	}
	f.Close()

	pathPtr, _ := syscall.UTF16PtrFromString(path)
	h := win.LoadImage(0, pathPtr, win.IMAGE_ICON, 0, 0, win.LR_LOADFROMFILE|win.LR_DEFAULTSIZE)
	os.Remove(path)
	return win.HICON(h)
}

// trayIconBytes 运行时生成 ICO 字节（蓝底 + 白色路由聚合网络，PNG 内嵌格式）
func trayIconBytes() []byte {
	s := 64
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	blue := color.RGBA{59, 130, 246, 255}
	white := color.RGBA{255, 255, 255, 255}
	cx, cy, r := s/2, s/2, s/2-2
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r*r {
				img.Set(x, y, blue)
			}
		}
	}
	nodes := [][2]int{{32, 14}, {14, 45}, {50, 45}}
	drawLine(img, nodes[0][0], nodes[0][1], nodes[1][0], nodes[1][1], white)
	drawLine(img, nodes[0][0], nodes[0][1], nodes[2][0], nodes[2][1], white)
	drawLine(img, nodes[1][0], nodes[1][1], nodes[2][0], nodes[2][1], white)
	for _, n := range nodes {
		drawCircle(img, n[0], n[1], 5, white)
	}

	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)

	buf := new(bytes.Buffer)
	_, _ = buf.Write([]byte{0, 0})  // reserved
	_, _ = buf.Write([]byte{1, 0})  // type: icon
	_, _ = buf.Write([]byte{1, 0})  // count
	buf.WriteByte(byte(s))          // width
	buf.WriteByte(byte(s))          // height
	buf.WriteByte(0)                // palette
	buf.WriteByte(0)                // reserved
	_, _ = buf.Write([]byte{1, 0})  // planes
	_, _ = buf.Write([]byte{32, 0}) // bpp
	_ = binary.Write(buf, binary.LittleEndian, uint32(pngBuf.Len()))
	_ = binary.Write(buf, binary.LittleEndian, uint32(22))
	buf.Write(pngBuf.Bytes())
	return buf.Bytes()
}

func drawCircle(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r*r {
				img.Set(cx+x, cy+y, c)
			}
		}
	}
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for ox := -1; ox <= 1; ox++ {
		for oy := -1; oy <= 1; oy++ {
			bresenham(img, x0+ox, y0+oy, x1+ox, y1+oy, c)
		}
	}
}

func bresenham(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		if x0 >= 0 && x0 < 64 && y0 >= 0 && y0 < 64 {
			img.Set(x0, y0, c)
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func openExternal(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func showError(title, msg string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	proc.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}
