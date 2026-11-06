//go:build !windows

package main

import "fmt"

func main() { fmt.Println("bsctl работает только под Windows") }
