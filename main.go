package main

import (
	"os"

	"github.com/elastic/beats/libbeat/beat"

	"mysqlbeat/beater"
	"fmt"
)

func main() {
	err := beat.Run("mysqlbeat", "", beater.New())
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
