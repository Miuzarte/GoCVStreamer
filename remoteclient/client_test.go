package remoteclient

import (
	"bufio"
	jsonv2 "encoding/json/v2"
	"net"
	"strings"
	"testing"
	"time"
)

// startFakeMhub 起一个假 mhub server, 返回监听地址与收到的消息通道
func startFakeMhub(t *testing.T) (string, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	got := make(chan string, 16)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			got <- sc.Text()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), got
}

func recvMsg(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
		return ""
	}
}

func TestClientMove(t *testing.T) {
	addr, got := startFakeMhub(t)
	c := Dial(addr)
	defer c.Close()

	if err := c.Move(3, -2); err != nil {
		t.Fatalf("Move: %v", err)
	}
	var msg remoteMsg
	if err := jsonv2.Unmarshal([]byte(recvMsg(t, got)), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.T != "move" || msg.Dx != 3 || msg.Dy != -2 || msg.Mark {
		t.Fatalf("move msg = %+v", msg)
	}
}

func TestClientMoveAndMark(t *testing.T) {
	addr, got := startFakeMhub(t)
	c := Dial(addr)
	defer c.Close()

	if err := c.MoveAndMark(5, 7); err != nil {
		t.Fatalf("MoveAndMark: %v", err)
	}
	var msg remoteMsg
	if err := jsonv2.Unmarshal([]byte(recvMsg(t, got)), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.T != "move" || msg.Dx != 5 || msg.Dy != 7 || !msg.Mark {
		t.Fatalf("marked move msg = %+v", msg)
	}
}

func TestClientButtons(t *testing.T) {
	addr, got := startFakeMhub(t)
	c := Dial(addr)
	defer c.Close()

	if err := c.MouseDown(0); err != nil { // MB_LEFT -> 1
		t.Fatalf("MouseDown: %v", err)
	}
	raw1 := recvMsg(t, got)
	t.Logf("raw1: %q", raw1)
	var msg1 remoteMsg
	if err := jsonv2.Unmarshal([]byte(raw1), &msg1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg1.T != "btn" || msg1.B != 1 || !msg1.Down {
		t.Fatalf("btn down msg = %+v", msg1)
	}

	if err := c.MouseUp(2); err != nil { // MB_RIGHT -> 2
		t.Fatalf("MouseUp: %v", err)
	}
	raw2 := recvMsg(t, got)
	t.Logf("raw2: %q", raw2)
	var msg2 remoteMsg
	if err := jsonv2.Unmarshal([]byte(raw2), &msg2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg2.T != "btn" || msg2.B != 2 || msg2.Down {
		t.Fatalf("btn up msg = %+v", msg2)
	}

	if err := c.MouseClick(1); err != nil { // MB_MIDDLE -> 3
		t.Fatalf("MouseClick: %v", err)
	}
	raw3 := recvMsg(t, got)
	t.Logf("raw3: %q", raw3)
	var msg3 remoteMsg
	if err := jsonv2.Unmarshal([]byte(raw3), &msg3); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg3.T != "btn" || msg3.B != 3 || !msg3.Down {
		t.Fatalf("click down msg = %+v", msg3)
	}
	raw4 := recvMsg(t, got)
	t.Logf("raw4: %q", raw4)
	var msg4 remoteMsg
	if err := jsonv2.Unmarshal([]byte(raw4), &msg4); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg4.T != "btn" || msg4.B != 3 || msg4.Down {
		t.Fatalf("click up msg = %+v", msg4)
	}
}

func TestButtonToMhub(t *testing.T) {
	cases := map[int]int{0: 1, 1: 3, 2: 2, -1: -1, 3: -1}
	for in, want := range cases {
		if got := buttonToMhub(in); got != want {
			t.Fatalf("buttonToMhub(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestClientReconnect(t *testing.T) {
	// 先连上一个 server, 关闭它, 再起新 server 同端口, Client 应能重连
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen1: %v", err)
	}
	addr := ln1.Addr().String()
	got1 := make(chan string, 4)
	go func() {
		conn, err := ln1.Accept()
		if err != nil {
			return
		}
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			got1 <- sc.Text()
		}
	}()
	_ = ln1.Close() // 让第一次连接失败

	c := Dial(addr)
	defer c.Close()

	// 第二次连接前先把 listener 重新打开 (同端口)
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen2: %v", err)
	}
	defer ln2.Close()
	got2 := make(chan string, 4)
	go func() {
		conn, err := ln2.Accept()
		if err != nil {
			return
		}
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			got2 <- sc.Text()
		}
	}()

	if err := c.Move(1, 1); err != nil {
		t.Fatalf("Move: %v", err)
	}
	var msg remoteMsg
	if err := jsonv2.Unmarshal([]byte(recvMsg(t, got2)), &msg); err != nil {
		t.Fatalf("unmarshal after reconnect: %v", err)
	}
	if msg.T != "move" || !strings.Contains(addr, "127.0.0.1") {
		t.Fatalf("unexpected msg %+v", msg)
	}
}

func TestClientSetRemoteState(t *testing.T) {
	addr, got := startFakeMhub(t)
	c := Dial(addr)
	defer c.Close()

	if err := c.SetRemoteState("WeaponType", "full"); err != nil {
		t.Fatalf("SetRemoteState: %v", err)
	}
	var msg remoteMsg
	if err := jsonv2.Unmarshal([]byte(recvMsg(t, got)), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.T != "state" || msg.Key != "WeaponType" {
		t.Fatalf("state msg = %+v", msg)
	}
	var v string
	if err := jsonv2.Unmarshal(msg.Value, &v); err != nil {
		t.Fatalf("value: %v", err)
	}
	if v != "full" {
		t.Fatalf("value = %q, want full", v)
	}
}

func TestClientEval(t *testing.T) {
	addr, got := startFakeMhub(t)
	c := Dial(addr)
	defer c.Close()

	if err := c.Eval("SpeedOffset = 3"); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	var msg remoteMsg
	if err := jsonv2.Unmarshal([]byte(recvMsg(t, got)), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.T != "eval" || msg.Expr != "SpeedOffset = 3" {
		t.Fatalf("eval msg = %+v", msg)
	}
}
