package main

import (
	"fmt"
	"net/http"
)

func main() {
	fmt.Println("Go Backend server successfully running on port 8080...")
	
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello! Your Go Backend is working perfectly.")
	})
	
	http.ListenAndServe(":8080", nil)
}