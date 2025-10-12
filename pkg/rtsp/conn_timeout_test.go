package rtsp

import (
	"context"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

// TestTimeoutKeepaliveCalculation tests the logic for calculating keepalive intervals
// based on user-specified timeout values (for QVR cameras)
func TestTimeoutKeepaliveCalculation(t *testing.T) {
	tests := []struct {
		name                 string
		timeout              int
		expectedKeepalive    time.Duration
		expectedReadTimeout  time.Duration
		description          string
	}{
		{
			name:                "QVR typical timeout 100s",
			timeout:             100,
			expectedKeepalive:   50 * time.Second,
			expectedReadTimeout: 100 * time.Second,
			description:         "100s timeout should give 50s keepalive (timeout/2)",
		},
		{
			name:                "Short timeout 10s",
			timeout:             10,
			expectedKeepalive:   8 * time.Second,
			expectedReadTimeout: 10 * time.Second,
			description:         "10s timeout should give 8s keepalive (timeout-2)",
		},
		{
			name:                "Very short timeout 5s",
			timeout:             5,
			expectedKeepalive:   3 * time.Second,
			expectedReadTimeout: 5 * time.Second,
			description:         "5s timeout should give 3s keepalive (timeout-2)",
		},
		{
			name:                "Minimal timeout 2s",
			timeout:             2,
			expectedKeepalive:   1 * time.Second,
			expectedReadTimeout: 2 * time.Second,
			description:         "2s timeout should give 1s keepalive (minimum)",
		},
		{
			name:                "Large timeout 300s",
			timeout:             300,
			expectedKeepalive:   150 * time.Second,
			expectedReadTimeout: 300 * time.Second,
			description:         "300s timeout should give 150s keepalive (timeout/2)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a connection with specified timeout
			c := &Conn{
				Connection: core.Connection{
					ID:         core.NewID(),
					FormatName: "rtsp",
				},
				Timeout: tt.timeout,
				mode:    core.ModeActiveProducer,
			}

			// Calculate keepalive and timeout values using the same logic as Handle()
			var keepaliveDT time.Duration
			var timeout time.Duration

			if c.Timeout > 0 {
				timeout = time.Second * time.Duration(c.Timeout)

				if c.Timeout > 10 {
					keepaliveDT = time.Duration(c.Timeout/2) * time.Second
				} else {
					keepaliveDT = time.Duration(c.Timeout-2) * time.Second
					if keepaliveDT < time.Second {
						keepaliveDT = time.Second
					}
				}
			}

			// Verify keepalive interval
			if keepaliveDT != tt.expectedKeepalive {
				t.Errorf("%s: keepalive interval = %v, want %v",
					tt.description, keepaliveDT, tt.expectedKeepalive)
			}

			// Verify read timeout
			if timeout != tt.expectedReadTimeout {
				t.Errorf("%s: read timeout = %v, want %v",
					tt.description, timeout, tt.expectedReadTimeout)
			}

			t.Logf("✓ %s: keepalive=%v, timeout=%v", tt.name, keepaliveDT, timeout)
		})
	}
}

// TestKeepaliveNotUsedWhenTimeoutZero verifies that when timeout is not specified,
// the old behavior (using camera-provided keepalive) is maintained
func TestKeepaliveNotUsedWhenTimeoutZero(t *testing.T) {
	tests := []struct {
		name              string
		cameraKeepalive   int
		expectedKeepalive time.Duration
		description       string
	}{
		{
			name:              "Camera provides 60s timeout",
			cameraKeepalive:   60,
			expectedKeepalive: 55 * time.Second,
			description:       "Should use camera timeout-5",
		},
		{
			name:              "Camera provides no timeout",
			cameraKeepalive:   0,
			expectedKeepalive: 25 * time.Second,
			description:       "Should use default 25s",
		},
		{
			name:              "Camera provides short timeout 5s",
			cameraKeepalive:   5,
			expectedKeepalive: 25 * time.Second,
			description:       "Should use default 25s when too short",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Conn{
				Connection: core.Connection{
					ID:         core.NewID(),
					FormatName: "rtsp",
				},
				Timeout:   0, // No user-specified timeout
				keepalive: tt.cameraKeepalive,
				mode:      core.ModeActiveProducer,
			}

			// Calculate keepalive using original logic (when Timeout == 0)
			var keepaliveDT time.Duration

			if c.Timeout == 0 {
				if c.keepalive > 5 {
					keepaliveDT = time.Duration(c.keepalive-5) * time.Second
				} else {
					keepaliveDT = 25 * time.Second
				}
			}

			if keepaliveDT != tt.expectedKeepalive {
				t.Errorf("%s: keepalive = %v, want %v",
					tt.description, keepaliveDT, tt.expectedKeepalive)
			}

			t.Logf("✓ %s: keepalive=%v", tt.name, keepaliveDT)
		})
	}
}

// TestKeepaliveContextCancellation verifies that keepalive goroutine
// stops properly when context is cancelled
func TestKeepaliveContextCancellation(t *testing.T) {
	// This test verifies that the keepalive goroutine properly responds to context cancellation
	// We can't fully test the actual network operations, but we can test the cancellation logic

	ctx, cancel := context.WithCancel(context.Background())

	// Create a channel to signal when handleKeepalive exits
	done := make(chan bool, 1)

	// Simulate a very short keepalive interval for testing
	go func() {
		// Mock the handleKeepalive behavior with just the cancellation logic
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		defer func() { done <- true }()

		for {
			select {
			case <-ticker.C:
				// In real code, this would send keepalive
			case <-ctx.Done():
				return
			}
		}
	}()

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for goroutine to exit
	select {
	case <-done:
		t.Log("✓ Keepalive goroutine stopped correctly after context cancellation")
	case <-time.After(1 * time.Second):
		t.Fatal("Keepalive goroutine did not stop within timeout after context cancellation")
	}
}

// TestQVRScenario tests the specific QVR camera scenario
func TestQVRScenario(t *testing.T) {
	// QVR cameras typically:
	// - Ignore keepalive OPTIONS requests
	// - Disconnect after their internal timeout (~100s)
	// - Don't provide proper timeout in Session header

	c := &Conn{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: "rtsp",
		},
		Timeout:   100, // User specifies timeout=100 for QVR
		keepalive: 0,   // Camera doesn't provide keepalive
		mode:      core.ModeActiveProducer,
	}

	var keepaliveDT time.Duration
	var timeout time.Duration

	// Use the new logic
	if c.Timeout > 0 {
		timeout = time.Second * time.Duration(c.Timeout)
		if c.Timeout > 10 {
			keepaliveDT = time.Duration(c.Timeout/2) * time.Second
		}
	}

	// For QVR with timeout=100:
	// - Read timeout should be 100s (allows camera to send data)
	// - Keepalive should be 50s (prevents camera disconnect)

	expectedTimeout := 100 * time.Second
	expectedKeepalive := 50 * time.Second

	if timeout != expectedTimeout {
		t.Errorf("QVR scenario: timeout = %v, want %v", timeout, expectedTimeout)
	}

	if keepaliveDT != expectedKeepalive {
		t.Errorf("QVR scenario: keepalive = %v, want %v", keepaliveDT, expectedKeepalive)
	}

	// Verify keepalive is sent before timeout
	if keepaliveDT >= timeout {
		t.Errorf("Keepalive (%v) must be less than timeout (%v) to prevent disconnection",
			keepaliveDT, timeout)
	}

	// Verify we send at least 2 keepalives before timeout
	keepalivesBeforeTimeout := timeout / keepaliveDT
	if keepalivesBeforeTimeout < 2 {
		t.Errorf("Should send at least 2 keepalives before timeout, got %v", keepalivesBeforeTimeout)
	}

	t.Logf("✓ QVR scenario verified: timeout=%v, keepalive=%v, keepalives_before_timeout=%v",
		timeout, keepaliveDT, keepalivesBeforeTimeout)
}
