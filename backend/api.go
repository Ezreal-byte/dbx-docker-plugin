package main

import (
	"github.com/docker/docker/client"
)

// dockerAPI 抽象出后端用到的 docker client 方法，便于在 compose 测试中注入 fake。
type dockerAPI = *client.Client
