package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DescribedOption is one menu item with a secondary state or explanation.
type DescribedOption struct {
	Name        string
	Description string
	Value       string
}

// DescribedOptions aligns every description after the longest visible name.
func DescribedOptions(items ...DescribedOption) []Option {
	width := 0
	for _, item := range items {
		if w := lipgloss.Width(item.Name); w > width {
			width = w
		}
	}
	options := make([]Option, len(items))
	for i, item := range items {
		label := item.Name
		if item.Description != "" {
			label += strings.Repeat(" ", width-lipgloss.Width(item.Name)+3) + Dim(item.Description)
		}
		options[i] = Option{Label: label, Value: item.Value}
	}
	return options
}
