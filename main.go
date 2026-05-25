package main

import (
	"escrowd/internal/api"
	"escrowd/internal/bot"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "bot":
			fmt.Println("starting escrowd bot...")
			bot.Start()
			return
		case "api":
			fmt.Println("starting escrowd API server...")
			api.Start()
			return
		}
	}
	fmt.Println("usage:")
	fmt.Println("  escrowd bot   — starts the Discord bot")
	fmt.Println("  escrowd api   — starts the HTTP API server")
	os.Exit(1)
}
