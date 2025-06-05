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
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

var (
	heapReachedThreshold       int
	rssReachedThreshold        int
	lastEntireSetAnalyzedIndex int
)

const (
	// bytesPerLineInFile is an approximation of the number of bytes per stat in the file.
	// The line usually looks like this: 2025-04-22T13:13:15Z,42450944,120524800,0.000
	bytesPerLineInFile = 46
	maxStatsFileSize   = 1024 * 1024 // 1MB
)

// InternalMemoryMonitorParams is a struct that holds the parameters for the memory monitor.
type InternalMemoryMonitorParams struct {
	mutex               sync.Mutex
	analysisWindow      time.Duration
	smoothingProbeCount int
	probingInterval     time.Duration
	slopeThreshold      float64
	isActive            bool
	// Context to make the monitoring goroutine cancellable
	context context.Context
	stop    context.CancelFunc
}

func (immp *InternalMemoryMonitorParams) Set(analysisWindow, probingInterval time.Duration, slopeThreshold float64, smoothingProbeCount int, isActive bool) {
	immp.mutex.Lock()
	immp.analysisWindow = analysisWindow
	immp.smoothingProbeCount = smoothingProbeCount
	immp.probingInterval = probingInterval
	immp.slopeThreshold = slopeThreshold
	immp.isActive = isActive
	immp.mutex.Unlock()
}

func (immp *InternalMemoryMonitorParams) Get() (time.Duration, time.Duration, float64, int, bool) {
	immp.mutex.Lock()
	defer immp.mutex.Unlock()
	return immp.analysisWindow, immp.probingInterval, immp.slopeThreshold, immp.smoothingProbeCount, immp.isActive
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

// TODO Should we use a faster average instead of median?
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

type memType string

const (
	memGoHeapInuse memType = "heap"
	memOsRss       memType = "rss"
)

type leakDesc struct {
	lType       memType
	lSlope      float64
	windowSize  int
	isEntireSet bool
}

type memUsageProbe struct {
	time           time.Time
	usages         map[memType]uint64
	leaks          []leakDesc
	leakScore      float64
	leakScoreDebug float64
}

func appendMemoryUsage(stats []memUsageProbe, newStat memUsageProbe) []memUsageProbe {
	stats = append(stats, newStat)
	if size := (len(stats) + 1) * bytesPerLineInFile; size > maxStatsFileSize {
		probesToRemove := size/bytesPerLineInFile - maxStatsFileSize/bytesPerLineInFile
		stats = stats[probesToRemove:]
		log.Noticef("trimming memory probes from %d to %d lines (%d removed)\n", len(stats)+probesToRemove, len(stats), probesToRemove)
		// Update the last analyzed index if we removed probes
		lastEntireSetAnalyzedIndex -= probesToRemove
	}
	return stats
}

const (
	weightHeap = 1.0
	weightRSS  = 0.3
)

func calculateStreakScore(window []memUsageProbe, leakType memType, isEntireSet bool, streakFactor float64, step, maxLeakWindowSize int) float64 {
	type streakInfo struct {
		length             int
		noLeakAfter        int
		lastLeakWindowSize int
	}
	var streaks []streakInfo
	curStreak := 0
	noLeak := 0
	inStreak := false
	lastLeakWindowSize := 0

	// Find the first probe that contains the leak type
	var i int
	found := false
	for i = 0; i < len(window); i++ {
		for _, leak := range window[i].leaks {
			if leak.lType == leakType && leak.isEntireSet == isEntireSet {
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	// Iterate over the window, looking for streaks of leaks
	for ; i < len(window); i += step {
		isLeak := false
		leakWindowSize := 0
		for _, leak := range window[i].leaks {
			if leak.lType == leakType && leak.isEntireSet == isEntireSet {
				isLeak = true
				leakWindowSize = leak.windowSize
				break
			}
		}
		if isLeak {
			if !inStreak && curStreak > 0 {
				newStreakInfo := streakInfo{
					length:             curStreak,
					noLeakAfter:        noLeak,
					lastLeakWindowSize: lastLeakWindowSize,
				}
				streaks = append(streaks, newStreakInfo)
				noLeak = 0
			}
			curStreak++
			inStreak = true
			lastLeakWindowSize = leakWindowSize
		} else {
			if inStreak && curStreak > 0 {
				inStreak = false
				noLeak = 1
			} else if !inStreak && curStreak > 0 {
				noLeak++
			}
		}
	}
	if curStreak > 0 {
		newStreakInfo := streakInfo{
			length:             curStreak,
			noLeakAfter:        noLeak,
			lastLeakWindowSize: lastLeakWindowSize,
		}
		streaks = append(streaks, newStreakInfo)
	}

	score := 0.0
	for _, s := range streaks {
		if s.lastLeakWindowSize > maxLeakWindowSize {
			log.Warnf("Leak window size %d > max leak window size %d\n", s.lastLeakWindowSize, maxLeakWindowSize)
			s.lastLeakWindowSize = maxLeakWindowSize
		}
		weight := float64(s.lastLeakWindowSize) / float64(maxLeakWindowSize)
		log.Tracef("Leak streak: %d, no leak after: %d, weight: %.2f\n", s.length, s.noLeakAfter, weight)
		score += float64(s.length*s.length) / float64(1+s.noLeakAfter) * weight
	}

	return score * streakFactor
}

// maxStreakScore returns the maximum streak score for a given window size and streak factor.
func maxStreakScore(maxStreakSize int, streakFactor float64) float64 {
	maxStreak := maxStreakSize * maxStreakSize
	return float64(maxStreak) * streakFactor
}

// normalize normalizes a value to a range of 0..1
// y= log(1 + k * x) / log (1 + k * maxValue);
// k - a constant that determines the steepness of the curve
// x - the value to be normalized
// maxValue - the maximum value in the data set
// The function returns a value between 0 and 1.
// Bigger k means more steepness.
func normalize(value, maxValue, k float64) float64 {
	if maxValue <= 0 {
		return 0
	}
	if value > maxValue {
		log.Warnf("value %f > maxValue %f\n", value, maxValue)
		value = maxValue
	}
	result := math.Log(1+k*value) / math.Log(1+k*maxValue)

	return result
}

// determineLeakScore returns 0…10 (10 is worst).
func determineLeakScore(stats []memUsageProbe, analysisWindowDuration, probingInterval time.Duration) (float64, float64) {
	n := len(stats)
	if n == 0 {
		return 0, 0
	}

	const (
		analysisWindowCount = 6
	)
	maxScoringWindowSize := int(analysisWindowDuration/probingInterval) * analysisWindowCount
	analysisWindowSize := int(analysisWindowDuration / probingInterval)
	entireSetStep := analysisWindowSize

	// focus on last N probes
	start := n - maxScoringWindowSize
	if start < 0 {
		start = 0
	}
	window := stats[start:]

	maxEntireSetStreakSize := analysisWindowCount

	maxHeapOneWindowStreakScore := maxStreakScore(maxScoringWindowSize, weightHeap)
	maxRSSOneWindowStreakScore := maxStreakScore(maxScoringWindowSize, weightRSS)

	streakEntireHeapFactor := maxHeapOneWindowStreakScore / (analysisWindowCount * analysisWindowCount)
	streakEntireRSSFactor := maxRSSOneWindowStreakScore / (analysisWindowCount * analysisWindowCount)

	maxEntireSetSize := maxStatsFileSize / bytesPerLineInFile
	maxHeapEntireSetStreakScore := maxStreakScore(maxEntireSetStreakSize, streakEntireHeapFactor)
	maxRSSEntireSetStreakScore := maxStreakScore(maxEntireSetStreakSize, streakEntireRSSFactor)

	maxScore := maxHeapOneWindowStreakScore + maxRSSOneWindowStreakScore + maxHeapEntireSetStreakScore + maxRSSEntireSetStreakScore

	// 2) calculate streak scores
	heapOneWindowStreakScore := calculateStreakScore(window, memGoHeapInuse, false, weightHeap, 1, analysisWindowSize)
	rssOneWindowStreakScore := calculateStreakScore(window, memOsRss, false, weightRSS, 1, analysisWindowSize)
	heapEntireSetStreakScore := calculateStreakScore(window, memGoHeapInuse, true, streakEntireHeapFactor, entireSetStep, maxEntireSetSize)
	rssEntireSetStreakScore := calculateStreakScore(window, memOsRss, true, streakEntireRSSFactor, entireSetStep, maxEntireSetSize)

	// 5) combine, clamp & round
	score := heapOneWindowStreakScore + rssOneWindowStreakScore + heapEntireSetStreakScore + rssEntireSetStreakScore

	// Normalize to 0..10
	result := normalize(score, maxScore, 0.0001) * 10
	resultDebug := normalize(score, maxScore, 0.00001) * 10

	log.Noticef("#ohm: result: %.3f, score: %.3f, maxScore: %.3f", result, score, maxScore)
	log.Noticef("#ohm: maxHeapOneWindowStreakScore: %.3f, maxRSSOneWindowStreakScore: %.3f, maxHeapEntireSetStreakScore: %.3f, maxRSSEntireSetStreakScore: %.3f", maxHeapOneWindowStreakScore, maxRSSOneWindowStreakScore, maxHeapEntireSetStreakScore, maxRSSEntireSetStreakScore)
	log.Noticef("#ohm: heapOneWindowStreakScore: %.3f, rssOneWindowStreakScore: %.3f, heapEntireSetStreakScore: %.3f, rssEntireSetStreakScore: %.3f\n", heapOneWindowStreakScore, rssOneWindowStreakScore, heapEntireSetStreakScore, rssEntireSetStreakScore)
	return result, resultDebug
}

func performMemoryLeakDetection(analysisWindow, probingInterval time.Duration, slopeThreshold float64, smoothingProbeCount int, stats []memUsageProbe, isActive bool) []memUsageProbe {
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
		usages: map[memType]uint64{
			memGoHeapInuse: heapInUse,
			memOsRss:       rss,
		},
	}
	stats = appendMemoryUsage(stats, memUsage)
	log.Noticef("Currently accumulated %d samples\n", len(stats))

	if !isActive {
		log.Noticef("Memory leak detection is not active, skipping analysis\n")
		return stats
	}

	// How many samples to analyze in one window
	probesInAnalysisWindow := int(analysisWindow / probingInterval)

	// If we don't have enough samples, wait for more
	if len(stats) < probesInAnalysisWindow {
		log.Noticef("Not enough samples to perform analysis, waiting for more samples\n")
		return stats
	}

	// Count the index of the first sample to be used
	currentIndex := len(stats) - 1
	firstProbeIndex := currentIndex - probesInAnalysisWindow + 1
	if firstProbeIndex < 0 {
		firstProbeIndex = 0
	}

	// Read all samples newer than the first sample time
	probes := stats[firstProbeIndex:]
	firstSampleTime := probes[0].time

	heapValues := make([]uint64, 0)
	rssValues := make([]uint64, 0)
	times := make([]float64, 0)
	for _, probe := range probes {
		times = append(times, probe.time.Sub(firstSampleTime).Seconds())
		heapValues = append(heapValues, probe.usages[memGoHeapInuse])
		rssValues = append(rssValues, probe.usages[memOsRss])
	}

	// Get a timestamp for the filename
	// Smooth the values via a median filter to reduce spikes
	log.Noticef("Smoothing values for one window, heap: %d", len(heapValues))
	smoothedHeapValues := medianFilter(heapValues, smoothingProbeCount)
	log.Noticef("Smoothing values for one window, RSS: %d", len(rssValues))
	smoothedRSSValues := medianFilter(rssValues, smoothingProbeCount)

	log.Noticef("Run linear regression on heap, one window, size: %d", len(smoothedHeapValues))
	heapSlope := linearRegressionSlope(times, smoothedHeapValues)
	log.Noticef("Run linear regression on RSS, one window, size: %d", len(smoothedRSSValues))
	RSSSlope := linearRegressionSlope(times, smoothedRSSValues)
	//If slope is positive and above a certain threshold, print a warning
	if heapSlope > slopeThreshold {
		newLeakDesc := leakDesc{
			lType:       memGoHeapInuse,
			lSlope:      heapSlope,
			windowSize:  len(heapValues),
			isEntireSet: false,
		}
		probes[len(probes)-1].leaks = append(probes[len(probes)-1].leaks, newLeakDesc)
		heapReachedThreshold++
		log.Warnf("Potential memory leak (heap) detected: slope %.2f > %.2f\n", heapSlope, slopeThreshold*2)
	} else {
		heapReachedThreshold = 0
	}
	if RSSSlope > slopeThreshold {
		newLeakDesc := leakDesc{
			lType:       memOsRss,
			lSlope:      RSSSlope,
			windowSize:  len(rssValues),
			isEntireSet: false,
		}
		probes[len(probes)-1].leaks = append(probes[len(probes)-1].leaks, newLeakDesc)
		rssReachedThreshold++
		log.Warnf("Potential memory leak (RSS) detected: slope %.2f > %.2f\n", RSSSlope, slopeThreshold*2)
	} else {
		rssReachedThreshold = 0
	}

	// Once per analysis window, perform analysis on entire data set
	if currentIndex == lastEntireSetAnalyzedIndex+probesInAnalysisWindow-1 {
		lastEntireSetAnalyzedIndex = currentIndex
		log.Noticef("Performing analysis on entire data set")
		// Read all samples
		probes = stats
		heapValues = make([]uint64, 0)
		rssValues = make([]uint64, 0)
		times = make([]float64, 0)
		for _, probe := range probes {
			times = append(times, probe.time.Sub(probes[0].time).Seconds())
			heapValues = append(heapValues, probe.usages[memGoHeapInuse])
			rssValues = append(rssValues, probe.usages[memOsRss])
		}
		// Perform smoothing with a bigger window (x5)
		log.Noticef("Smoothing values for entire data set, heap: %d", len(heapValues))
		smoothedHeapValues = medianFilter(heapValues, smoothingProbeCount*5)
		log.Noticef("Smoothing values for entire data set, RSS: %d", len(rssValues))
		smoothedRSSValues = medianFilter(rssValues, smoothingProbeCount*5)
		log.Noticef("Run linear regression on heap, entire data set, size: %d", len(smoothedHeapValues))
		heapSlope = linearRegressionSlope(times, smoothedHeapValues)
		log.Noticef("Run linear regression on RSS, entire data set, size: %d", len(smoothedRSSValues))
		RSSSlope = linearRegressionSlope(times, smoothedRSSValues)
		log.Noticef("Heap slope: %.2f, RSS slope: %.2f\n", heapSlope, RSSSlope)

		// If slope is positive and above a certain threshold, print a warning
		if heapSlope > slopeThreshold { // Use a lower threshold for the entire set
			log.Warnf("Potential memory leak (heap) detected on a entire data set: slope %.2f > %.2f\n", heapSlope, slopeThreshold)
			newLeakDesc := leakDesc{
				lType:       memGoHeapInuse,
				lSlope:      heapSlope,
				windowSize:  len(heapValues),
				isEntireSet: true,
			}
			probes[len(probes)-1].leaks = append(probes[len(probes)-1].leaks, newLeakDesc)
		}
		if RSSSlope > slopeThreshold { // Use a lower threshold for the entire seta
			log.Warnf("Potential memory leak (RSS) detected on a entire data set: slope %.2f > %.2f\n", RSSSlope, slopeThreshold)
			newLeakDesc := leakDesc{
				lType:       memOsRss,
				lSlope:      RSSSlope,
				windowSize:  len(rssValues),
				isEntireSet: true,
			}
			probes[len(probes)-1].leaks = append(probes[len(probes)-1].leaks, newLeakDesc)
		}
	}

	// Define signal zone for the last probe
	stats[len(stats)-1].leakScore, stats[len(stats)-1].leakScoreDebug = determineLeakScore(stats, analysisWindow, probingInterval)

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
	header := []string{"time", "heap", "rss", "score", "score_debug"}
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
		record = append(record, fmt.Sprintf("%d", probe.usages[memGoHeapInuse]))
		record = append(record, fmt.Sprintf("%d", probe.usages[memOsRss]))
		record = append(record, fmt.Sprintf("%.3f", probe.leakScore))
		record = append(record, fmt.Sprintf("%.3f", probe.leakScoreDebug))
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

// cleanupOldCsvFiles removes old CSV files from the output directory.
// Limit the comulative size of the files to 10MB.
// If the size exceeds 10MB, remove the oldest files until the size is below 10MB.
func cleanupOldCsvFiles() {
	dir := types.MemoryMonitorOutputDir
	files, err := os.ReadDir(dir)
	if err != nil {
		log.Warnf("failed to read directory: %v", err)
		return
	}

	var totalSize int64
	var fileInfos []os.FileInfo

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		// We are only interested in CSV files and their backups: *.csv and *.csv.<timestamp>.old
		if filepath.Ext(file.Name()) != ".csv" && filepath.Ext(file.Name()) != ".old" {
			continue
		}
		info, err := file.Info()
		if err != nil {
			log.Warnf("failed to get file info: %v", err)
			continue
		}
		fileInfos = append(fileInfos, info)
		totalSize += info.Size()
	}

	if totalSize <= 10*1024*1024 { // 10MB
		return
	}

	sort.Slice(fileInfos, func(i, j int) bool {
		return fileInfos[i].ModTime().Before(fileInfos[j].ModTime())
	})

	for _, fileInfo := range fileInfos {
		if totalSize <= 10*1024*1024 { // 10MB
			break
		}
		err := os.Remove(filepath.Join(dir, fileInfo.Name()))
		if err != nil {
			log.Warnf("failed to remove file: %v", err)
			continue
		}
		totalSize -= fileInfo.Size()
		log.Noticef("removed old CSV file: %s\n", fileInfo.Name())
	}
}

func backupOldCsvFile() {
	// Backup the old CSV file
	fileName := filepath.Join(types.MemoryMonitorOutputDir, "memory_usage.csv")
	backupFileName := fileName + "." + time.Now().Format("20060102150405") + ".old"
	if _, err := os.Stat(fileName); err == nil {
		err = os.Rename(fileName, backupFileName)
		if err != nil {
			log.Warnf("failed to rename file: %v", err)
		} else {
			log.Noticef("Backup of old CSV file created: %s\n", backupFileName)
		}
	}
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
	fileName := filepath.Join(types.MemoryMonitorOutputDir, "memory_usage.csv")
	// Backup the old CSV file
	backupOldCsvFile()
	// Cleanup old CSV files
	cleanupOldCsvFiles()
	// Create a slice to accumulate memory usage data
	stats := make([]memUsageProbe, 0)
	statsWrittenLastTime := time.Now()
	// Get the initial memory leak detection parameters
	for {
		analysisWindow, probingInterval, slopeThreshold, smoothingProbeCount, isActive := ctx.IMMParams.Get()
		// Check if we have to stop
		if ctx.IMMParams.isStoppable() && ctx.IMMParams.checkStopCondition() {
			log.Functionf("Stopping internal memory monitor")
			log.Warnf("#ohm: Internal memory monitor stopped")
			return
		}
		stats = performMemoryLeakDetection(analysisWindow, probingInterval, slopeThreshold, smoothingProbeCount, stats, isActive)
		// Write memory usage to CSV once per 1 minute
		if time.Since(statsWrittenLastTime) >= 1*time.Minute {
			log.Noticef("#ohm: Writing memory usage to CSV")
			// Write memory usage to CSV
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
	analysisWindow := time.Duration(gcp.GlobalValueInt(types.InternalMemoryMonitorAnalysisWindowMinutes)) * time.Minute
	probingInterval := time.Duration(gcp.GlobalValueInt(types.InternalMemoryMonitorProbingIntervalSeconds)) * time.Second
	slopeThreshold := float64(gcp.GlobalValueInt(types.InternalMemoryMonitorSlopeThreshold))
	smoothingWindow := time.Duration(gcp.GlobalValueInt(types.InternalMemoryMonitorSmoothingWindowSeconds)) * time.Second
	smoothingProbeCount := int(smoothingWindow / probingInterval)
	isActive := gcp.GlobalValueBool(types.InternalMemoryMonitorEnabled)
	ctx.IMMParams.Set(analysisWindow, probingInterval, slopeThreshold, smoothingProbeCount, isActive)
}

// This function is just a placeholder to simulate a memory leak.
// The function leaks memory by allocating a small buffer in a loop.
func FunctionThatLeaksMemory() {
	// Create a buffer to accumulate memory
	buffer := make([]byte, 0)
	// Seed the random number generator
	for {
		chunk := make([]byte, 2048) // 2KB per 10 seconds is ~118 Mb per week
		// Put random data in the chunk
		for i := range chunk {
			chunk[i] = byte(rand.Intn(256))
		}
		buffer = append(buffer, chunk...)
		// Print the size of the buffer
		log.Noticef("#ohm: Buffer size: %d bytes (%0.3f MB)\n", len(buffer), float64(len(buffer))/1024/1024)
		time.Sleep(10 * time.Second)
	}
}
