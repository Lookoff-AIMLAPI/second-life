package bridge

import (
	"context"

	"github.com/Lookoff-AIMLAPI/second-life/internal/picker"
)

// pickerDeps is the bridge as the picker page sees it.
type pickerDeps struct{ *Bridge }

func (d pickerDeps) AIMLAPIModels(ctx context.Context) ([]picker.Model, error) {
	list, err := d.Bridge.AIMLAPIModels(ctx)
	out := make([]picker.Model, len(list))
	for i, m := range list {
		out[i] = picker.Model(m)
	}
	return out, err
}

// Plugins is the plugin store. A nil runner must stay a nil interface.
func (d pickerDeps) Plugins() picker.PluginStore {
	if d.plugins == nil {
		return nil
	}
	return d.plugins
}
