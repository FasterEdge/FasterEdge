// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
//go:build windows

// Windows 通信 API 的过程句柄, 以及 syscall 包没有暴露的 RegEnumValueW。
// 集中在此文件, 避免每个调用点重复 NewProc。
package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modAdvapi32 = syscall.NewLazyDLL("advapi32.dll")

	procGetCommState    = modKernel32.NewProc("GetCommState")
	procSetCommState    = modKernel32.NewProc("SetCommState")
	procSetCommTimeouts = modKernel32.NewProc("SetCommTimeouts")
	procPurgeComm       = modKernel32.NewProc("PurgeComm")
	procRegEnumValueW   = modAdvapi32.NewProc("RegEnumValueW")
)

// winRegEnumValue 封装 RegEnumValueW: syscall 包只暴露了 RegEnumKeyEx /
// RegQueryValueEx, 没有枚举"值"的入口, 因此这里直接按 W 变体原型调用。
//
//	LSTATUS RegEnumValueW(HKEY hKey, DWORD dwIndex, LPWSTR lpValueName,
//	                     LPDWORD lpcchValueName, LPDWORD lpReserved,
//	                     LPDWORD lpType, LPBYTE lpData, LPDWORD lpcbData);
func winRegEnumValue(key syscall.Handle, index uint32, name *uint16, nameLen *uint32, data *uint16, dataLen *uint32) error {
	r1, _, e := procRegEnumValueW.Call(
		uintptr(key),
		uintptr(index),
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(nameLen)),
		0, // lpReserved
		0, // lpType: 端口名都是 REG_SZ, 不做类型过滤
		uintptr(unsafe.Pointer(data)),
		uintptr(unsafe.Pointer(dataLen)),
	)
	if r1 != 0 {
		return fmt.Errorf("RegEnumValueW: %w", e)
	}
	return nil
}
