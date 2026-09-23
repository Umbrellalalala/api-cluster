// Package autostart 管理 Windows 开机自启（HKCU Run 键，无需管理员权限）。
package autostart

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName  = "ApiCluster"
)

// IsEnabled 检查是否已设置开机自启
func IsEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(valueName)
	return err == nil
}

// Enable 开启开机自启。启动时静默运行（--hidden，不弹窗）。
func Enable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.Abs(exe)
	// Run 键值： "C:\path\to\exe" --hidden
	cmd := "\"" + exe + "\" --hidden"
	return k.SetStringValue(valueName, cmd)
}

// Disable 关闭开机自启
func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue(valueName)
}
