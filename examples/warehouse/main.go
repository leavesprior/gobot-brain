// Example: warehouse robot fleet with all gobot-brain components.
//
// This demonstrates a realistic scenario where a fleet of pick-and-place
// arms work together in a warehouse. All nine gobot-brain components
// interact to create an intelligent, self-monitoring system:
//
//   - Memory persists robot state across restarts
//   - Inference diagnoses problems using an LLM
//   - Scheduler runs periodic inventory checks with escalation
//   - Watchdog monitors motor health across the fleet
//   - HITL gates risky firmware updates behind human approval
//   - Guardian enforces safety policies (no movement while charging)
//   - Routing assigns pick tasks to the best-performing arm
//   - Lifecycle prunes old telemetry to prevent unbounded growth
//   - Browser checks the warehouse dashboard for order status
//
// Run with:
//
//	go run ./examples/warehouse/
package main

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"gobot.io/x/gobot/v2"

	brain "github.com/leavesprior/gobot-brain"
	"github.com/leavesprior/gobot-brain/guardian"
	"github.com/leavesprior/gobot-brain/hitl"
	"github.com/leavesprior/gobot-brain/inference"
	"github.com/leavesprior/gobot-brain/lifecycle"
	"github.com/leavesprior/gobot-brain/memory"
	"github.com/leavesprior/gobot-brain/routing"
	"github.com/leavesprior/gobot-brain/scheduler"
	"github.com/leavesprior/gobot-brain/watchdog"
)

func main() {
	var b *brain.Brain
	b = brain.NewBrain("warehouse-fleet",

		// --- MEMORY: persist state to disk so restarts don't lose context ---
		brain.WithMemoryOptions(
			memory.WithFileStore("/tmp/warehouse-brain"),
		),

		// --- INFERENCE: local LLM for diagnostics ---
		brain.WithProviders(
			inference.NewOllamaProvider(
				inference.WithOllamaModel("llama3"),
			),
		),

		// --- SCHEDULER: periodic tasks with auto-escalation ---
		brain.WithSchedulerOptions(
			scheduler.WithEscalationThreshold(3),
			// Inventory check every 5 minutes. If it fails 3x in a row,
			// escalation kicks in (Silent → Notify → Urgent → ...).
			scheduler.WithTask(scheduler.Task{
				Name:     "inventory-check",
				Interval: 5 * time.Minute,
				Level:    scheduler.Silent,
				Fn: func() error {
					fmt.Println("[scheduler] Running inventory check...")
					// In production: scan RFID tags, compare to expected counts.
					return nil
				},
			}),
			// Heartbeat every 30s.
			scheduler.WithTask(scheduler.Task{
				Name:     "heartbeat",
				Interval: 30 * time.Second,
				Level:    scheduler.Silent,
				Fn: func() error {
					return b.Memory.Store("fleet", "heartbeat", time.Now().Unix())
				},
			}),
		),

		// --- WATCHDOG: monitor hardware health across arms ---
		brain.WithWatchdogAlert(func(name string, err error, consecutive int) {
			if err != nil {
				fmt.Printf("[watchdog] ALERT %s failed %dx: %v\n", name, consecutive, err)
				// In production: send to PagerDuty, Slack, Telegram...
			} else {
				fmt.Printf("[watchdog] RECOVERED: %s is healthy\n", name)
			}
		}),
		brain.WithWatchdogOptions(watchdog.WithAlertAfter(2)),

		// --- HITL: gate risky operations behind human approval ---
		brain.WithHITLNotify(func(req hitl.Request) error {
			fmt.Printf("[hitl] APPROVAL NEEDED: %s (ID: %s, expires: %s)\n",
				req.Description, req.ID, req.Timeout)
			// In production: POST to Telegram bot, webhook, email...
			return nil
		}),

		// --- GUARDIAN: enforce safety policies ---
		brain.WithGuardianOptions(
			guardian.WithPolicy(guardian.Policy{
				Name:        "no-move-while-charging",
				Description: "Prevent arm movement when battery is charging",
				Severity:    guardian.Blocked,
				Check: func(action guardian.Action) guardian.Decision {
					if action.Name == "move_arm" {
						if charging, ok := action.Parameters["charging"].(bool); ok && charging {
							return guardian.Decision{
								Allowed:  false,
								Reason:   "arm movement blocked: battery charging",
								Severity: guardian.Blocked,
							}
						}
					}
					return guardian.Decision{Allowed: true, Severity: guardian.Info}
				},
			}),
			guardian.WithPolicy(guardian.Policy{
				Name:        "weight-limit",
				Description: "Warn when payload exceeds 10kg",
				Severity:    guardian.Warning,
				Check: func(action guardian.Action) guardian.Decision {
					if action.Name == "pick" {
						if weight, ok := action.Parameters["weight_kg"].(float64); ok && weight > 10 {
							return guardian.Decision{
								Allowed:  true,
								Reason:   fmt.Sprintf("payload %.1fkg exceeds 10kg limit", weight),
								Severity: guardian.Warning,
							}
						}
					}
					return guardian.Decision{Allowed: true, Severity: guardian.Info}
				},
			}),
		),

		// --- ROUTING: assign tasks to the best-performing arm ---
		brain.WithRoutingOptions(
			routing.WithWorker(routing.Worker{
				Name:         "arm-alpha",
				Capabilities: []string{"pick", "place", "scan"},
			}),
			routing.WithWorker(routing.Worker{
				Name:         "arm-beta",
				Capabilities: []string{"pick", "place"},
			}),
			routing.WithWorker(routing.Worker{
				Name:         "arm-gamma",
				Capabilities: []string{"pick", "place", "weld"},
			}),
			routing.WithExplorationRate(0.1),  // 10% chance of trying a non-optimal arm
			routing.WithRecencyWindow(time.Hour),
		),

		// --- LIFECYCLE: auto-prune old telemetry ---
		brain.WithLifecycleOptions(
			lifecycle.WithRule(lifecycle.Rule{Namespace: "telemetry", Tier: lifecycle.Telemetry}), // 14 days
			lifecycle.WithRule(lifecycle.Rule{Namespace: "fleet", Tier: lifecycle.Low}),            // 7 days
			lifecycle.WithRule(lifecycle.Rule{Namespace: "config", Tier: lifecycle.Critical}),      // never
			lifecycle.WithPruneInterval(10 * time.Minute),
		),

		// --- BROWSER: monitor warehouse dashboard ---
		brain.WithBrowserOptions(
			// In production: supply a real CDP transport
			// browser.WithTransport(myCDPTransport),
			// browser.WithEndpoint("ws://localhost:9222"),
		),

		// --- WORK: the main robot logic ---
		brain.WithWork(func(r *gobot.Robot) {
			fmt.Println("=== Warehouse fleet online ===")

			// Add watchdog checks for each arm.
			for _, arm := range []string{"arm-alpha", "arm-beta", "arm-gamma"} {
				armName := arm
				b.Watchdog.AddCheck(watchdog.Check{
					Name:     armName + "-motor",
					Interval: 10 * time.Second,
					Timeout:  5 * time.Second,
					Fn: func() error {
						// Simulate: 95% of the time motors are fine.
						if rand.Float64() < 0.05 {
							return fmt.Errorf("%s motor overheating", armName)
						}
						return nil
					},
				})
			}

			// Simulate a pick-and-place cycle.
			gobot.Every(15*time.Second, func() {
				// 1. ROUTING: pick the best arm for this task.
				worker, err := b.Routing.Route("pick")
				if err != nil {
					log.Printf("routing error: %v", err)
					return
				}
				fmt.Printf("[routing] Task 'pick' assigned to: %s\n", worker)

				// 2. GUARDIAN: check safety before moving.
				action := guardian.Action{
					Name:   "pick",
					Source: worker,
					Parameters: map[string]interface{}{
						"weight_kg": 5.0 + rand.Float64()*8, // 5-13kg
						"charging":  false,
					},
				}
				decision := b.Guardian.Evaluate(action)
				if !decision.Allowed {
					fmt.Printf("[guardian] BLOCKED: %s\n", decision.Reason)
					return
				}
				if decision.Severity >= guardian.Warning {
					fmt.Printf("[guardian] WARNING: %s\n", decision.Reason)
				}

				// 3. Execute the pick (in production: send commands to hardware).
				success := rand.Float64() < 0.85 // 85% success rate
				fmt.Printf("[%s] Pick %s\n", worker, map[bool]string{true: "succeeded", false: "failed"}[success])

				// 4. ROUTING: report the result to update confidence scores.
				b.Routing.Report(routing.Result{
					Worker:   worker,
					TaskType: "pick",
					Success:  success,
					Time:     time.Now(),
				})

				// 5. MEMORY: log telemetry.
				b.Memory.Store("telemetry", fmt.Sprintf("pick-%d", time.Now().UnixMilli()), map[string]interface{}{
					"worker":  worker,
					"success": success,
					"weight":  action.Parameters["weight_kg"],
					"time":    time.Now().Format(time.RFC3339),
				})

				// 6. LIFECYCLE: track the telemetry entry for future pruning.
				b.Lifecycle.Track("telemetry", fmt.Sprintf("pick-%d", time.Now().UnixMilli()), lifecycle.Telemetry)
			})

			// Simulate a risky operation that needs human approval.
			gobot.After(1*time.Minute, func() {
				_, err := b.HITL.RequestApproval(hitl.Request{
					Description: "Firmware update v2.4.1 for all arms",
					Timeout:     30 * time.Minute,
					Action: func() error {
						fmt.Println("[hitl] Firmware v2.4.1 deployed to all arms!")
						return b.Memory.Store("config", "firmware_version", "2.4.1")
					},
				})
				if err != nil {
					log.Printf("HITL error: %v", err)
				}
			})

			// Use inference to diagnose issues when watchdog fires.
			b.Watchdog.On("unhealthy", func(data interface{}) {
				// Ask the LLM what might be wrong.
				prompt := fmt.Sprintf(
					"A robot arm motor health check is failing. The error is: %v. "+
						"What are the most likely causes and recommended actions? Be brief.",
					data,
				)
				result, err := b.Inference.InferWithFramework(inference.ChainOfThought, prompt)
				if err != nil {
					log.Printf("inference error: %v", err)
					return
				}
				fmt.Printf("[inference] Diagnosis: %s\n", result)
			})

			// Print routing scores every minute.
			gobot.Every(1*time.Minute, func() {
				scores := b.Routing.Scores("pick")
				fmt.Printf("[routing] Confidence scores: %v\n", scores)
			})
		}),
	)

	if err := b.Robot.Start(); err != nil {
		log.Fatal(err)
	}
}
