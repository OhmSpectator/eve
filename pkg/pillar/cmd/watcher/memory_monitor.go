// Copyright (c) 2025 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package watcher

import (
	"context"
	"encoding/csv"
	"fmt"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"gonum.org/v1/gonum/stat"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

var (
	heapReachedThreshold  int
	rssReachedThreshold   int
	entireSetAnalyzedLast time.Time
)

const (
	// bytesPerLineInFile is an approximation of the number of bytes per stat in the file.
	// The line usually looks like this: 2025-04-22T13:13:15Z,42450944,120524800,0
	bytesPerLineInFile = 42
)

// InternalMemoryMonitorParams is a struct that holds the parameters for the memory monitor.
type InternalMemoryMonitorParams struct {
	mutex               sync.Mutex
	analysisPeriod      time.Duration
	smoothingProbeCount int
	probingInterval     time.Duration
	slopeThreshold      float64
	isActive            bool
	// Context to make the monitoring goroutine cancellable
	context context.Context
	stop    context.CancelFunc
}

func (immp *InternalMemoryMonitorParams) Set(analysisPeriod, probingInterval time.Duration, slopeThreshold float64, smoothingProbeCount int, isActive bool) {
	immp.mutex.Lock()
	immp.analysisPeriod = analysisPeriod
	immp.smoothingProbeCount = smoothingProbeCount
	immp.probingInterval = probingInterval
	immp.slopeThreshold = slopeThreshold
	immp.isActive = isActive
	immp.mutex.Unlock()
}

func (immp *InternalMemoryMonitorParams) Get() (time.Duration, time.Duration, float64, int, bool) {
	immp.mutex.Lock()
	defer immp.mutex.Unlock()
	return immp.analysisPeriod, immp.probingInterval, immp.slopeThreshold, immp.smoothingProbeCount, immp.isActive
}

func (immp *InternalMemoryMonitorParams) MakeStoppable() {
	immp.context, immp.stop = context.WithCancel(context.Background())
}

func (immp *InternalMemoryMonitorParams) isStoppable() bool {
	if immp.context == nil {
		return false
	}
	return immp.context.Err() == nil
}

func (immp *InternalMemoryMonitorParams) checkStopCondition() bool {
	immp.mutex.Lock()
	defer immp.mutex.Unlock()
	if immp.context == nil {
		return false
	}
	select {
	case <-immp.context.Done():
		return true
	default:
		return false
	}
}

func (immp *InternalMemoryMonitorParams) Stop() {
	if immp.stop != nil {
		immp.stop()
	}
}

// medianFilter applies a median filter to a slice of values. windowSize should be odd.
// This reduces the impact of local spikes.
func medianFilter(values []uint64, windowSize int) []uint64 {
	if windowSize < 1 {
		return values
	}
	if windowSize == 1 || windowSize > len(values) {
		// No filtering possible if window size is 1 or larger than data
		log.Tracef("No median filtering possible, returning original values")
		return values
	}
	// Measure the time taken for the median filter
	startTime := time.Now()
	out := make([]uint64, len(values))
	half := windowSize / 2
	for i := range values {
		start := i - half
		end := i + half
		if start < 0 {
			start = 0
		}
		if end >= len(values) {
			end = len(values) - 1
		}
		window := append([]uint64{}, values[start:end+1]...)
		sort.Slice(window, func(i, j int) bool { return window[i] < window[j] })
		mid := len(window) / 2
		out[i] = window[mid]
	}
	// Calculate the time taken for the median filter
	elapsedTime := time.Since(startTime)
	log.Tracef("Median filter applied in %s", elapsedTime)
	log.Noticef("Median filter applied in %s", elapsedTime)
	return out
}

// getRSS reads /proc/self/statm and returns the RSS in bytes.
// Format of /proc/self/statm: size resident share text lib data dt
// We're interested in the second field: 'resident'.
func getRSS() (uint64, error) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, err
	}
	var size, rss uint64
	if _, err := fmt.Sscanf(string(data), "%d %d", &size, &rss); err != nil {
		return 0, err
	}
	pageSize := uint64(os.Getpagesize())
	return rss * pageSize, nil
}

func controlFileSize(filename string, threshold int64) {
	const linesToRemove = 12

	// Keep trimming until size <= threshold (or until we can't trim anymore).
	for {

		// Open the file
		file, err := os.OpenFile(filename, os.O_RDWR, 0644)
		if err != nil {
			log.Warnf("failed to open file: %v", err)
			return
		}

		fi, err := file.Stat()
		if err != nil {
			log.Warnf("failed to get file info: %v", err)
			file.Close()
			return
		}

		// If file size is okay, done
		if fi.Size() <= threshold {
			file.Close()
			return
		}

		fileDir := filepath.Dir(filename)

		// Create a temporary output file
		tmp, err := os.CreateTemp(fileDir, "truncated-*.csv")
		if err != nil {
			log.Warnf("failed to create temp file: %v", err)
			file.Close()
			return
		}

		renamed := false
		deferCleanup := func() {
			tmp.Close()
			if !renamed {
				os.Remove(tmp.Name())
			}
		}

		// We’ll use a named cleanup so we can call it as needed.
		// We won't just do `defer deferCleanup()` right away,
		// because we might close early.
		// Instead, call `deferCleanup()` on any return path.

		reader := csv.NewReader(file)
		writer := csv.NewWriter(tmp)

		// 1) read header
		header, err := reader.Read()
		if err != nil {
			log.Warnf("failed to read header: %v", err)
			file.Close()
			deferCleanup()
			return
		}

		if err := writer.Write(header); err != nil {
			log.Warnf("failed to write header: %v", err)
			file.Close()
			deferCleanup()
			return
		}

		// 2) skip lines
		for i := 0; i < linesToRemove; i++ {
			_, err := reader.Read()
			if err == io.EOF {
				// End of file, nothing else to skip
				break
			}
			if err != nil {
				log.Warnf("error reading csv: %v", err)
				file.Close()
				deferCleanup()
				return
			}
		}

		// 3) copy everything else
		for {
			record, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Warnf("error reading csv: %v", err)
				file.Close()
				deferCleanup()
				return
			}
			if err = writer.Write(record); err != nil {
				log.Warnf("error writing csv: %v", err)
				file.Close()
				deferCleanup()
				return
			}
		}

		// Flush writer
		writer.Flush()
		if err := writer.Error(); err != nil {
			log.Warnf("error flushing csv writer: %v", err)
			file.Close()
			deferCleanup()
			return
		}

		// Close original file
		if err := file.Close(); err != nil {
			log.Warnf("error closing original file: %v", err)
			deferCleanup()
			return
		}

		// Close temp file
		if err := tmp.Close(); err != nil {
			log.Warnf("error closing temp file: %v", err)
			return
		}

		// Overwrite original with truncated version
		if err := os.Rename(tmp.Name(), filename); err != nil {
			log.Warnf("error renaming temp file: %v", err)
			os.Remove(tmp.Name()) // we can do an explicit remove here
			return
		}

		// Mark rename success, so we don't remove the file in defer.
		renamed = true

		// Continue the loop to see if we need more trimming.
		// If we want to do it in multiple passes (again and again),
		// just loop around. If removing lines once is enough, then
		// we break here instead.
	}
}

type usageType string

const (
	heapUsage usageType = "heap"
	rssUsage  usageType = "rss"
)

type suspectLeakType string

const (
	heapOneWindow suspectLeakType = "heapOneWindow"
	rssOneWindow  suspectLeakType = "rssOneWindow"
	heapEntireSet suspectLeakType = "heapEntireSet"
	rssEntireSet  suspectLeakType = "rssEntireSet"
)

type memUsageProbe struct {
	time   time.Time
	usages map[usageType]uint64
	leaks  []suspectLeakType
}

// writeMemoryUsage writes out times, memUsage, and redDots as CSV.
// There's no repeated field name, so it's smaller than typical JSON.
// Then you can load this CSV in any interactive plotting tool.
//func writeMemoryUsage(timeNow time.Time, memUsage []memoryUsage, outPath string) {
//
//	// Measure the time taken for the write operation
//
//	// Open file in append mode, create if it doesn't exist
//	file, err := os.OpenFile(outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
//	if err != nil {
//		fmt.Printf("Error opening file: %v\n", err)
//		return
//	}
//	defer controlFileSize(outPath, 1024*1024) // 1MB
//	defer file.Close()
//
//	w := csv.NewWriter(file)
//
//	// Check if file is empty to write header
//	fileInfo, err := file.Stat()
//	if err != nil {
//		fmt.Printf("Error getting file info: %v\n", err)
//		return
//	}
//
//	if fileInfo.Size() == 0 {
//		// Form header as "time,memory_usage_name1, memory_usage_name2, ... ,red"
//		header := make([]string, 0, len(memUsage)+2)
//		header = append(header, "time")
//		for _, mu := range memUsage {
//			header = append(header, mu.name)
//		}
//		header = append(header, "red")
//		if err = w.Write(header); err != nil {
//			fmt.Printf("Error writing header: %v\n", err)
//			return
//		}
//	}
//
//	record := make([]string, 0, len(memUsage)+2)
//	record = append(record, timeNow.Format(time.RFC3339))
//	for _, mu := range memUsage {
//		record = append(record, strconv.FormatUint(mu.usage, 10))
//	}
//	record = append(record, "0") // Default red value
//
//	// Write the data
//	if err = w.Write(record); err != nil {
//		fmt.Printf("Error writing record: %v\n", err)
//		return
//	}
//
//	// Flush any buffered data to disk
//	w.Flush()
//	if err = w.Error(); err != nil {
//		fmt.Printf("Error flushing CSV writer: %v\n", err)
//		return
//	}
//
//	fmt.Printf("Data exported to %s\n", outPath)
//}

// markLastUsageRed marks the last usage in the file as red.
//func markLastUsageRed(filename string) {
//	// Open the file
//	file, err := os.OpenFile(filename, os.O_RDWR, 0644)
//	if err != nil {
//		log.Warnf("failed to open file: %v", err)
//		return
//	}
//	defer file.Close()
//
//	// Seek to the end of the file
//	offset, err := file.Seek(0, io.SeekEnd)
//	if err != nil {
//		fmt.Println("Error seeking file:", err)
//		return
//	}
//
//	// Read the file backwards until we find the last "0"
//	// and replace it with "1"
//	for {
//		offset--
//		if offset < 0 {
//			fmt.Println("Reached beginning of file without finding 0")
//			return
//		}
//		_, err = file.Seek(offset, io.SeekStart)
//		if err != nil {
//			fmt.Println("Error seeking file:", err)
//			return
//		}
//		b := make([]byte, 1)
//		_, err = file.Read(b)
//		if err != nil {
//			fmt.Println("Error reading file:", err)
//			return
//		}
//		if b[0] == '0' {
//			break
//		}
//	}
//
//	_, err = file.WriteAt([]byte("1"), offset)
//	if err != nil {
//		fmt.Println("Error writing file:", err)
//		return
//	}
//
//	fmt.Println("Last usage marked as red")
//
//	// Flush any buffered data to disk
//	if err := file.Sync(); err != nil {
//		fmt.Printf("Error flushing file: %v\n", err)
//		return
//	}
//	return
//}

//func readFirstStatAvailable(filePath string) (*time.Time, error) {
//	file, err := os.Open(filePath)
//	if err != nil {
//		log.Warnf("failed to open file: %v", err)
//		return nil, err
//	}
//	defer file.Close()
//
//	r := csv.NewReader(file)
//
//	// Read the header
//	_, err = r.Read()
//	if err != nil {
//		log.Warnf("failed to read csv header: %v", err)
//		return nil, err
//	}
//
//	// Read the first record after the header to parse the timestamp
//	record, err := r.Read()
//	if err != nil {
//		log.Warnf("failed to read csv record: %v", err)
//		return nil, err
//	}
//
//	timeStr := record[0]
//	timeRead, err := time.Parse(time.RFC3339, timeStr)
//	if err != nil {
//		log.Warnf("failed to parse time: %v", err)
//		return nil, err
//	}
//
//	return &timeRead, nil
//}

func getProbesNewerThan(stats []memUsageProbe, timeCutoff time.Time) ([]memUsageProbe, error) {
	for i, curStat := range stats {
		if !curStat.time.Before(timeCutoff) {
			return stats[i:], nil
		}
	}
	log.Warnf("no samples found newer than %v", timeCutoff)
	return nil, fmt.Errorf("no samples found newer than %v", timeCutoff)
}

//func readSamplesNewerThan(filePath string, timeCutoff time.Time) ([]memoryUsage, error) {
//	file, err := os.Open(filePath)
//	if err != nil {
//		log.Warnf("failed to open file: %v", err)
//		return nil, err
//	}
//	defer file.Close()
//
//	r := csv.NewReader(file)
//	records, err := r.ReadAll()
//	if err != nil {
//		log.Warnf("failed to read csv: %v", err)
//		return nil, err
//	}
//
//	if len(records) < 2 {
//		log.Warnf("no records found in file")
//		err = fmt.Errorf("no records found in file")
//		return nil, err
//	}
//
//	// Parse the header to fill the names
//	header := records[0]
//	names := make([]string, len(header)-2) // Exclude time and red
//	for i := 1; i < len(header)-1; i++ {
//		names[i-1] = header[i]
//	}
//
//	var values []memoryUsage
//	for _, record := range records[1:] {
//		timeStr := record[0]
//		timeRead, err := time.Parse(time.RFC3339, timeStr)
//		if err != nil {
//			log.Warnf("failed to parse time: %v", err)
//			continue
//		}
//		if timeRead.Before(timeCutoff) {
//			continue
//		}
//		// Parse the values for all the names in the header
//		for i := 1; i < len(record)-1; i++ {
//			value, err := strconv.ParseUint(record[i], 10, 64)
//			if err != nil {
//				log.Warnf("failed to parse value: %v", err)
//				continue
//			}
//			values = append(values, memoryUsage{usage: value, name: names[i-1], time: timeRead})
//		}
//	}
//
//	return values, nil
//}

func appendMemoryUsage(stats []memUsageProbe, newStat memUsageProbe) []memUsageProbe {
	stats = append(stats, newStat)
	if size := len(stats) * bytesPerLineInFile; size > 1024*1024 { // 1MB
		probesToRemove := size/bytesPerLineInFile - 1024*1024/bytesPerLineInFile
		stats = stats[probesToRemove:]
	}
	return stats
}

func performMemoryLeakDetection(analysisPeriod time.Duration, slopeThreshold float64, smoothingProbeCount int, stats []memUsageProbe) []memUsageProbe {
	// Measure the time taken for a single memory leak detection
	startTime := time.Now()

	// Get the current memory usage
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	heapInUse := m.HeapInuse
	rss, err := getRSS()
	if err != nil {
		log.Errorf("Failed to read RSS: %v\n", err)
		return stats
	}
	log.Tracef("Heap usage: %d MB, RSS: %.2f MB\n", heapInUse/1024/1024, float64(rss)/1024/1024)

	timeNow := time.Now()

	// Write memory usage to CSV
	memUsage := memUsageProbe{
		time: timeNow,
		usages: map[usageType]uint64{
			heapUsage: heapInUse,
			rssUsage:  rss,
		},
	}
	stats = appendMemoryUsage(stats, memUsage)
	log.Noticef("Currently accumulated %d samples\n", len(stats))
	// writeMemoryUsage(timeNow, memUsage, fileName)
	firstStatAvailable := stats[0].time

	if timeNow.Sub(firstStatAvailable) < analysisPeriod {
		log.Noticef("Not enough samples to perform regression, waiting for %s\n", analysisPeriod)
		return stats
	}

	// Calculate the time of the first sample to be used in the regression
	firstSampleTime := timeNow.Add(analysisPeriod * -1)

	// Read all samples newer than the first sample time
	probes, err := getProbesNewerThan(stats, firstSampleTime)
	if err != nil {
		log.Warnf("failed to read samples: %v", err)
		return stats
	}

	heapValues := make([]uint64, 0)
	rssValues := make([]uint64, 0)
	times := make([]float64, 0)
	for _, probe := range probes {
		times = append(times, probe.time.Sub(firstSampleTime).Seconds())
		heapValues = append(heapValues, probe.usages[heapUsage])
		rssValues = append(rssValues, probe.usages[rssUsage])
	}

	// Get a timestamp for the filename
	// Smooth the values via a median filter to reduce spikes
	smoothedHeapValues := medianFilter(heapValues, smoothingProbeCount)
	smoothedRSSValues := medianFilter(rssValues, smoothingProbeCount)

	heapSlope := linearRegressionSlope(times, smoothedHeapValues)
	RSSSlope := linearRegressionSlope(times, smoothedRSSValues)
	//If slope is positive and above a certain threshold, print a warning
	if heapSlope > slopeThreshold {
		probes[len(probes)-1].leaks = append(probes[len(probes)-1].leaks, heapOneWindow)
		heapReachedThreshold++
		if heapReachedThreshold > 10 {
			log.Warnf("Potential memory leak (heap) detected: slope %.2f > %.2f\n", heapSlope, slopeThreshold)
		}
	} else {
		heapReachedThreshold = 0
	}
	if RSSSlope > slopeThreshold {
		probes[len(probes)-1].leaks = append(probes[len(probes)-1].leaks, rssOneWindow)
		rssReachedThreshold++
		if rssReachedThreshold > 10 && heapReachedThreshold > 0 { // Just RSS growth most probably is caused by page cache
			log.Warnf("Potential memory leak (RSS) detected: slope %.2f > %.2f\n", RSSSlope, slopeThreshold)
		}
	} else {
		rssReachedThreshold = 0
	}
	if entireSetAnalyzedLast.IsZero() {
		entireSetAnalyzedLast = probes[0].time
	}
	// Once per 5 periods, perform analysis on entire data set
	if timeNow.Sub(entireSetAnalyzedLast) >= analysisPeriod*5 {
		entireSetAnalyzedLast = timeNow
		log.Noticef("Performing analysis on entire data set")
		// Read all samples
		probes = stats
		heapValues = make([]uint64, 0)
		rssValues = make([]uint64, 0)
		times = make([]float64, 0)
		for _, probe := range probes {
			times = append(times, probe.time.Sub(firstSampleTime).Seconds())
			heapValues = append(heapValues, probe.usages[heapUsage])
			rssValues = append(rssValues, probe.usages[rssUsage])
		}
		// Perform smoothing with a bigger window (x5)
		smoothedHeapValues = medianFilter(heapValues, smoothingProbeCount*5)
		smoothedRSSValues = medianFilter(rssValues, smoothingProbeCount*5)
		heapSlope = linearRegressionSlope(times, smoothedHeapValues)
		RSSSlope = linearRegressionSlope(times, smoothedRSSValues)
		log.Noticef("Heap slope: %.2f, RSS slope: %.2f\n", heapSlope, RSSSlope)

		// If slope is positive and above a certain threshold, print a warning
		if heapSlope > slopeThreshold {
			log.Warnf("Potential memory leak (heap) detected on a entire data set: slope %.2f > %.2f\n", heapSlope, slopeThreshold/5)
			probes[len(probes)-1].leaks = append(probes[len(probes)-1].leaks, heapEntireSet)
		}
		if RSSSlope > slopeThreshold {
			log.Warnf("Potential memory leak (RSS) detected on a entire data set: slope %.2f > %.2f\n", RSSSlope, slopeThreshold/5)
			probes[len(probes)-1].leaks = append(probes[len(probes)-1].leaks, rssEntireSet)
		}
	}
	// Calculate the time taken for the memory leak detection
	elapsedTime := time.Since(startTime)
	log.Noticef("Memory leak detection performed in %s, accumulated %d samples\n", elapsedTime, len(stats))
	return stats
}

func writeMemoryUsage(stats []memUsageProbe, fileName string) {
	log.Noticef("Writing memory usage to %s\n", fileName)
	// Open file, truncate if it exists and create if it doesn't
	file, err := os.OpenFile(fileName, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Warnf("failed to open file: %v", err)
		return
	}
	defer file.Close()

	w := csv.NewWriter(file)

	// Form header as "time,memory_usage_name1, memory_usage_name2, ... ,red"
	header := []string{"time", "heap", "rss", "red"}
	if err = w.Write(header); err != nil {
		log.Warnf("failed to write header: %v", err)
		return
	}

	log.Noticef("Header written to %s\n", fileName)

	for _, probe := range stats {
		var record []string
		// Add timestamp first
		record = append(record, probe.time.Format(time.RFC3339))
		// Add heap and RSS values
		record = append(record, fmt.Sprintf("%d", probe.usages[heapUsage]))
		record = append(record, fmt.Sprintf("%d", probe.usages[rssUsage]))
		if len(probe.leaks) > 0 {
			// Add red value if there are leaks
			record = append(record, "1")
		} else {
			record = append(record, "0") // Default red value
		}
		if err = w.Write(record); err != nil {
			log.Warnf("failed to write record: %v", err)
			return
		}
	}

	log.Noticef("Records written to %s\n", fileName)

	w.Flush()
	if err = w.Error(); err != nil {
		log.Warnf("failed to flush CSV writer: %v", err)
		return
	}

	log.Noticef("Data exported to %s\n", fileName)
}

// This goroutine periodically captures memory usage stats and attempts to detect
// a potential memory leak by looking at the trend of heap usage over time. It uses a
// simple linear regression to estimate whether heap memory usage is consistently rising.
//
// Note that this is a simplistic heuristic and can produce false positives or fail to detect
// subtle leaks. In a real-world scenario, you'd likely want more robust logic or integrate
// with profiling tools.

func InternalMemoryMonitor(ctx *watcherContext) {
	log.Functionf("Starting internal memory monitor (stoppable: %v)", ctx.IMMParams.isStoppable())
	log.Tracef("Starting internal memory monitor")
	log.Warnf("#ohm: Internal memory monitor started")
	// Create a slice to accumulate memory usage data
	stats := make([]memUsageProbe, 0)
	statsWrittenLastTime := time.Now()
	// Get the initial memory leak detection parameters
	for {
		analysisPeriod, probingInterval, slopeThreshold, smoothingProbeCount, isActive := ctx.IMMParams.Get()
		// Check if we have to stop
		if ctx.IMMParams.isStoppable() && ctx.IMMParams.checkStopCondition() {
			log.Functionf("Stopping internal memory monitor")
			log.Warnf("#ohm: Internal memory monitor stopped")
			return
		}
		if isActive {
			log.Noticef("#ohm: Internal memory monitor is active, performing memory leak detection")
			stats = performMemoryLeakDetection(analysisPeriod, slopeThreshold, smoothingProbeCount, stats)
		}
		// Write memory usage to CSV once per 1 minute
		if time.Since(statsWrittenLastTime) >= 1*time.Minute {
			log.Noticef("#ohm: Writing memory usage to CSV")
			// Write memory usage to CSV
			fileName := filepath.Join(types.MemoryMonitorOutputDir, "memory_usage.csv")
			writeMemoryUsage(stats, fileName)
			statsWrittenLastTime = time.Now()
		}

		// Sleep for the probing interval
		time.Sleep(probingInterval)
	}
}

// linearRegressionSlope calculates the slope (beta) via Gonum's LinearRegression.
func linearRegressionSlope(xs []float64, ys []uint64) float64 {
	// Basic sanity check
	if len(xs) != len(ys) || len(xs) < 2 {
		log.Warnf("#ohm: Insufficient data for regression\n")
		log.Warnf("#ohm: length of xs: %d, length of ys: %d\n", len(xs), len(ys))
		return 0
	}
	// Count time the regression is performed
	start := time.Now()
	log.Noticef("#ohm: Performing linear regression on %d samples\n", len(xs))

	// Convert uint64 to float64
	ysFloat := make([]float64, len(ys))
	for i, y := range ys {
		ysFloat[i] = float64(y)
	}
	// LinearRegression returns (alpha, beta)
	alpha, beta := stat.LinearRegression(xs, ysFloat, nil, false)
	// Calculate the time taken for regression
	elapsed := time.Since(start)
	log.Noticef("#ohm: Linear regression: alpha %.2f, beta %.2f, analysis time: %s\n", alpha, beta, elapsed)
	return beta
}

func updateInternalMemoryMonitorConfig(ctx *watcherContext) {
	gcp := agentlog.GetGlobalConfig(log, ctx.subGlobalConfig)
	if gcp == nil {
		return
	}

	// Update the internal memory monitor parameters
	analysisPeriod := time.Duration(gcp.GlobalValueInt(types.InternalMemoryMonitorAnalysisPeriodMinutes)) * time.Minute
	probingInterval := time.Duration(gcp.GlobalValueInt(types.InternalMemoryMonitorProbingIntervalSeconds)) * time.Second
	slopeThreshold := float64(gcp.GlobalValueInt(types.InternalMemoryMonitorSlopeThreshold))
	smoothingPeriod := time.Duration(gcp.GlobalValueInt(types.InternalMemoryMonitorSmoothingPeriodSeconds)) * time.Second
	smoothingProbeCount := int(smoothingPeriod / probingInterval)
	isActive := gcp.GlobalValueBool(types.InternalMemoryMonitorEnabled)
	ctx.IMMParams.Set(analysisPeriod, probingInterval, slopeThreshold, smoothingProbeCount, isActive)
}

// This function is just a placeholder to simulate a memory leak.
// The function leaks memory by allocating a small buffer in a loop.
func FunctionThatLeaksMemory() {
	// Create a buffer to accumulate memory
	buffer := make([]byte, 0)
	// Seed the random number generator
	for {
		chunk := make([]byte, 1024*1024/64) // ~0.01 MB that creates leak of 135 MB per day
		// Put random data in the chunk
		for i := range chunk {
			chunk[i] = byte(rand.Intn(256))
		}
		buffer = append(buffer, chunk...)
		// Print the size of the buffer
		log.Noticef("#ohm: Buffer size: %d MB\n", len(buffer)/1024/1024)
		time.Sleep(10 * time.Second)
	}
}
