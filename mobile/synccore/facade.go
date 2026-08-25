// SPDX-License-Identifier: Apache-2.0
package mobilecore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

// Facade keeps the exported surface compatible with generated Go mobile
// bindings: strings, byte arrays, booleans, integers and error are the only
// boundary types. Native code owns all secret-store and lifecycle decisions.
type Facade struct {
	mu   sync.RWMutex
	core *Core

	operationMu       sync.Mutex
	operationSequence uint64
	currentOperation  uint64
	currentCancel     context.CancelFunc
	cancelRequested   bool
}

func OpenFacade(databasePath, snapshotPath, endpoint string, allowPrivateHTTP bool, credential []byte) (*Facade, error) {
	core, err := Open(context.Background(), Options{
		DatabasePath: databasePath, SnapshotPath: snapshotPath, Endpoint: endpoint,
		AllowPrivateHTTP: allowPrivateHTTP, Credential: credential,
	})
	if err != nil {
		return nil, errors.New(redactedErrorCode(err))
	}
	return &Facade{core: core}, nil
}

func withTimeout(timeoutMillis int64) (context.Context, context.CancelFunc, error) {
	if timeoutMillis < 1000 || timeoutMillis > int64((5*time.Minute)/time.Millisecond) {
		return nil, nil, errors.New("mobile operation timeout is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMillis)*time.Millisecond)
	return ctx, cancel, nil
}

func (facade *Facade) beginOperation(timeoutMillis int64) (context.Context, context.CancelFunc, uint64, error) {
	ctx, cancel, err := withTimeout(timeoutMillis)
	if err != nil {
		return nil, nil, 0, err
	}
	facade.operationMu.Lock()
	defer facade.operationMu.Unlock()
	if facade.currentCancel != nil {
		cancel()
		return nil, nil, 0, errors.New("local_state_error")
	}
	facade.operationSequence++
	if facade.operationSequence == 0 {
		facade.operationSequence++
	}
	token := facade.operationSequence
	facade.currentOperation = token
	facade.currentCancel = cancel
	if facade.cancelRequested {
		cancel()
	}
	return ctx, cancel, token, nil
}

func (facade *Facade) endOperation(token uint64, cancel context.CancelFunc) {
	cancel()
	facade.operationMu.Lock()
	defer facade.operationMu.Unlock()
	if facade.currentOperation == token {
		facade.currentOperation = 0
		facade.currentCancel = nil
	}
}

// CancelCurrentOperation is safe to call from an OS background-expiration
// callback while another goroutine is inside Sync or Status. Cancellation is
// sticky for this short-lived Facade so a stop arriving immediately before an
// operation begins cannot be lost; callers should Close the Facade afterward.
func (facade *Facade) CancelCurrentOperation() {
	if facade == nil {
		return
	}
	facade.operationMu.Lock()
	facade.cancelRequested = true
	cancel := facade.currentCancel
	facade.operationMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func encodeRedacted(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("encode redacted mobile result")
	}
	return string(encoded), nil
}

type facadeResult struct {
	OK        bool   `json:"ok"`
	ErrorCode string `json:"error_code,omitempty"`
	Result    any    `json:"result,omitempty"`
}

func redactedErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var api *syncclient.APIError
	if errors.As(err, &api) {
		switch api.Status {
		case 401, 403:
			return "authorization_required"
		case 409:
			return "remote_conflict"
		case 408, 429:
			return "remote_unavailable"
		default:
			if api.Status >= 500 && api.Status <= 599 {
				return "remote_unavailable"
			}
			return "remote_rejected"
		}
	}
	var rejection *syncclient.UploadRejectionError
	if errors.As(err, &rejection) {
		switch rejection.Code {
		case "sequence_conflict", "sequence_gap", "previous_hash_mismatch":
			return "remote_conflict"
		default:
			return "local_state_error"
		}
	}
	var network net.Error
	if errors.As(err, &network) {
		return "network_unavailable"
	}
	return "local_state_error"
}

func encodeFacadeResult(result any, err error) (string, error) {
	if err != nil {
		// Failure envelopes intentionally contain no partial result. Native
		// strict decoders can therefore classify the fixed redacted code without
		// accepting ambiguous counters from an incomplete operation.
		return encodeRedacted(facadeResult{OK: false, ErrorCode: redactedErrorCode(err)})
	}
	return encodeRedacted(facadeResult{OK: true, Result: result})
}

func (facade *Facade) Sync(timeoutMillis int64) (string, error) {
	if facade == nil {
		return "", errors.New("mobile sync facade is closed")
	}
	facade.mu.RLock()
	defer facade.mu.RUnlock()
	if facade.core == nil {
		return "", errors.New("mobile sync facade is closed")
	}
	ctx, cancel, operation, err := facade.beginOperation(timeoutMillis)
	if err != nil {
		return "", err
	}
	defer facade.endOperation(operation, cancel)
	report, err := facade.core.Sync(ctx)
	return encodeFacadeResult(report, err)
}

func (facade *Facade) Status(timeoutMillis int64) (string, error) {
	if facade == nil {
		return "", errors.New("mobile sync facade is closed")
	}
	facade.mu.RLock()
	defer facade.mu.RUnlock()
	if facade.core == nil {
		return "", errors.New("mobile sync facade is closed")
	}
	ctx, cancel, operation, err := facade.beginOperation(timeoutMillis)
	if err != nil {
		return "", err
	}
	defer facade.endOperation(operation, cancel)
	status, err := facade.core.Status(ctx)
	return encodeFacadeResult(status, err)
}

func (facade *Facade) RecordSelection(text, pinyin string, passwordField, privateMode, oneTimeInput, noPersonalizedLearning bool, timeoutMillis int64) (string, error) {
	if facade == nil {
		return "", errors.New("mobile sync facade is closed")
	}
	facade.mu.RLock()
	defer facade.mu.RUnlock()
	if facade.core == nil {
		return "", errors.New("mobile sync facade is closed")
	}
	ctx, cancel, operation, err := facade.beginOperation(timeoutMillis)
	if err != nil {
		return "", err
	}
	defer facade.endOperation(operation, cancel)
	result, err := facade.core.RecordSelection(ctx, text, pinyin, localstore.LearningContext{
		PasswordField: passwordField, PrivateMode: privateMode, OneTimeInput: oneTimeInput,
		NoPersonalizedLearning: noPersonalizedLearning,
	})
	return encodeFacadeResult(result, err)
}

func (facade *Facade) SaveExplicit(text, pinyin string, useCount int64, pinned bool, timeoutMillis int64) error {
	if facade == nil {
		return errors.New("mobile sync facade is closed")
	}
	facade.mu.RLock()
	defer facade.mu.RUnlock()
	if facade.core == nil {
		return errors.New("mobile sync facade is closed")
	}
	if useCount < 1 {
		return errors.New("local_state_error")
	}
	ctx, cancel, operation, err := facade.beginOperation(timeoutMillis)
	if err != nil {
		return err
	}
	defer facade.endOperation(operation, cancel)
	if err := facade.core.SaveExplicit(ctx, text, pinyin, uint64(useCount), pinned); err != nil {
		return errors.New(redactedErrorCode(err))
	}
	return nil
}

func (facade *Facade) Delete(text, pinyin string, timeoutMillis int64) error {
	if facade == nil {
		return errors.New("mobile sync facade is closed")
	}
	facade.mu.RLock()
	defer facade.mu.RUnlock()
	if facade.core == nil {
		return errors.New("mobile sync facade is closed")
	}
	ctx, cancel, operation, err := facade.beginOperation(timeoutMillis)
	if err != nil {
		return err
	}
	defer facade.endOperation(operation, cancel)
	if err := facade.core.Delete(ctx, text, pinyin); err != nil {
		return errors.New(redactedErrorCode(err))
	}
	return nil
}

func (facade *Facade) PublishSnapshot(timeoutMillis int64) (string, error) {
	if facade == nil {
		return "", errors.New("mobile sync facade is closed")
	}
	facade.mu.RLock()
	defer facade.mu.RUnlock()
	if facade.core == nil {
		return "", errors.New("mobile sync facade is closed")
	}
	ctx, cancel, operation, err := facade.beginOperation(timeoutMillis)
	if err != nil {
		return "", err
	}
	defer facade.endOperation(operation, cancel)
	report, err := facade.core.PublishSnapshot(ctx)
	return encodeFacadeResult(report, err)
}

func (facade *Facade) RollbackSnapshot() error {
	if facade == nil {
		return errors.New("mobile sync facade is closed")
	}
	facade.mu.RLock()
	defer facade.mu.RUnlock()
	if facade.core == nil {
		return errors.New("mobile sync facade is closed")
	}
	ctx, cancel, operation, err := facade.beginOperation(30_000)
	if err != nil {
		return err
	}
	defer facade.endOperation(operation, cancel)
	if err := ctx.Err(); err != nil {
		return errors.New(redactedErrorCode(err))
	}
	if err := facade.core.RollbackSnapshot(); err != nil {
		return errors.New(redactedErrorCode(err))
	}
	return nil
}

func (facade *Facade) Close() error {
	if facade == nil {
		return nil
	}
	facade.CancelCurrentOperation()
	facade.mu.Lock()
	defer facade.mu.Unlock()
	if facade.core == nil {
		return nil
	}
	err := facade.core.Close()
	facade.core = nil
	if err != nil {
		return errors.New(redactedErrorCode(err))
	}
	return nil
}
