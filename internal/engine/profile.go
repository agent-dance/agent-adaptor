package engine

func cloneProfileSelection(selection *ProfileSelection) *ProfileSelection {
	if selection == nil {
		return nil
	}
	copySelection := *selection
	if selection.Clone != nil {
		copyClone := *selection.Clone
		copySelection.Clone = &copyClone
	}
	return &copySelection
}
