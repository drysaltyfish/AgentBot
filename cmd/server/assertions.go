package main

import "github.com/drysaltyfish/agentbot/internal/router"

// 编译期断言：装配层实现的外部接口必须始终成立（F-79）。
//
// 放在实现文件而不是测试里：接口一改，编译立刻失败，而不是等到跑测试。
var _ router.RouteObserver = routeMetrics{}
