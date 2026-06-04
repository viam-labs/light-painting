// Command smoketest drives the light-painting-controller on a locally-running
// viam-server (see test/local-config.json) without the web UI. It exercises
// get_plane, set_plane, home, a small paint_path, and stop, printing results.
//
// Usage: go run ./cmd/smoketest [address]   (default localhost:8090)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/robot/client"
	"go.viam.com/rdk/services/generic"
	"go.viam.com/utils/rpc"
)

func main() {
	addr := "localhost:8090"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	if err := run(addr); err != nil {
		fmt.Fprintln(os.Stderr, "smoketest failed:", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	ctx := context.Background()
	logger := logging.NewLogger("smoketest")

	machine, err := client.New(ctx, addr, logger,
		client.WithDisableSessions(),
		client.WithDialOptions(rpc.WithInsecure()),
	)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	defer machine.Close(ctx)
	fmt.Println("connected:", machine.ResourceNames())

	painter, err := generic.FromRobot(machine, "painter")
	if err != nil {
		return fmt.Errorf("get painter service: %w", err)
	}

	do := func(label string, cmd map[string]interface{}) error {
		resp, err := painter.DoCommand(ctx, cmd)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		b, _ := json.Marshal(resp)
		fmt.Printf("%-12s -> %s\n", label, b)
		return nil
	}

	if err := do("get_plane", map[string]interface{}{"command": "get_plane"}); err != nil {
		return err
	}

	// Adjust the plane (shrink + shift), then read it back.
	if err := do("set_plane", map[string]interface{}{
		"command": "set_plane",
		"origin":  map[string]interface{}{"x": 320, "y": 120, "z": 480},
		"width_mm":  240,
		"height_mm": 240,
	}); err != nil {
		return err
	}

	if err := do("home", map[string]interface{}{"command": "home"}); err != nil {
		return err
	}

	// Paint a square outline.
	square := []map[string]interface{}{
		{"u": 0.2, "v": 0.2},
		{"u": 0.8, "v": 0.2},
		{"u": 0.8, "v": 0.8},
		{"u": 0.2, "v": 0.8},
		{"u": 0.2, "v": 0.2},
	}
	if err := do("paint_path", map[string]interface{}{
		"command": "paint_path",
		"strokes": []map[string]interface{}{{"points": square}},
	}); err != nil {
		return err
	}

	if err := do("stop", map[string]interface{}{"command": "stop"}); err != nil {
		return err
	}

	fmt.Println("smoketest OK")
	return nil
}
