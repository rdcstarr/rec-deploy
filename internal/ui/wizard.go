package ui

import "errors"

// WizardStep is one ordered setup action. Keeping the order as data makes a
// wizard's flow visible and testable without coupling the individual steps.
type WizardStep struct {
	// Name is the operator-facing heading, e.g. "Server".
	Name string
	// Skip, when it reports true, drops the step from the run and from the
	// numbering. A host without systemd has no update timer to enable, and
	// numbering its wizard "7" would promise a step it never reaches.
	Skip func() bool
	// Optional marks a step nothing else depends on, so that its failure is
	// reported and the wizard carries on. Without it one failing extra
	// abandons every step after it — and the summary, and the wizard's exit
	// status, which an installer reads to decide whether setup succeeded.
	Optional bool
	Run      func() error
}

// RunWizard announces and executes each step that is not skipped, in order. It
// stops at the first error a required step returns, and at a quit from any step;
// an optional step's failure is reported and the run continues. The headings it
// prints carry the wizard's only blank lines, which is what keeps the spacing
// uniform rather than incidental.
func RunWizard(steps ...WizardStep) error {
	active := make([]WizardStep, 0, len(steps))
	for _, step := range steps {
		if step.Run == nil || (step.Skip != nil && step.Skip()) {
			continue
		}
		active = append(active, step)
	}

	for i, step := range active {
		Step(i+1, len(active), step.Name)

		err := step.Run()
		switch {
		case err == nil:
		case IsQuit(err), !step.Optional:
			// A quit is the operator leaving, not a step failing, so it ends
			// every wizard regardless of what the step was.
			return err
		case errors.Is(err, ErrBack):
			// Backed out of an extra rather than answering it.
			Info(step.Name + " was not configured")
		default:
			Warn(step.Name + " was not configured: " + err.Error())
		}
	}

	return nil
}
