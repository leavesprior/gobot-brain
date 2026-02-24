// Example: full gobot-brain setup with all five components.
//
// This wires together memory, inference, scheduler, watchdog, and HITL
// into a single robot using the NewBrain convenience constructor.
//
// Run with:
//
//	go run ./examples/robot-with-brain/
package main

import (
	"fmt"
	"log"
	"time"

	"gobot.io/x/gobot/v2"

	brain "github.com/leavesprior/gobot-brain"
	"github.com/leavesprior/gobot-brain/hitl"
	"github.com/leavesprior/gobot-brain/inference"
	"github.com/leavesprior/gobot-brain/memory"
	"github.com/leavesprior/gobot-brain/scheduler"
	"github.com/leavesprior/gobot-brain/watchdog"
)

func main() {
	var b *brain.Brain
	b = brain.NewBrain("smart-robot",
		// Persist memory to disk.
		brain.WithMemoryOptions(
			memory.WithFileStore("/tmp/gobot-brain-demo"),
		),

		// LLM fallback chain: try Ollama first, then OpenAI-compatible API.
		brain.WithProviders(
			inference.NewOllamaProvider(
				inference.WithOllamaModel("llama3"),
			),
			// Uncomment with your API key:
			// inference.NewOpenAIProvider("sk-...",
			//     inference.WithOpenAIModel("gpt-4"),
			// ),
		),

		// Scheduler: periodic health report every 30 seconds.
		brain.WithSchedulerOptions(
			scheduler.WithEscalationThreshold(3),
			scheduler.WithTask(scheduler.Task{
				Name:     "health-report",
				Interval: 30 * time.Second,
				Level:    scheduler.Silent,
				Fn: func() error {
					fmt.Println("[scheduler] Health report: all systems nominal")
					return nil
				},
			}),
		),

		// Watchdog: alert callback.
		brain.WithWatchdogAlert(func(name string, err error, consecutive int) {
			if err != nil {
				fmt.Printf("[watchdog] ALERT: %s failed %d times: %v\n", name, consecutive, err)
			} else {
				fmt.Printf("[watchdog] RECOVERED: %s is healthy again\n", name)
			}
		}),
		brain.WithWatchdogOptions(
			watchdog.WithAlertAfter(2),
		),

		// HITL: print notification to console.
		brain.WithHITLNotify(func(req hitl.Request) error {
			fmt.Printf("[hitl] Approval needed: %s (ID: %s)\n", req.Description, req.ID)
			return nil
		}),

		// Robot work function.
		brain.WithWork(func(r *gobot.Robot) {
			fmt.Println("Robot started with full brain!")

			// Add a watchdog health check.
			b.Watchdog.AddCheck(watchdog.Check{
				Name:     "memory-ping",
				Interval: 10 * time.Second,
				Timeout:  5 * time.Second,
				Fn: func() error {
					// Verify memory is working.
					if err := b.Memory.Store("watchdog", "ping", time.Now().Unix()); err != nil {
						return err
					}
					_, err := b.Memory.Retrieve("watchdog", "ping")
					return err
				},
			})

			// Request human approval for a risky action.
			id, err := b.HITL.RequestApproval(hitl.Request{
				Description: "Deploy firmware update to all fleet robots",
				Timeout:     5 * time.Minute,
				Action: func() error {
					fmt.Println("[hitl] Firmware update deployed!")
					return nil
				},
			})
			if err != nil {
				log.Printf("HITL error: %v", err)
			} else {
				fmt.Printf("Approval request created: %s\n", id)
				// In a real scenario, a webhook/Telegram callback would call:
				//   b.HITL.Approve(id)
			}

			// Store robot state in memory.
			if err := b.Memory.Store("robot", "status", map[string]interface{}{
				"started_at": time.Now().Format(time.RFC3339),
				"components": []string{"memory", "inference", "scheduler", "watchdog", "hitl"},
			}); err != nil {
				log.Printf("Memory store error: %v", err)
			}

			fmt.Println("All brain components wired and running.")
		}),
	)

	// Access brain components directly.
	_ = b.Memory
	_ = b.Inference
	_ = b.Scheduler
	_ = b.Watchdog
	_ = b.HITL

	if err := b.Robot.Start(); err != nil {
		log.Fatal(err)
	}
}
