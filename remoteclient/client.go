// Package remoteclient 是 GoCVStreamer 到 mhub RemoteServer 的远程注入客户端,
// 实现 mouse.Mover, 让 streamer 的瞄准/压枪移动指令通过 TCP 交给 mhub 执行
package remoteclient

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/Miuzarte/GoCVStreamer/mouse"
)

var log = logger.New("RemoteClient")

// buttonToMhub 按钮号转换: streamer 用 MB_LEFT=0/MB_MIDDLE=1/MB_RIGHT=2,
// mhub 脚本 API 用 G Hub 编号 1=左 2=右 3=中; 越界返回 -1
func buttonToMhub(button int) int {
	if button < 0 || button > 2 {
		return -1
	}
	return [3]int{1, 3, 2}[button]
}

// remoteMsg 与 mhub remoteMsg 一致, 换行分隔 JSON
type remoteMsg struct {
	T      string          `json:"t"`
	Dx     int32           `json:"dx,omitzero"`
	Dy     int32           `json:"dy,omitzero"`
	Mark   bool            `json:"mark,omitzero"`
	B      int             `json:"b,omitzero"`
	Down   bool            `json:"down,omitzero"`
	Clicks int32           `json:"clicks,omitzero"`
	Key    string          `json:"key,omitzero"`
	Value  json.RawMessage `json:"value,omitzero"`
	Expr   string          `json:"expr,omitzero"`
}

// Client 是远程注入客户端, 实现 mouse.Mover,
// 断线后在下次调用时自动重连 (1s 间隔, 最多 3 次尝试)
type Client struct {
	addr string

	mu   sync.Mutex
	conn net.Conn
}

// Dial 创建指向 mhub RemoteServer 的客户端, 并尝试建立初始连接
// (失败不阻塞, send 时会自动重试)
func Dial(addr string) *Client {
	c := &Client{addr: addr}
	c.mu.Lock()
	c.dialLocked()
	c.mu.Unlock()
	return c
}

// dialLocked 建立连接 (调用方需持有 c.mu), 已连接时 no-op
func (c *Client) dialLocked() {
	if c.conn != nil {
		return
	}
	conn, err := net.DialTimeout("tcp", c.addr, time.Second)
	if err != nil {
		log.Debug().Str("addr", c.addr).Err(err).Msg("mhub initial connect failed, will retry on send")
		return
	}
	log.Info().Str("addr", c.addr).Msg("mhub connected")
	c.conn = conn
}

// 编译期断言: Client 实现 mouse.Mover
var _ mouse.Mover = (*Client)(nil)

// Close 关闭当前连接
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// send 确保连接可用后写入一条消息
func (c *Client) send(msg remoteMsg) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for range 3 {
		if c.conn == nil {
			c.dialLocked()
			if c.conn == nil {
				time.Sleep(time.Second)
				continue
			}
		}
		data, err := jsonv2.Marshal(msg)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.conn.Write(data); err != nil {
			_ = c.closeLocked()
			time.Sleep(time.Second)
			continue
		}
		return nil
	}
	return fmt.Errorf("remoteclient: no connection to %s", c.addr)
}

// Move 实现 mouse.Mover: 相对移动 (不携带标记)
func (c *Client) Move(dx, dy int) error {
	return c.send(remoteMsg{T: "move", Dx: int32(dx), Dy: int32(dy)})
}

// MoveAndMark 实现 mouse.Mover: 相对移动并携带 ExtraInfo 标记,
// 供 mhub 注入时设置 dwExtraInfo, 让 streamer 的 rawinput 忽略该移动
func (c *Client) MoveAndMark(dx, dy int) error {
	return c.send(remoteMsg{T: "move", Dx: int32(dx), Dy: int32(dy), Mark: true})
}

// MouseDown 实现 mouse.Mover: 按下鼠标按钮
func (c *Client) MouseDown(button int) error {
	b := buttonToMhub(button)
	if b < 0 {
		return nil
	}
	return c.send(remoteMsg{T: "btn", B: b, Down: true})
}

// MouseUp 实现 mouse.Mover: 松开鼠标按钮
func (c *Client) MouseUp(button int) error {
	b := buttonToMhub(button)
	if b < 0 {
		return nil
	}
	return c.send(remoteMsg{T: "btn", B: b, Down: false})
}

// MouseClick 实现 mouse.Mover: 点击一次鼠标按钮
func (c *Client) MouseClick(button int) error {
	if err := c.MouseDown(button); err != nil {
		return err
	}
	return c.MouseUp(button)
}

// SetRemoteState 推送命名状态, mhub 会以 "key = value" 形式注入脚本全局变量
func (c *Client) SetRemoteState(key string, value any) error {
	raw, err := jsonv2.Marshal(value)
	if err != nil {
		return fmt.Errorf("remoteclient: marshal state %q: %w", key, err)
	}
	return c.send(remoteMsg{T: "state", Key: key, Value: raw})
}

// Eval 注入任意表达式到 mhub 活动脚本解释器
func (c *Client) Eval(expr string) error {
	return c.send(remoteMsg{T: "eval", Expr: expr})
}
