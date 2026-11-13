// WARDOGS faction auto-joiner (Windows .exe, no dependencies)
//
// How it works:
//   1. Calibrate once: hover each faction button (Red/Blue/Green) and press F6.
//      Then hover a "watch" spot that is only visible on the faction-select
//      screen (the faction panel/background works well) and press F6.
//   2. Pick a team. Press F8 to start/pause clicking, F9 to abort.
//   3. The watch spot's color is recorded when you start. When it changes,
//      clicks pause. If it stays changed for confirm_ms (default 2.5s) you're
//      in and it stops; if the faction screen comes back, clicking resumes.
//
// Run the game in Borderless/Windowed mode - exclusive fullscreen can make
// pixel reads return black, which breaks auto-stop detection.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")
	shcore = syscall.NewLazyDLL("shcore.dll")
	kernel = syscall.NewLazyDLL("kernel32.dll")

	pGetAsyncKeyState       = user32.NewProc("GetAsyncKeyState")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pSetCursorPos           = user32.NewProc("SetCursorPos")
	pMouseEvent             = user32.NewProc("mouse_event")
	pGetDC                  = user32.NewProc("GetDC")
	pReleaseDC              = user32.NewProc("ReleaseDC")
	pGetForegroundWindow    = user32.NewProc("GetForegroundWindow")
	pGetWindowTextW         = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW   = user32.NewProc("GetWindowTextLengthW")
	pSetProcessDPIAware     = user32.NewProc("SetProcessDPIAware")
	pSetProcessDpiAwareness = shcore.NewProc("SetProcessDpiAwareness")
	pGetPixel               = gdi32.NewProc("GetPixel")
	pBeep                   = kernel.NewProc("Beep")
)

const (
	vkF6       = 0x75
	vkF8       = 0x77
	vkF9       = 0x78
	leftDown   = 0x0002
	leftUp     = 0x0004
	clrInvalid = 0xFFFFFFFF
)

var teams = []string{"red", "blue", "green"}
var teamLabels = map[string]string{"red": "RED (Valkyra)", "blue": "BLUE (Lonestar)", "green": "GREEN (Manticore)"}

type Config struct {
	Positions       map[string][2]int `json:"positions"`         // team -> [x, y]
	Watch           *[2]int           `json:"watch"`             // pixel that changes once you're on a team
	ClickIntervalMs int               `json:"click_interval_ms"` // time between clicks (~25 clicks/sec)
	ColorTolerance  int               `json:"color_tolerance"`   // summed RGB difference that counts as "changed"
	ConfirmMs       int               `json:"confirm_ms"`        // how long the screen must stay changed to count as joined
	WindowTitle     string            `json:"window_title"`      // only click while this window is focused ("" = any)
}

type rgb struct{ r, g, b int }

type point struct{ X, Y int32 }

// ---------- helpers ----------

var keyPrev = map[int]bool{}

// tapped returns true once per key press (edge-triggered)
func tapped(vk int) bool {
	r, _, _ := pGetAsyncKeyState.Call(uintptr(vk))
	down := r&0x8000 != 0
	was := keyPrev[vk]
	keyPrev[vk] = down
	return down && !was
}

func cursorPos() (int, int) {
	var p point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y)
}

func getPixel(x, y int) (rgb, bool) {
	hdc, _, _ := pGetDC.Call(0)
	c, _, _ := pGetPixel.Call(hdc, uintptr(int32(x)), uintptr(int32(y)))
	pReleaseDC.Call(0, hdc)
	c &= 0xFFFFFFFF
	if c == clrInvalid {
		return rgb{}, false
	}
	return rgb{int(c & 0xFF), int((c >> 8) & 0xFF), int((c >> 16) & 0xFF)}, true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func colorDiff(a, b rgb) int {
	return abs(a.r-b.r) + abs(a.g-b.g) + abs(a.b-b.b)
}

func foregroundTitle() string {
	hwnd, _, _ := pGetForegroundWindow.Call()
	n, _, _ := pGetWindowTextLengthW.Call(hwnd)
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func gameFocused(cfg *Config) bool {
	if cfg.WindowTitle == "" {
		return true
	}
	return strings.Contains(strings.ToLower(foregroundTitle()), strings.ToLower(cfg.WindowTitle))
}

func click(x, y int) {
	// Nudge the cursor so the game registers a hover before the click
	pSetCursorPos.Call(uintptr(int32(x+1)), uintptr(int32(y)))
	pSetCursorPos.Call(uintptr(int32(x)), uintptr(int32(y)))
	pMouseEvent.Call(leftDown, 0, 0, 0, 0)
	time.Sleep(12 * time.Millisecond)
	pMouseEvent.Call(leftUp, 0, 0, 0, 0)
}

func beep(freq, ms int) {
	pBeep.Call(uintptr(freq), uintptr(ms))
}

func configPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "wardogs_clicker.json"
	}
	return filepath.Join(filepath.Dir(exe), "wardogs_clicker.json")
}

func loadConfig() *Config {
	cfg := &Config{
		Positions:       map[string][2]int{},
		ClickIntervalMs: 40,
		ColorTolerance:  60,
		ConfirmMs:       2500,
		WindowTitle:     "WARDOGS",
	}
	data, err := os.ReadFile(configPath())
	if err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			fmt.Println("Couldn't read config, using defaults:", err)
		}
	}
	if cfg.Positions == nil {
		cfg.Positions = map[string][2]int{}
	}
	return cfg
}

func saveConfig(cfg *Config) {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(configPath(), data, 0644); err != nil {
		fmt.Println("Couldn't save config:", err)
	}
}

func waitForF6(prompt string) (int, int, bool) {
	fmt.Println(prompt + "  [F6 = capture, F9 = cancel]")
	tapped(vkF6)
	tapped(vkF9)
	for {
		if tapped(vkF6) {
			x, y := cursorPos()
			c, _ := getPixel(x, y)
			fmt.Printf("   -> captured (%d, %d), color %v\n", x, y, c)
			time.Sleep(200 * time.Millisecond)
			return x, y, true
		}
		if tapped(vkF9) {
			return 0, 0, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------- calibration ----------

func calibrate(cfg *Config) {
	fmt.Println("\n=== CALIBRATION ===")
	fmt.Println("Open WARDOGS to the Select Faction screen.")
	for _, team := range teams {
		x, y, ok := waitForF6("Hover the " + teamLabels[team] + " join button")
		if !ok {
			fmt.Println("Calibration cancelled.")
			return
		}
		cfg.Positions[team] = [2]int{x, y}
	}

	x, y, ok := waitForF6("Hover a WATCH spot: something on the faction screen that disappears once you're " +
		"on a team (NOT on a button - hover effects change its color)")
	if !ok {
		fmt.Println("Calibration cancelled.")
		return
	}
	cfg.Watch = &[2]int{x, y}

	title := foregroundTitle()
	fmt.Printf("Focused window title: '%s'\n", title)
	if cfg.WindowTitle != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(cfg.WindowTitle)) {
		fmt.Printf("   Warning: doesn't contain '%s'. Edit window_title in wardogs_clicker.json if clicks never fire.\n", cfg.WindowTitle)
	}

	saveConfig(cfg)
	fmt.Println("Saved.")
}

// ---------- clicking ----------

func run(cfg *Config, team string) {
	pos, ok := cfg.Positions[team]
	if !ok || cfg.Watch == nil {
		fmt.Println("Not calibrated yet - press c to calibrate first.")
		return
	}

	tx, ty := pos[0], pos[1]
	wx, wy := cfg.Watch[0], cfg.Watch[1]
	interval := time.Duration(cfg.ClickIntervalMs) * time.Millisecond
	confirm := time.Duration(cfg.ConfirmMs) * time.Millisecond

	fmt.Printf("\nTarget: %s at (%d, %d)\n", teamLabels[team], tx, ty)
	fmt.Println("Switch to the game. F8 = start/pause, F9 = abort.")

	tapped(vkF8)
	tapped(vkF9)
	running := false
	var baseline rgb
	clicks := 0
	var started, changedSince, backSince time.Time

	for {
		if tapped(vkF9) {
			fmt.Printf("\nAborted after %d clicks.\n", clicks)
			return
		}
		if tapped(vkF8) {
			running = !running
			if running {
				var ok bool
				baseline, ok = getPixel(wx, wy)
				if !ok || baseline == (rgb{}) {
					fmt.Println("Watch pixel reads black/empty - use Borderless mode or recalibrate.")
				}
				started = time.Now()
				changedSince = time.Time{}
				backSince = time.Time{}
				fmt.Printf("RUNNING (baseline %v)\n", baseline)
			} else {
				fmt.Println("PAUSED")
			}
		}

		if !running {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		if !gameFocused(cfg) {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		cur, ok := getPixel(wx, wy)
		isChanged := ok && colorDiff(cur, baseline) > cfg.ColorTolerance

		// Screen changed: stop clicking and wait to see if the join sticks
		// ("joining..." also changes the screen, so one change isn't enough)
		if isChanged {
			backSince = time.Time{}
			if changedSince.IsZero() {
				changedSince = time.Now()
				fmt.Println("Screen changed - holding clicks, confirming join...")
			}
			if time.Since(changedSince) >= confirm {
				fmt.Printf("\nJOINED %s - stopped after %d clicks (%.1fs).\n",
					teamLabels[team], clicks, time.Since(started).Seconds())
				beep(1200, 150)
				beep(1600, 200)
				return
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}

		// Faction screen is back: the join failed, so resume once it's stable for 200ms
		if !changedSince.IsZero() {
			if backSince.IsZero() {
				backSince = time.Now()
			}
			if time.Since(backSince) < 200*time.Millisecond {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			fmt.Println("Back on faction screen - join failed, resuming clicks.")
			changedSince = time.Time{}
			backSince = time.Time{}
		}

		click(tx, ty)
		clicks++
		time.Sleep(interval)
	}
}

// ---------- menu ----------

func main() {
	// Make coordinates match real screen pixels on scaled (125%/150%) displays
	if pSetProcessDpiAwareness.Find() == nil {
		pSetProcessDpiAwareness.Call(2)
	} else {
		pSetProcessDPIAware.Call()
	}

	cfg := loadConfig()
	in := bufio.NewScanner(os.Stdin)

	for {
		fmt.Println("\n=== WARDOGS Team Joiner ===")
		fmt.Println(" 1) Red (Valkyra)   2) Blue (Lonestar)   3) Green (Manticore)")
		fmt.Println(" c) Calibrate       q) Quit")
		fmt.Print("> ")
		if !in.Scan() {
			return
		}
		switch strings.ToLower(strings.TrimSpace(in.Text())) {
		case "1":
			run(cfg, "red")
		case "2":
			run(cfg, "blue")
		case "3":
			run(cfg, "green")
		case "c":
			calibrate(cfg)
		case "q":
			return
		}
	}
}
