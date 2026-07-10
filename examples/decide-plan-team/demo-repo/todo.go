package main

import "fmt"

type item struct {
	Title string
	Done  bool
}

func defaultItems() []item {
	return []item{
		{Title: "write demo"},
		{Title: "add tests", Done: true},
		{Title: "document behavior"},
	}
}

func renderList(items []item) string {
	out := ""
	for _, current := range items {
		state := "todo"
		if current.Done {
			state = "done"
		}
		out += fmt.Sprintf("- [%s] %s\n", state, current.Title)
	}
	return out
}

func renderSummary(items []item) string {
	doneCount := 0
	for _, current := range items {
		if current.Done {
			doneCount++
		}
	}
	return fmt.Sprintf("total=%d done=%d", len(items), doneCount)
}
