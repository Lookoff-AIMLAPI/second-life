package tray

import (
	_ "embed"

	"fyne.io/systray"
)

var (
	//go:embed icons/off.png
	pngOff []byte
	//go:embed icons/on.png
	pngOn []byte
	//go:embed icons/attention.png
	pngAttention []byte
)

func setIcon(s iconState) {
	icon := [...][]byte{iconOff: pngOff, iconOn: pngOn, iconAttention: pngAttention}[s]
	systray.SetTemplateIcon(icon, icon)
}
