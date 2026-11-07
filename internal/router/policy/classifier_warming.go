package policy

import (
	"aiand-os/router/internal/router"
	"fmt"
)

type ClassifierErrorCode string

const ClassifierWarmingCode ClassifierErrorCode = "qwen_warming"

var ErrClassifierWarming = fmt.Errorf("Qwen classifier is warming; run wv router prepare-model %s before retrying", router.ModelQwen)
