package main

import (
	"fmt"
)


func main() {
	source := 'C:\Users\ahahahah\Downloads\setups'
	target := 'C:\Users\ahahahah\Downloads\mysetups'

	fmt.Println("Source:", source)
	fmt.Println("Target:", target)
}

func getDirecties(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	val dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}

	return dirs, nil
}

cars, err := getDirecties(source)
if err != nil {
	panic(err)
}

for _, car := range cars {
	fmt.Println("Car:", car)
}