package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/oarkflow/ref/platform/spi"
)

type storedIdempotencyResponse struct {
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}

type storedIdempotencyRecord struct {
	State       string                    `json:"state"`
	Owner       string                    `json:"owner"`
	Fingerprint string                    `json:"fingerprint"`
	Response    storedIdempotencyResponse `json:"response"`
	CreatedAt   time.Time                 `json:"created_at"`
}

type cacheAtomicMutator interface {
	Mutate(string, func([]byte, bool) ([]byte, time.Duration, bool, error)) error
}

type atomicCacheIdempotencyStore struct {
	handle  cacheHandle
	mutator cacheAtomicMutator
}

func newAtomicCacheIdempotencyStore(handle cacheHandle, mutator cacheAtomicMutator) spi.IdempotencyStore {
	return &atomicCacheIdempotencyStore{handle: handle, mutator: mutator}
}

func (s *atomicCacheIdempotencyStore) Claim(ctx context.Context, key, fingerprint, owner string, ttl time.Duration) (spi.IdempotencyClaim, error) {
	if err := ctx.Err(); err != nil {
		return spi.IdempotencyClaim{}, err
	}
	claim := spi.IdempotencyClaim{}
	err := s.mutator.Mutate(key, func(current []byte, exists bool) ([]byte, time.Duration, bool, error) {
		if !exists {
			encoded, err := encodeIdempotencyRecord(storedIdempotencyRecord{
				State: "claimed", Owner: owner, Fingerprint: fingerprint, CreatedAt: time.Now().UTC(),
			})
			if err != nil {
				return nil, 0, false, err
			}
			claim.State = spi.IdempotencyAcquired
			return encoded, ttl, true, nil
		}
		record, err := decodeIdempotencyRecord(current)
		if err != nil {
			claim.State = spi.IdempotencyConflict
			return current, ttl, false, nil
		}
		if record.Fingerprint != fingerprint {
			claim.State = spi.IdempotencyConflict
			return current, ttl, false, nil
		}
		if record.State == "released" {
			encoded, err := encodeIdempotencyRecord(storedIdempotencyRecord{
				State: "claimed", Owner: owner, Fingerprint: fingerprint, CreatedAt: time.Now().UTC(),
			})
			if err != nil {
				return current, ttl, false, err
			}
			claim.State = spi.IdempotencyAcquired
			return encoded, ttl, true, nil
		}
		if record.State == "completed" {
			claim.State = spi.IdempotencyReplay
			claim.Response = spi.IdempotencyResponse{Status: record.Response.Status, Body: append([]byte(nil), record.Response.Body...)}
			return current, ttl, false, nil
		}
		claim.State = spi.IdempotencyInProgress
		return current, ttl, false, nil
	})
	return claim, err
}

func (s *atomicCacheIdempotencyStore) Complete(ctx context.Context, key, fingerprint, owner string, response spi.IdempotencyResponse, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.mutator.Mutate(key, func(current []byte, exists bool) ([]byte, time.Duration, bool, error) {
		if !exists {
			return current, ttl, false, spi.ErrConflict
		}
		record, err := decodeIdempotencyRecord(current)
		if err != nil {
			return current, ttl, false, err
		}
		if record.State == "completed" && record.Fingerprint == fingerprint {
			return current, ttl, false, nil
		}
		if record.State != "claimed" || record.Owner != owner || record.Fingerprint != fingerprint {
			return current, ttl, false, spi.ErrConflict
		}
		record.State = "completed"
		record.Response = storedIdempotencyResponse{Status: response.Status, Body: append([]byte(nil), response.Body...)}
		encoded, err := encodeIdempotencyRecord(record)
		if err != nil {
			return current, ttl, false, err
		}
		return encoded, ttl, true, nil
	})
}

func (s *atomicCacheIdempotencyStore) Release(ctx context.Context, key, owner string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.mutator.Mutate(key, func(current []byte, exists bool) ([]byte, time.Duration, bool, error) {
		if !exists {
			return current, 0, false, nil
		}
		record, err := decodeIdempotencyRecord(current)
		if err != nil {
			return current, 0, false, err
		}
		if record.Owner == owner && record.State == "claimed" {
			record.State = "released"
			record.Owner = ""
			encoded, err := encodeIdempotencyRecord(record)
			if err != nil {
				return current, time.Minute, false, err
			}
			return encoded, time.Minute, true, nil
		}
		return current, 0, false, nil
	})
}

func encodeIdempotencyRecord(record storedIdempotencyRecord) ([]byte, error) {
	return json.Marshal(record)
}

func decodeIdempotencyRecord(value []byte) (storedIdempotencyRecord, error) {
	var record storedIdempotencyRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return storedIdempotencyRecord{}, err
	}
	if record.State != "claimed" && record.State != "completed" && record.State != "released" {
		return storedIdempotencyRecord{}, fmt.Errorf("platform: invalid idempotency state %q", record.State)
	}
	return record, nil
}

func (c *sqlCache) Claim(ctx context.Context, key, fingerprint, owner string, ttl time.Duration) (spi.IdempotencyClaim, error) {
	record := storedIdempotencyRecord{State: "claimed", Owner: owner, Fingerprint: fingerprint, CreatedAt: time.Now().UTC()}
	encoded, err := encodeIdempotencyRecord(record)
	if err != nil {
		return spi.IdempotencyClaim{}, err
	}
	var expires any
	if ttl > 0 {
		expires = time.Now().UTC().Add(ttl)
	}
	insert := c.query(fmt.Sprintf("INSERT INTO %s (cache_key, value, expires_at) VALUES ($1, $2, $3)", c.table))
	if _, err := c.db.ExecContext(ctx, insert, key, encoded, expires); err == nil {
		return spi.IdempotencyClaim{State: spi.IdempotencyAcquired}, nil
	} else {
		current, found, getErr := c.GetContext(ctx, key)
		if getErr != nil {
			return spi.IdempotencyClaim{}, getErr
		}
		if !found {
			return spi.IdempotencyClaim{}, err
		}
		stored, decodeErr := decodeIdempotencyRecord(current)
		if decodeErr != nil {
			return spi.IdempotencyClaim{State: spi.IdempotencyConflict}, nil
		}
		if stored.Fingerprint != fingerprint {
			return spi.IdempotencyClaim{State: spi.IdempotencyConflict}, nil
		}
		if stored.State == "completed" {
			return spi.IdempotencyClaim{State: spi.IdempotencyReplay, Response: spi.IdempotencyResponse{Status: stored.Response.Status, Body: append([]byte(nil), stored.Response.Body...)}}, nil
		}
		return spi.IdempotencyClaim{State: spi.IdempotencyInProgress}, nil
	}
}

func (c *sqlCache) Complete(ctx context.Context, key, fingerprint, owner string, response spi.IdempotencyResponse, ttl time.Duration) error {
	for attempt := 0; attempt < 8; attempt++ {
		current, found, err := c.GetContext(ctx, key)
		if err != nil {
			return err
		}
		if !found {
			return spi.ErrConflict
		}
		stored, err := decodeIdempotencyRecord(current)
		if err != nil {
			return err
		}
		if stored.State == "completed" && stored.Fingerprint == fingerprint {
			return nil
		}
		if stored.State != "claimed" || stored.Owner != owner || stored.Fingerprint != fingerprint {
			return spi.ErrConflict
		}
		stored.State = "completed"
		stored.Response = storedIdempotencyResponse{Status: response.Status, Body: append(json.RawMessage(nil), response.Body...)}
		encoded, err := encodeIdempotencyRecord(stored)
		if err != nil {
			return err
		}
		var expires any
		if ttl > 0 {
			expires = time.Now().UTC().Add(ttl)
		}
		statement := c.query(fmt.Sprintf("UPDATE %s SET value=$1, expires_at=$2 WHERE cache_key=$3 AND value=$4", c.table))
		result, err := c.db.ExecContext(ctx, statement, encoded, expires, key, current)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 1 {
			return nil
		}
	}
	return spi.ErrConflict
}

func (c *sqlCache) Release(ctx context.Context, key, owner string) error {
	for attempt := 0; attempt < 8; attempt++ {
		current, found, err := c.GetContext(ctx, key)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		stored, err := decodeIdempotencyRecord(current)
		if err != nil {
			return err
		}
		if stored.State != "claimed" || stored.Owner != owner {
			return nil
		}
		statement := c.query(fmt.Sprintf("DELETE FROM %s WHERE cache_key=$1 AND value=$2", c.table))
		result, err := c.db.ExecContext(ctx, statement, key, current)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 1 {
			return nil
		}
	}
	return spi.ErrConflict
}
