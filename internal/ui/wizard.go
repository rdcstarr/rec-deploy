package ui

// WizardStep is one ordered setup action. Keeping the order as data makes a
// wizard's flow visible and testable without coupling the individual steps.
type WizardStep struct {
	Name string
	Run  func() error
}

// RunWizard executes steps in order and stops at the first error.
func RunWizard(steps ...WizardStep) error {
	for _, step := range steps {
		if step.Run == nil {
			continue
		}
		if err := step.Run(); err != nil {
			return err
		}
	}
	return nil
}
