package main

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	hash, err := bcrypt.GenerateFromPassword([]byte("Test@2026"), 12)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(hash))
}
