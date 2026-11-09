-- reserve: give a Shopper a Hold on one unit of a LIVE Flash Sale.
-- KEYS[1] = the Flash Sale's counts hash
-- KEYS[2] = the Flash Sale's Shoppers hash: Shopper ID -> the ID of their Hold
-- KEYS[3] = the new Hold's hash
-- KEYS[4] = the Flash Sale's Hold expiries: Hold IDs scored by expiry time, which the reaper
--           scans for expired Holds (ADR-0002)
-- ARGV[1] = the Shopper's ID
-- ARGV[2] = the new Hold's ID
-- ARGV[3] = the Hold window, in milliseconds
-- Returns the Hold's expiry time in Unix milliseconds, on Redis server time, or the reason
-- there is no Hold: 'NOT_LIVE', {'ALREADY_HOLDING', <the ID of the Shopper's Hold>} or
-- 'SOLD_OUT'.
local time = redis.call('TIME')
local now = tonumber(time[1]) * 1000 + math.floor(tonumber(time[2]) / 1000)

-- LIVE means loaded and not yet at the end time. A Flash Sale that was never loaded hasn't
-- started.
local ends_at = tonumber(redis.call('HGET', KEYS[1], 'ends_at'))
if not ends_at or now >= ends_at then
  return 'NOT_LIVE'
end

-- One Hold per Shopper per Flash Sale. A Shopper's entry stays while their Hold is HELD,
-- PAYING or SOLD; releasing the Hold must remove it, which frees them to reserve again.
local existing = redis.call('HGET', KEYS[2], ARGV[1])
if existing then
  return {'ALREADY_HOLDING', existing}
end

if tonumber(redis.call('HGET', KEYS[1], 'available')) <= 0 then
  return 'SOLD_OUT'
end

local expires_at = now + tonumber(ARGV[3])
redis.call('HINCRBY', KEYS[1], 'available', -1)
redis.call('HINCRBY', KEYS[1], 'held', 1)
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
redis.call('HSET', KEYS[3], 'shopper', ARGV[1], 'state', 'HELD', 'expires_at', expires_at)
redis.call('ZADD', KEYS[4], expires_at, ARGV[2])
return expires_at
