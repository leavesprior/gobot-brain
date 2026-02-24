// Example: basic usage of gobot-brain memory + inference.
//
// This demonstrates the simplest setup: an in-memory store and a
// local Ollama provider. Run with:
//
//	go run ./examples/basic/
package main

import (
	"fmt"
	"log"

	"gobot.io/x/gobot/v2"

	"github.com/leavesprior/gobot-brain/inference"
	"github.com/leavesprior/gobot-brain/memory"
)

func main() {
	// Create a memory adaptor (in-memory backend by default).
	mem := memory.NewAdaptor()

	// Create an inference driver with a local Ollama provider.
	llm := inference.NewDriver(mem,
		inference.NewOllamaProvider(
			inference.WithOllamaModel("llama3"),
		),
	)

	robot := gobot.NewRobot("basic-brain",
		[]gobot.Connection{mem},
		[]gobot.Device{llm},
		func(r *gobot.Robot) {
			// Store a value in memory.
			if err := mem.Store("config", "greeting", "Hello from gobot-brain!"); err != nil {
				log.Printf("store error: %v", err)
			}

			// Retrieve it back.
			val, err := mem.Retrieve("config", "greeting")
			if err != nil {
				log.Printf("retrieve error: %v", err)
			} else {
				fmt.Println("Retrieved:", val)
			}

			// Run an inference (requires Ollama running on localhost:11434).
			result, err := llm.Infer("What are the three laws of robotics?")
			if err != nil {
				log.Printf("inference error: %v", err)
				return
			}
			fmt.Println("LLM response:", result)
		},
	)

	if err := robot.Start(); err != nil {
		log.Fatal(err)
	}
}
