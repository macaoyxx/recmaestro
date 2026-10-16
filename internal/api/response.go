// Package api 提供 REST API 层公共约定：统一响应信封与分页查询入参。
//
// 响应信封（全部 JSON 接口一致）：
//
//	普通:  {"code": 0, "data": <任意>, "msg": "ok"}
//	分页:  {"code": 0, "data": [<列表>], "msg": "ok", "total": <总数>}
//	失败:  HTTP 状态码照常返回，body 为 {"code": <HTTP状态码>, "data": null, "msg": "<说明>"}
//
// code 约定：0 = 成功；失败时取 HTTP 状态码（400/401/403/404/409/500…），不另设业务码表。
// 文件下载等流式接口与 worker 内部接口不走信封。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Envelope 是统一响应信封。Total 用指针 + omitempty：仅分页接口携带，
// 且 total=0 的合法空页也会输出该字段（指针非 nil 即序列化）。
type Envelope struct {
	Code  int         `json:"code"`
	Data  interface{} `json:"data"`
	Msg   string      `json:"msg"`
	Total *int64      `json:"total,omitempty"`
}

// PageQuery 分页查询入参基类：各分页接口的入参结构体内嵌（继承 page/page_size），
// POST body JSON 绑定后经 Normalize 归一化。
// 缺省与归一化规则：page <= 0 归 1，pageSize <= 0 归 10。
type PageQuery struct {
	Page     int `form:"page" json:"page"`
	PageSize int `form:"page_size" json:"page_size"`
}

// 分页缺省值（与 store 层 List 的兜底一致）。
const (
	defaultPage     = 1
	defaultPageSize = 10
)

// Normalize 就地归一化非法/缺省值，返回自身便于链式使用。
func (q *PageQuery) Normalize() *PageQuery {
	if q.Page <= 0 {
		q.Page = defaultPage
	}
	if q.PageSize <= 0 {
		q.PageSize = defaultPageSize
	}
	return q
}

// OK 成功响应：{code:0, data, msg:"ok"}。
func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Envelope{Code: 0, Data: data, Msg: "ok"})
}

// OKMsg 成功响应并附提示文案（动作类接口，如"已删除"）。
func OKMsg(c *gin.Context, data interface{}, msg string) {
	c.JSON(http.StatusOK, Envelope{Code: 0, Data: data, Msg: msg})
}

// Created 创建成功响应（201）。
func Created(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, Envelope{Code: 0, Data: data, Msg: "ok"})
}

// OKPage 分页成功响应：data 为列表，顶层携带 total。
func OKPage(c *gin.Context, list interface{}, total int64) {
	c.JSON(http.StatusOK, Envelope{Code: 0, Data: list, Msg: "ok", Total: &total})
}

// Fail 失败响应：HTTP 状态码照常，信封 code 取同一状态码。
func Fail(c *gin.Context, status int, msg string) {
	c.JSON(status, Envelope{Code: status, Msg: msg})
}
