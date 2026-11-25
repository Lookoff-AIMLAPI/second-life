package tray

import (
	"runtime"

	"fyne.io/systray"
)

// menuItem is the part of *systray.MenuItem the menu uses.
type menuItem interface {
	SetTitle(string)
	Enable()
	Disable()
	Check()
	Uncheck()
	Show()
	Hide()
}

// widget remembers what is on screen, so that only differences are sent.
type widget struct {
	item menuItem
	cur  entry
}

func (w *widget) apply(e entry) {
	if !e.Visible {
		// Nothing else: on Windows any update brings a hidden item back.
		if w.cur.Visible {
			w.item.Hide()
			w.cur.Visible = false
		}
		return
	}
	if e.Title != w.cur.Title {
		w.item.SetTitle(menuTitle(e.Title, runtime.GOOS))
	}
	if e.Enabled != w.cur.Enabled {
		if e.Enabled {
			w.item.Enable()
		} else {
			w.item.Disable()
		}
	}
	if e.Checked != w.cur.Checked {
		if e.Checked {
			w.item.Check()
		} else {
			w.item.Uncheck()
		}
	}
	if !w.cur.Visible {
		w.item.Show()
	}
	w.cur = e
}

// menu is only touched by the goroutine that created it.
type menu struct {
	models  []modelRow
	items   [itemCount]*systray.MenuItem
	widgets [itemCount]*widget
	icon    iconState
	tooltip string
	painted bool
}

func newMenu(v view) *menu {
	m := &menu{}
	for id := itemStatus; id < itemCount; id++ {
		if id == itemPlan || id == itemSignIn {
			systray.AddSeparator()
		}
		e := v.Items[id]
		if id == itemAutostart {
			m.items[id] = systray.AddMenuItemCheckbox(e.Title, "", false)
		} else {
			m.items[id] = systray.AddMenuItem(menuTitle(e.Title, runtime.GOOS), "")
		}
		m.widgets[id] = &widget{item: m.items[id], cur: entry{Title: e.Title, Enabled: true, Visible: true}}
	}
	m.apply(v)
	return m
}

func (m *menu) apply(v view) {
	if !m.painted || m.icon != v.Icon {
		setIcon(v.Icon)
		m.icon = v.Icon
	}
	if !m.painted || m.tooltip != v.Tooltip {
		systray.SetTooltip(v.Tooltip)
		m.tooltip = v.Tooltip
	}
	m.painted = true
	for id, w := range m.widgets {
		w.apply(v.Items[id])
	}
}

// setModels shows which model answers for each Claude name; only the draw goroutine calls it.
func (m *menu) setModels(lines []string) {
	for i, line := range lines {
		title := menuTitle(line, runtime.GOOS)
		switch {
		case i >= len(m.models):
			w := m.items[itemModels].AddSubMenuItem(title, "")
			w.Disable()
			m.models = append(m.models, modelRow{w, title})
		case m.models[i].title != title:
			m.models[i].item.SetTitle(title)
			m.models[i].title = title
		}
	}
}

type modelRow struct {
	item  *systray.MenuItem
	title string
}
