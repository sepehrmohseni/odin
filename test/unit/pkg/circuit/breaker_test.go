package circuit

import (
	"context"
	"errors"
	"testing"
	"time"

	"odin/pkg/circuit"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreaker_Execute(t *testing.T) {
	config := circuit.Config{
		MaxRequests:  5,
		Interval:     time.Second,
		Timeout:      time.Second,
		FailureRatio: 0.5,
		MinRequests:  3,
	}

	cb := circuit.NewCircuitBreaker("test", config, logrus.New())

	// Test successful execution
	result, err := cb.Execute(func() (interface{}, error) {
		return "success", nil
	})

	assert.NoError(t, err)
	assert.Equal(t, "success", result)
	assert.Equal(t, circuit.StateClosed, cb.State())
}

func TestCircuitBreaker_StateTransitions(t *testing.T) {
	config := circuit.Config{
		MaxRequests:  5,
		Interval:     100 * time.Millisecond,
		Timeout:      100 * time.Millisecond,
		FailureRatio: 0.6,
		MinRequests:  3,
	}

	cb := circuit.NewCircuitBreaker("test", config, logrus.New())

	// Trigger failures to open the circuit
	for i := 0; i < 5; i++ {
		_, err := cb.Execute(func() (interface{}, error) {
			return nil, errors.New("test error")
		})
		assert.Error(t, err)
	}

	// Verify circuit is open
	assert.Equal(t, circuit.StateOpen, cb.State())

	// Wait for half-open
	time.Sleep(150 * time.Millisecond)

	// Next call should put it in half-open state
	_, err := cb.Execute(func() (interface{}, error) {
		return "success", nil
	})
	assert.NoError(t, err)

	// Verify circuit is closed again
	assert.Equal(t, circuit.StateClosed, cb.State())
}

func TestCircuitBreaker_ExecuteWithContext(t *testing.T) {
	config := circuit.Config{
		MaxRequests:  5,
		Interval:     time.Second,
		Timeout:      time.Second,
		FailureRatio: 0.5,
		MinRequests:  3,
	}

	cb := circuit.NewCircuitBreaker("test", config, logrus.New())
	ctx := context.Background()

	result, err := cb.ExecuteWithContext(ctx, func(ctx context.Context) (interface{}, error) {
		return "context success", nil
	})

	assert.NoError(t, err)
	assert.Equal(t, "context success", result)
}

func TestCircuitBreakerManager(t *testing.T) {
	manager := circuit.NewManager(logrus.New())

	config := circuit.Config{
		MaxRequests:  5,
		Interval:     time.Second,
		Timeout:      time.Second,
		FailureRatio: 0.5,
		MinRequests:  3,
	}

	// Get breaker should create new one
	cb1 := manager.GetBreaker("service1", config)
	assert.NotNil(t, cb1)

	// Getting same breaker should return existing one
	cb2 := manager.GetBreaker("service1", config)
	assert.Same(t, cb1, cb2)

	// Get status should return info for all breakers
	status := manager.GetBreakerStatus()
	assert.Contains(t, status, "service1")
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cb := circuit.NewCircuitBreaker("test", circuit.Config{
		MaxRequests:  1,
		Timeout:      50 * time.Millisecond,
		FailureRatio: 0.5,
		MinRequests:  3,
	}, logrus.New())

	fail := func() (interface{}, error) { return nil, errors.New("boom") }
	for i := 0; i < 3; i++ {
		_, _ = cb.Execute(fail)
	}
	require.Equal(t, circuit.StateOpen, cb.State())

	time.Sleep(70 * time.Millisecond)
	require.Equal(t, circuit.StateHalfOpen, cb.State())

	// One failed trial is enough to open again, even though it is below minRequests.
	_, _ = cb.Execute(fail)
	assert.Equal(t, circuit.StateOpen, cb.State())
}
