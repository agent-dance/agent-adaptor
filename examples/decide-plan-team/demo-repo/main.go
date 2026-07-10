package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	items := defaultItems()
	switch os.Args[1] {
	case "list":
		fmt.Print(renderList(items))
	case "summary":
		fmt.Println(renderSummary(items))
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("usage: todo <list|summary>")
}
