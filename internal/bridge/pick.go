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
