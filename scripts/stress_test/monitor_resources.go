package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Flags
var (
	targetPID = flag.Int("pid", 0, "Target Process ID to monitor (Numind Server)")
	interval  = flag.Duration("interval", 1*time.Second, "Monitoring interval")
	output    = flag.String("output", "performance_report.md", "Output report file")
)

type StatPoint struct {
	Timestamp  time.Time
	CPUPercent float64 // Process CPU usage
	MemMB      float64 // RSS memory in MB
	Goroutines int     // App specific - hard to get from outside without debug endpoint, will use thread count as proxy or just system metrics
	Threads    int
}

func main() {
	flag.Parse()

	if *targetPID == 0 {
		fmt.Println("Error: Please provide -pid <process_id>")
		fmt.Println("Usage: go run monitor_resources.go -pid 12345")
		return
	}

	fmt.Printf("Starting monitoring for PID: %d\n", *targetPID)
	fmt.Printf("Press Ctrl+C to stop and generate report.\n")

	stats := []StatPoint{}

	// Handle Ctrl+C
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	startTime := time.Now()

	go func() {
		<-c
		fmt.Println("\nStopping monitoring...")
		generateReport(stats, startTime)
		os.Exit(0)
	}()

	for range ticker.C {
		stat, err := getProcessStats(*targetPID)
		if err != nil {
			fmt.Printf("Error getting stats (Process died?): %v\n", err)
			break
		}
		stats = append(stats, stat)
		fmt.Printf("\r[%s] CPU: %.1f%% | Mem: %.1f MB | Threads: %d",
			stat.Timestamp.Format("15:04:05"), stat.CPUPercent, stat.MemMB, stat.Threads)
	}
}

func getProcessStats(pid int) (StatPoint, error) {
	// Cross-platform way usually involves 'gopsutil' lib, but to keep dependencies zero,
	// we use 'ps' command on Mac/Linux.

	// ps -o %cpu,rss,thcount -p PID
	cmd := exec.Command("ps", "-o", "%cpu,rss,count", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		return StatPoint{}, err
	}

	// Output format is like:
	// %CPU   RSS THCNT
	//  0.0  1234    12

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return StatPoint{}, fmt.Errorf("no process found")
	}

	fields := strings.Fields(lines[1])
	if len(fields) < 3 {
		return StatPoint{}, fmt.Errorf("parse error")
	}

	cpu, _ := strconv.ParseFloat(fields[0], 64)
	rssKB, _ := strconv.ParseFloat(fields[1], 64)
	threads, _ := strconv.Atoi(fields[2])

	return StatPoint{
		Timestamp:  time.Now(),
		CPUPercent: cpu,
		MemMB:      rssKB / 1024.0,
		Threads:    threads,
	}, nil
}

func generateReport(stats []StatPoint, start time.Time) {
	f, err := os.Create(*output)
	if err != nil {
		fmt.Printf("Failed to create report: %v\n", err)
		return
	}
	defer f.Close()

	if len(stats) == 0 {
		f.WriteString("# Performance Report\nNo data collected.\n")
		return
	}

	// Basic Stats
	var maxCPU, sumCPU float64
	var maxMem, sumMem float64
	minMem := stats[0].MemMB

	for _, s := range stats {
		if s.CPUPercent > maxCPU {
			maxCPU = s.CPUPercent
		}
		sumCPU += s.CPUPercent
		if s.MemMB > maxMem {
			maxMem = s.MemMB
		}
		if s.MemMB < minMem {
			minMem = s.MemMB
		}
		sumMem += s.MemMB
	}
	avgCPU := sumCPU / float64(len(stats))
	avgMem := sumMem / float64(len(stats))

	duration := time.Since(start)

	// Write Markdown
	fmt.Fprintf(f, "# Performance Test Report\n\n")
	fmt.Fprintf(f, "**Target PID:** %d\n", *targetPID)
	fmt.Fprintf(f, "**Duration:** %v\n", duration)
	fmt.Fprintf(f, "**Data Points:** %d\n\n", len(stats))

	fmt.Fprintf(f, "## Summary\n")
	fmt.Fprintf(f, "| Metric | Average | Max | Min |\n")
	fmt.Fprintf(f, "| :--- | :--- | :--- | :--- |\n")
	fmt.Fprintf(f, "| **CPU** | %.1f%% | %.1f%% | - |\n", avgCPU, maxCPU)
	fmt.Fprintf(f, "| **Memory** | %.1f MB | %.1f MB | %.1f MB |\n", avgMem, maxMem, minMem)

	fmt.Fprintf(f, "\n## Potential Leaks Analysis\n")
	memGrowth := stats[len(stats)-1].MemMB - stats[0].MemMB
	if memGrowth > 50 { // Arbitrary threshold of 50MB growth
		fmt.Fprintf(f, "⚠️ **Warning:** Memory grew by %.1f MB during the test. Investigating per-minute growth is recommended.\n", memGrowth)
	} else {
		fmt.Fprintf(f, "✅ Memory usage appears stable (Net change: %.1f MB)\n", memGrowth)
	}

	fmt.Fprintf(f, "\n## Timeline Data (Sampled)\n")
	fmt.Fprintf(f, "Time | CPU | Memory | Threads\n")
	fmt.Fprintf(f, "--- | --- | --- | ---\n")

	// Sample up to 20 points for table to avoid huge file
	step := 1
	if len(stats) > 20 {
		step = len(stats) / 20
	}

	for i := 0; i < len(stats); i += step {
		s := stats[i]
		fmt.Fprintf(f, "%s | %.1f%% | %.1f MB | %d\n", s.Timestamp.Format("15:04:05"), s.CPUPercent, s.MemMB, s.Threads)
	}

	fmt.Printf("\nReport saved to %s\n", *output)
}
