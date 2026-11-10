package jobs

import (
	"errors"
	"fmt"
	"strings"

	"forja/internal/config"
	"forja/internal/plain"
	"forja/internal/provider"
)

// explain is what a failed job shows: what happened, where, and what to do next. The error
// itself, with every layer that wrapped it, goes to the core's log.
func (r *Runner) explain(cfg *config.Loaded, err error) string {
	var se *stageError
	if !errors.As(err, &se) {
		return err.Error()
	}
	where := r.label(cfg, se.provider)
	said := brief(strings.TrimPrefix(se.Error(), "provider "+se.provider+": "))
	switch {
	case errors.Is(err, provider.ErrRunLost):
		return where + ": the engine stopped while this job ran, and again when the job was sent once more. It may be " +
			"crashing or running out of memory on this job; the engine's log says more. Try a lighter preset or model, " +
			"or generate again."
	case se.oom:
		return fmt.Sprintf("%s ran out of memory: %s. Close other programs that use the GPU, choose a lighter preset or "+
			"model, or add a provider with more memory, then generate again.", where, strings.TrimRight(said, ". "))
	case se.down:
		return fmt.Sprintf("%s could not be reached: %s. Check that it is running and connected, then generate again.",
			where, strings.TrimPrefix(unreached(se.err), "provider "+se.provider+": "))
	}
	return where + ": " + said
}

func unreached(err error) string { return plain.Unreached(err) }
