package ui

// Report is a consistent title-and-rows result used by both interactive detail
// screens and plain terminal output.
type Report struct {
	Title string
	Rows  [][2]string
}

// Run opens the report as an interactive detail screen.
func (r Report) Run() error {
	return Detail(r).Run()
}

// Print writes the report in the standard non-interactive layout.
func (r Report) Print() {
	Title(r.Title)
	for _, row := range r.Rows {
		KeyValue(row[0], row[1])
	}
}
