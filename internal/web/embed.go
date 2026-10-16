// Package web 内嵌前端构建产物（Vue3 SPA），由调度器静态托管。
package web

import "embed"

// all: 前缀不可省：go:embed 默认排除下划线/点开头的文件，而按需引入构建会产出
// _plugin-vue_export-helper-*.js 这类下划线开头的 chunk，漏嵌会在线上 404。
//
//go:embed all:dist
var Dist embed.FS
