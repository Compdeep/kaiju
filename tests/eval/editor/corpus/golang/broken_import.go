package main

import "os"

func main() {
	fmt.Println("hello from", runtime.GOOS)
	os.Exit(0)
}
