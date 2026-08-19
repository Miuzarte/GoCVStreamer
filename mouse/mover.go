package mouse

// Mover 抽象鼠标注入, 本地实现 [LocalMover], 远程实现见 remoteclient 包
type Mover interface {
	// Move 相对移动鼠标 (不携带标记)
	Move(dx, dy int) error
	// MoveAndMark 相对移动鼠标并携带 ExtraInfo 标记,
	// 让 rawinput 能识别并忽略 "自己注入的移动"
	MoveAndMark(dx, dy int) error
	// MouseDown 按下鼠标按钮 (MB_LEFT/MB_MIDDLE/MB_RIGHT)
	MouseDown(button int) error
	// MouseUp 松开鼠标按钮 (MB_LEFT/MB_MIDDLE/MB_RIGHT)
	MouseUp(button int) error
	// MouseClick 点击一次鼠标按钮 (MB_LEFT/MB_MIDDLE/MB_RIGHT)
	MouseClick(button int) error
}

// LocalMover 是本地 SendInput 注入实现, 包装本包的包级函数
type LocalMover struct{}

func (LocalMover) Move(dx, dy int) error        { return Move(dx, dy) }
func (LocalMover) MoveAndMark(dx, dy int) error { return MoveAndMark(dx, dy) }
func (LocalMover) MouseDown(button int) error   { return MouseDown(button) }
func (LocalMover) MouseUp(button int) error     { return MouseUp(button) }
func (LocalMover) MouseClick(button int) error  { return MouseClick(button) }
