//go:build linux

package sysmetrics

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

func NewProcessCPUSampler() *ProcessCPUSampler {
	return &ProcessCPUSampler{}
}

func (s *ProcessCPUSampler) Sample() (float64, bool, error) {
	processTicks, err := readProcessTicks()
	if err != nil {
		return 0, false, err
	}
	totalTicks, err := readTotalTicks()
	if err != nil {
		return 0, false, err
	}
	if !s.initialized {
		s.lastProcessTicks = processTicks
		s.lastTotalTicks = totalTicks
		s.initialized = true
		return 0, false, nil
	}
	processDelta := processTicks - s.lastProcessTicks
	totalDelta := totalTicks - s.lastTotalTicks
	s.lastProcessTicks = processTicks
	s.lastTotalTicks = totalTicks
	if totalDelta == 0 {
		return 0, false, nil
	}
	percent := float64(processDelta) / float64(totalDelta) * float64(runtime.NumCPU()) * 100
	return percent, true, nil
}

func readTotalTicks() (uint64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	var total uint64
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, err
		}
		total += value
	}
	return total, nil
}

func readProcessTicks() (uint64, error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	stat := string(data)
	end := strings.LastIndex(stat, ")")
	if end < 0 || end+2 >= len(stat) {
		return 0, strconv.ErrSyntax
	}
	fields := strings.Fields(stat[end+2:])
	if len(fields) < 13 {
		return 0, strconv.ErrSyntax
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}
