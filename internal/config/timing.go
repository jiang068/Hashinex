package config

import (
	"fmt"
	"time"
)

func (e Endpoint) validateTiming(heartbeat, reconnect bool) error {
	if e.HeartbeatInterval != 0 && !heartbeat {
		return fmt.Errorf("此连接不能设置 heartbeat_interval。QQ 官方 Gateway 的心跳由服务端下发。")
	}
	if e.ReconnectInterval != 0 && !reconnect {
		return fmt.Errorf("此连接由对端拨号或使用 Webhook，重连间隔请在对端配置。请移除 reconnect_interval。")
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"heartbeat_interval", e.HeartbeatInterval},
		{"reconnect_interval", e.ReconnectInterval},
	} {
		if field.value != 0 && (field.value < time.Second || field.value > 10*time.Minute) {
			return fmt.Errorf("%s 须为 1s 到 10m，留空使用默认值。", field.name)
		}
	}
	return nil
}
