package ui

// WizardStep is one ordered setup action. Keeping the order as data makes a
// wizard's flow visible and testable without coupling the individual steps.
type WizardStep struct {
	// Name is the operator-facing heading, e.g. "Server".
	Name string
	// Skip, when it reports true, drops the step from the run and from the
	// numbering. A host without systemd has no update timer to enable, and
	// numbering its wizard "7" would promise a step it never reaches.
	Skip func() bool
	Run  func() error
}

// RunWizard announces and executes each step that is not skipped, in order,
// stopping at the first error. The headings it prints carry the wizard's only
// blank lines, which is what keeps the spacing uniform rather than incidental.
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
		if err := step.Run(); err != nil {
			return err
		}
	}

	return nil
}
