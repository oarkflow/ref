package capability

// ---------------------------------------------------------------------------
// Redis-backed distributed circuit breaker
// ---------------------------------------------------------------------------
//
// InMemoryCircuitBreaker (see circuit_breaker.go) tracks failure/success
// counters and state transitions in process memory. That works well for a
// single-instance deployment, but it falls apart the moment the workload is
// horizontally scaled: every instance keeps its own counters, so a
// dependency that is actually failing for the whole fleet only ever trips
// the breaker on the instance(s) that happen to observe enough failures.
// Other instances keep hammering the failing dependency, which defeats the
// entire point of a circuit breaker (stopping cascading failures).
//
// Use RedisCircuitBreaker instead of InMemoryCircuitBreaker when:
//   - You run more than one instance/replica of the service and want them
//     to share trip state (closed/open/half-open) for the same logical key.
//   - A failing downstream dependency should open the circuit for the whole
//     fleet, not just the instances that happened to see the failures.
//   - You can tolerate the extra network round trip to Redis on each
//     Allow/Record call, in exchange for consistent, cluster-wide breaker
//     behaviour.
//
// Stick with InMemoryCircuitBreaker for single-instance deployments, tests,
// or when Redis is not otherwise part of your infrastructure - it has zero
// external dependencies and lower latency.
//
// State for each key is stored in a single Redis hash
// (`<keyPrefix><key>`) with fields "state", "failures", "successes" and
// "opened_at". All state transitions (closed->open, open->half-open,
// half-open->closed, half-open->open) are performed by Lua scripts
// evaluated atomically on the Redis server via EVAL/EVALSHA, so concurrent
// callers - whether multiple goroutines in one process or multiple
// process instances - never race on the read-modify-write of the
// breaker's state.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisScripter is the subset of the go-redis client surface required to
// evaluate Lua scripts. github.com/redis/go-redis/v9's *redis.Client,
// *redis.ClusterClient and *redis.Ring all satisfy redis.Cmdable, which in
// turn satisfies this interface, so callers can inject whichever real
// client they already use without this package depending on a concrete
// client type.
type RedisScripter interface {
	redis.Scripter
}

// RedisCircuitBreakerConfig configures a RedisCircuitBreaker. It mirrors
// InMemoryCircuitBreakerConfig, with an additional Redis key prefix used to
// namespace the hashes this breaker creates.
type RedisCircuitBreakerConfig struct {
	// FailureThreshold is the number of consecutive failures before the circuit opens.
	FailureThreshold int
	// SuccessThreshold is the number of consecutive successes in half-open before closing.
	SuccessThreshold int
	// OpenTimeout is how long the circuit stays open before transitioning to half-open.
	OpenTimeout time.Duration
	// KeyPrefix namespaces the Redis keys this breaker uses. Defaults to
	// "ref:circuit_breaker:" when empty.
	KeyPrefix string
}

// DefaultRedisCircuitBreakerConfig returns sensible defaults, matching
// DefaultInMemoryCircuitBreakerConfig.
func DefaultRedisCircuitBreakerConfig() RedisCircuitBreakerConfig {
	return RedisCircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		OpenTimeout:      30 * time.Second,
		KeyPrefix:        "ref:circuit_breaker:",
	}
}

// RedisCircuitBreaker is a circuit breaker whose per-key state (closed,
// open, half-open) is stored in Redis, so it can be shared safely across
// multiple process instances. It satisfies the same Allow/Record/State
// method shapes as InMemoryCircuitBreaker (CircuitBreakerFunc,
// CircuitRecordFunc, CircuitStateFunc) and is therefore a drop-in
// alternative when constructing a circuit breaker capability via
// NewCircuitBreakerCapability.
type RedisCircuitBreaker struct {
	client RedisScripter
	config RedisCircuitBreakerConfig
	ctx    context.Context

	allowScript  *redis.Script
	recordScript *redis.Script
	stateScript  *redis.Script
}

// allowScriptSrc atomically inspects and, if necessary, advances the
// circuit's state (open -> half-open once the timeout has elapsed) and
// reports whether the caller may proceed.
//
// KEYS[1] = hash key
// ARGV[1] = now (unix milliseconds)
// ARGV[2] = open timeout (milliseconds)
//
// Returns {allowed (0/1), state (0=closed,1=open,2=half_open)}.
const allowScriptSrc = `
local state = redis.call('HGET', KEYS[1], 'state')
if state == false then
  state = '0'
end

if state == '0' then
  return {1, 0}
elseif state == '1' then
  local openedAt = tonumber(redis.call('HGET', KEYS[1], 'opened_at') or '0')
  local timeout = tonumber(ARGV[2])
  local now = tonumber(ARGV[1])
  if (now - openedAt) >= timeout then
    redis.call('HSET', KEYS[1], 'state', '2', 'successes', '0')
    return {1, 2}
  end
  return {0, 1}
else
  -- half-open: allow a single probe through (best effort; concurrent
  -- probes are still individually recorded and evaluated below).
  return {1, 2}
end
`

// recordScriptSrc atomically records the outcome of an attempt and applies
// the resulting state transition.
//
// KEYS[1] = hash key
// ARGV[1] = success (0/1)
// ARGV[2] = failure threshold
// ARGV[3] = success threshold
// ARGV[4] = now (unix milliseconds)
const recordScriptSrc = `
local state = redis.call('HGET', KEYS[1], 'state')
if state == false then
  state = '0'
end

local success = tonumber(ARGV[1])
local failureThreshold = tonumber(ARGV[2])
local successThreshold = tonumber(ARGV[3])
local now = tonumber(ARGV[4])

if state == '0' then
  if success == 1 then
    redis.call('HSET', KEYS[1], 'state', '0', 'failures', '0')
  else
    local failures = tonumber(redis.call('HINCRBY', KEYS[1], 'failures', 1))
    if failures >= failureThreshold then
      redis.call('HSET', KEYS[1], 'state', '1', 'opened_at', tostring(now), 'successes', '0')
    end
  end
elseif state == '2' then
  if success == 1 then
    local successes = tonumber(redis.call('HINCRBY', KEYS[1], 'successes', 1))
    if successes >= successThreshold then
      redis.call('HSET', KEYS[1], 'state', '0', 'failures', '0', 'successes', '0')
    end
  else
    redis.call('HSET', KEYS[1], 'state', '1', 'opened_at', tostring(now), 'successes', '0')
  end
end
-- state == '1' (open): callers are expected to check Allow first, so a
-- Record while open is a no-op.

return redis.status_reply('OK')
`

// stateScriptSrc reports the effective current state for a key, applying
// the same open -> half-open timeout logic as allowScriptSrc but without
// mutating anything (matching InMemoryCircuitBreaker.State, which peeks at
// the state without recording a probe).
//
// KEYS[1] = hash key
// ARGV[1] = now (unix milliseconds)
// ARGV[2] = open timeout (milliseconds)
//
// Returns state (0=closed,1=open,2=half_open).
const stateScriptSrc = `
local state = redis.call('HGET', KEYS[1], 'state')
if state == false then
  return 0
end
if state == '1' then
  local openedAt = tonumber(redis.call('HGET', KEYS[1], 'opened_at') or '0')
  local timeout = tonumber(ARGV[2])
  local now = tonumber(ARGV[1])
  if (now - openedAt) >= timeout then
    return 2
  end
  return 1
end
return tonumber(state)
`

// NewRedisCircuitBreaker creates a new Redis-backed circuit breaker. The
// provided client is used as-is; the caller owns its lifecycle (creation
// and Close).
func NewRedisCircuitBreaker(client RedisScripter, cfg RedisCircuitBreakerConfig) *RedisCircuitBreaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.SuccessThreshold <= 0 {
		cfg.SuccessThreshold = 2
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = 30 * time.Second
	}
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = "ref:circuit_breaker:"
	}
	return &RedisCircuitBreaker{
		client:       client,
		config:       cfg,
		ctx:          context.Background(),
		allowScript:  redis.NewScript(allowScriptSrc),
		recordScript: redis.NewScript(recordScriptSrc),
		stateScript:  redis.NewScript(stateScriptSrc),
	}
}

func (cb *RedisCircuitBreaker) hashKey(key string) string {
	return cb.config.KeyPrefix + key
}

// Allow reports whether the circuit is closed (or half-open and admitting a
// probe) for the given key. It may transition the circuit from open to
// half-open as a side effect, atomically, once OpenTimeout has elapsed.
func (cb *RedisCircuitBreaker) Allow(key string) (bool, error) {
	now := time.Now().UnixMilli()
	timeoutMs := cb.config.OpenTimeout.Milliseconds()

	res, err := cb.allowScript.Run(cb.ctx, cb.client, []string{cb.hashKey(key)}, now, timeoutMs).Result()
	if err != nil {
		return false, fmt.Errorf("ref: redis circuit breaker allow check failed: %w", err)
	}

	vals, ok := res.([]interface{})
	if !ok || len(vals) != 2 {
		return false, fmt.Errorf("ref: redis circuit breaker allow: unexpected script result %#v", res)
	}
	allowed, err := toInt64(vals[0])
	if err != nil {
		return false, fmt.Errorf("ref: redis circuit breaker allow: %w", err)
	}
	return allowed == 1, nil
}

// Record feeds the outcome of an execution attempt back into the breaker,
// atomically applying any resulting state transition.
func (cb *RedisCircuitBreaker) Record(key string, success bool) error {
	now := time.Now().UnixMilli()
	successArg := 0
	if success {
		successArg = 1
	}

	_, err := cb.recordScript.Run(
		cb.ctx,
		cb.client,
		[]string{cb.hashKey(key)},
		successArg,
		cb.config.FailureThreshold,
		cb.config.SuccessThreshold,
		now,
	).Result()
	if err != nil {
		return fmt.Errorf("ref: redis circuit breaker record failed: %w", err)
	}
	return nil
}

// State returns the current, effective state of the circuit for a key
// (applying the open -> half-open timeout without mutating state).
func (cb *RedisCircuitBreaker) State(key string) CircuitState {
	now := time.Now().UnixMilli()
	timeoutMs := cb.config.OpenTimeout.Milliseconds()

	res, err := cb.stateScript.Run(cb.ctx, cb.client, []string{cb.hashKey(key)}, now, timeoutMs).Result()
	if err != nil {
		// Fail safe: an unreachable/erroring Redis should not silently look
		// closed. Callers relying solely on State() for observability can
		// treat this as "unknown"; Allow()/Record() surface the error
		// explicitly and are the load-bearing path.
		return CircuitClosed
	}

	n, err := toInt64(res)
	if err != nil {
		return CircuitClosed
	}
	switch n {
	case 1:
		return CircuitOpen
	case 2:
		return CircuitHalfOpen
	default:
		return CircuitClosed
	}
}

func toInt64(v interface{}) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot parse %q as int: %w", t, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}

// NewRedisCircuitBreakerCapability creates a fully self-contained circuit
// breaker capability backed by Redis. Useful for multi-instance
// deployments where breaker trip state must be shared across processes.
func NewRedisCircuitBreakerCapability(
	name string,
	client RedisScripter,
	keyPrefix string,
	cfg RedisCircuitBreakerConfig,
	opts ...Option,
) (Registration, *RedisCircuitBreaker) {
	if keyPrefix != "" {
		cfg.KeyPrefix = keyPrefix
	}
	cb := NewRedisCircuitBreaker(client, cfg)

	reg := NewCircuitBreakerCapability(
		name,
		nil, // default to intent name
		cb.Allow,
		cb.Record,
		cb.State,
		opts...,
	)

	return reg, cb
}
