package tray

import (
	_ "embed"

	"fyne.io/systray"
)

var (
	//go:embed icons/off.ico
	icoOff []byte
	//go:embed icons/on.ico
	icoOn []byte
	//go:embed icons/attention.ico
	icoAttention []byte
)

func setIcon(s iconState) {
	systray.SetIcon([...][]byte{iconOff: icoOff, iconOn: icoOn, iconAttention: icoAttention}[s])
}
