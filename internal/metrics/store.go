package metrics

import (
	"sort"
	"sync"
	"time"
)

// DataPoint represents a single point in time for metrics history.
type DataPoint struct {
	Timestamp   time.Time `json:"timestamp"`
	Requests    int64     `json:"requests"`
	Errors      int64     `json:"errors"`
	AvgLatency  float64   `json:"avg_latency_ms"`
	P50Latency  float64   `json:"p50_latency_ms"`
	P90Latency  float64   `json:"p90_latency_ms"`
	P99Latency  float64   `json:"p99_latency_ms"`
	Connections int64     `json:"connections"`
}

// LatencyPercentiles contains latency percentile values.
type LatencyPercentiles struct {
	P50 float64
	P90 float64
	P99 float64
	Avg float64
}

// bucket holds metrics for a single time bucket.
type bucket struct {
	timestamp  time.Time
	requests   int64
	errors     int64
	latencies  []time.Duration
	totalLatency time.Duration
}

// Store is a ring buffer for storing historical metrics data.
type Store struct {
	mu         sync.RWMutex
	buckets    []*bucket
	resolution time.Duration
	duration   time.Duration
	maxBuckets int
	current    int

	// For overall latency tracking
	allLatencies []time.Duration
	maxLatencies int
}

// NewStore creates a new metrics store.
func NewStore(duration, resolution time.Duration) *Store {
	maxBuckets := int(duration / resolution)
	if maxBuckets < 1 {
		maxBuckets = 1440 // Default to 24 hours at 1 minute resolution
	}

	s := &Store{
		buckets:      make([]*bucket, maxBuckets),
		resolution:   resolution,
		duration:     duration,
		maxBuckets:   maxBuckets,
		maxLatencies: 10000, // Keep last 10000 latencies for percentile calculation
	}

	// Initialize buckets
	now := time.Now().Truncate(resolution)
	for i := range s.buckets {
		s.buckets[i] = &bucket{
			timestamp: now.Add(-time.Duration(maxBuckets-i-1) * resolution),
			latencies: make([]time.Duration, 0, 100),
		}
	}
	s.current = maxBuckets - 1

	return s
}

// getCurrentBucket returns the current bucket, rotating if necessary.
func (s *Store) getCurrentBucket() *bucket {
	now := time.Now().Truncate(s.resolution)
	currentBucket := s.buckets[s.current]

	// Check if we need to rotate to a new bucket
	if now.After(currentBucket.timestamp) {
		// Calculate how many buckets to skip
		elapsed := now.Sub(currentBucket.timestamp)
		skip := int(elapsed / s.resolution)

		for i := 0; i < skip; i++ {
			s.current = (s.current + 1) % s.maxBuckets
			s.buckets[s.current] = &bucket{
				timestamp: currentBucket.timestamp.Add(time.Duration(i+1) * s.resolution),
				latencies: make([]time.Duration, 0, 100),
			}
		}
		currentBucket = s.buckets[s.current]
	}

	return currentBucket
}

// RecordRequest records a request with its latency.
func (s *Store) RecordRequest(latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := s.getCurrentBucket()
	bucket.requests++
	bucket.latencies = append(bucket.latencies, latency)
	bucket.totalLatency += latency

	// Track overall latencies for percentile calculation
	s.allLatencies = append(s.allLatencies, latency)
	if len(s.allLatencies) > s.maxLatencies {
		s.allLatencies = s.allLatencies[len(s.allLatencies)-s.maxLatencies:]
	}
}

// RecordError records an error.
func (s *Store) RecordError() {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := s.getCurrentBucket()
	bucket.errors++
}

// GetLatencyPercentiles returns latency percentiles from recent data.
func (s *Store) GetLatencyPercentiles() LatencyPercentiles {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.allLatencies) == 0 {
		return LatencyPercentiles{}
	}

	// Copy and sort latencies
	sorted := make([]time.Duration, len(s.allLatencies))
	copy(sorted, s.allLatencies)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	n := len(sorted)
	var total time.Duration
	for _, l := range sorted {
		total += l
	}

	return LatencyPercentiles{
		P50: float64(sorted[n*50/100].Milliseconds()),
		P90: float64(sorted[n*90/100].Milliseconds()),
		P99: float64(sorted[n*99/100].Milliseconds()),
		Avg: float64(total.Milliseconds()) / float64(n),
	}
}

// GetHistory returns historical data points for the specified duration.
func (s *Store) GetHistory(duration, resolution time.Duration) []DataPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	startTime := now.Add(-duration)

	// Collect relevant buckets
	var points []DataPoint

	for _, b := range s.buckets {
		if b == nil || b.timestamp.Before(startTime) {
			continue
		}

		point := DataPoint{
			Timestamp:  b.timestamp,
			Requests:   b.requests,
			Errors:     b.errors,
		}

		if len(b.latencies) > 0 {
			point.AvgLatency = float64(b.totalLatency.Milliseconds()) / float64(len(b.latencies))

			// Calculate percentiles for this bucket
			sorted := make([]time.Duration, len(b.latencies))
			copy(sorted, b.latencies)
			sort.Slice(sorted, func(i, j int) bool {
				return sorted[i] < sorted[j]
			})

			n := len(sorted)
			if n > 0 {
				point.P50Latency = float64(sorted[n*50/100].Milliseconds())
				point.P90Latency = float64(sorted[n*90/100].Milliseconds())
				point.P99Latency = float64(sorted[n*99/100].Milliseconds())
			}
		}

		points = append(points, point)
	}

	// Sort by timestamp
	sort.Slice(points, func(i, j int) bool {
		return points[i].Timestamp.Before(points[j].Timestamp)
	})

	// Aggregate if resolution is coarser than storage resolution
	if resolution > s.resolution {
		points = aggregatePoints(points, resolution)
	}

	return points
}

// aggregatePoints combines data points into larger time buckets.
func aggregatePoints(points []DataPoint, resolution time.Duration) []DataPoint {
	if len(points) == 0 {
		return points
	}

	var result []DataPoint
	var current *DataPoint
	var currentEnd time.Time
	var latencySum float64
	var latencyCount int

	for _, p := range points {
		bucketStart := p.Timestamp.Truncate(resolution)

		if current == nil || bucketStart.After(currentEnd) {
			if current != nil && latencyCount > 0 {
				current.AvgLatency = latencySum / float64(latencyCount)
				result = append(result, *current)
			}

			current = &DataPoint{
				Timestamp: bucketStart,
			}
			currentEnd = bucketStart.Add(resolution)
			latencySum = 0
			latencyCount = 0
		}

		current.Requests += p.Requests
		current.Errors += p.Errors
		if p.AvgLatency > 0 {
			latencySum += p.AvgLatency * float64(p.Requests)
			latencyCount += int(p.Requests)
		}

		// Use max for percentiles when aggregating
		if p.P50Latency > current.P50Latency {
			current.P50Latency = p.P50Latency
		}
		if p.P90Latency > current.P90Latency {
			current.P90Latency = p.P90Latency
		}
		if p.P99Latency > current.P99Latency {
			current.P99Latency = p.P99Latency
		}
	}

	// Don't forget the last bucket
	if current != nil && latencyCount > 0 {
		current.AvgLatency = latencySum / float64(latencyCount)
		result = append(result, *current)
	}

	return result
}

// Reset clears all stored metrics.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Truncate(s.resolution)
	for i := range s.buckets {
		s.buckets[i] = &bucket{
			timestamp: now.Add(-time.Duration(s.maxBuckets-i-1) * s.resolution),
			latencies: make([]time.Duration, 0, 100),
		}
	}
	s.current = s.maxBuckets - 1
	s.allLatencies = nil
}
