package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func selectionSkips(doc nodeproto.PlanDoc, options runspec.Options) (map[string]string, error) {
	if err := validateStepRange(doc, options.StartAt.Value, options.StopAt.Value); err != nil {
		return nil, err
	}
	return store.PlanSelectionSkips(doc, options.Only.Value, options.StartAt.Value, options.StopAt.Value)
}
