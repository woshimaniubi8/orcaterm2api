// Package version 保存网关版本号与对外标识。
package version

// Version 是当前网关版本；面板与 /healthz 会透出。
const Version = "0.1.0"

// UserAgent 是出站请求默认 UA（对齐 OrcaTerm 桌面端形态）。
const UserAgent = "OrcaTerm/1.6.11 (Windows; x64) orcaterm2api/" + Version
