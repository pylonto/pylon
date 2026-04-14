package daemon

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAcquireRelease(t *testing.T) {
	l := NewAgentLimiter(2)

	assert.True(t, l.Acquire())
	assert.True(t, l.Acquire())
	assert.False(t, l.Acquire(), "should be at capacity")

	l.Release()
	assert.True(t, l.Acquire(), "should succeed after release")
}

func TestActive(t *testing.T) {
	l := NewAgentLimiter(5)

	assert.Equal(t, 0, l.Active())
	l.Acquire()
	l.Acquire()
	assert.Equal(t, 2, l.Active())
	l.Release()
	assert.Equal(t, 1, l.Active())
}

func TestZeroMax(t *testing.T) {
	l := NewAgentLimiter(0)

	// max=0 means unlimited
	for range 100 {
		assert.True(t, l.Acquire())
	}
	assert.Equal(t, 100, l.Active())
}

func TestReleaseUnderflow(t *testing.T) {
	l := NewAgentLimiter(2)

	// Releasing when count is 0 should not go negative
	l.Release()
	assert.Equal(t, 0, l.Active())
}

func TestConcurrentAcquireRelease(t *testing.T) {
	l := NewAgentLimiter(10)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Acquire() {
				l.Release()
			}
		}()
	}
	wg.Wait()

	// All acquired slots should have been released
	assert.Equal(t, 0, l.Active())
}
