package main

import (
	"fmt"
	"github.com/xbuyan/Escrowd/internal/api"
	"github.com/xbuyan/Escrowd/internal/bot"
	"os"
	"sync"
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
		case "both":
			fmt.Println("starting escrowd bot and API server...")
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				bot.Start()
			}()
			go func() {
				defer wg.Done()
				api.Start()
			}()
			wg.Wait()
			return
		}
	}
	fmt.Println("usage:")
	fmt.Println("  escrowd bot   — starts the Discord bot")
	fmt.Println("  escrowd api   — starts the HTTP API server")
	fmt.Println("  escrowd both  — starts both concurrently")
	os.Exit(1)
}
