package main

import (
	"context"
	"fmt"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/browser"
)

func main() {
	url := "https://en.wikipedia.org/wiki/Go_(programming_language)"
	fmt.Println("Fetching", url)
	html, err := browser.FetchRendered(context.Background(), url, 30*time.Second)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	
	isBlocked := browser.IsBlocked(200, html)
	fmt.Println("IsBlocked:", isBlocked)
}
