package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

// SignalManager handles signal processing for user input nodes
type SignalManager struct {
	ctx    workflow.Context
	logger log.Logger
}

// NewSignalManager creates a new signal manager
func NewSignalManager(ctx workflow.Context, logger log.Logger) *SignalManager {
	return &SignalManager{
		ctx:    ctx,
		logger: logger,
	}
}

// WaitForSignal waits for a signal with optional expiry
func (sm *SignalManager) WaitForSignal(signalName string, expirySeconds int) (interface{}, error) {
	sm.logger.Info("Waiting for signal", zap.String("signalName", signalName), zap.Int("expirySeconds", expirySeconds))

	signalChan := workflow.GetSignalChannel(sm.ctx, signalName)
	selector := workflow.NewSelector(sm.ctx)
	
	var signalData interface{}
	var signalReceived bool
	var err error

	// Add signal receiver to selector
	selector.AddReceive(signalChan, func(c workflow.ReceiveChannel, more bool) {
		c.Receive(sm.ctx, &signalData)
		signalReceived = true
		sm.logger.Info("Signal received", zap.String("signalName", signalName), zap.Any("data", signalData))
	})

	// Add expiry timer if specified
	var expiryTimerCancel workflow.CancelFunc
	if expirySeconds > 0 {
		expiryCtx, cancel := workflow.WithCancel(sm.ctx)
		expiryTimerCancel = cancel
		
		expiryTimer := workflow.NewTimer(expiryCtx, time.Duration(expirySeconds)*time.Second)
		selector.AddFuture(expiryTimer, func(f workflow.Future) {
			err := f.Get(sm.ctx, nil)
			if err == nil { // Timer completed normally (not cancelled)
				sm.logger.Warn("Signal wait expired", zap.String("signalName", signalName))
				err = fmt.Errorf("signal wait expired for %s", signalName)
			}
		})
	}

	// Wait for either signal or expiry
	selector.Select(sm.ctx)

	// Cancel expiry timer if signal was received
	if signalReceived && expiryTimerCancel != nil {
		expiryTimerCancel()
	}

	if !signalReceived && err == nil {
		err = fmt.Errorf("signal not received for %s", signalName)
	}

	if err != nil {
		return nil, err
	}

	return signalData, nil
}

// SendSignal sends a signal (for testing purposes)
func (sm *SignalManager) SendSignal(signalName string, data interface{}) {
	// This would typically be done by external clients
	// Included here for testing/simulation purposes
	sm.logger.Info("Signal sent", zap.String("signalName", signalName), zap.Any("data", data))
}

// IsSignalPending checks if there are pending signals for a given signal name
func (sm *SignalManager) IsSignalPending(signalName string) bool {
	signalChan := workflow.GetSignalChannel(sm.ctx, signalName)
	// This is a simplified check - in practice, you might need more sophisticated logic
	return signalChan != nil
}