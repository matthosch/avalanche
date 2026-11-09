-- reserve: give a Shopper a Hold on one unit of a LIVE Flash Sale.
-- KEYS[1] = the Flash Sale's counts hash
-- KEYS[2] = the Flash Sale's Shoppers hash: Shopper ID -> the ID of their latest Hold
-- KEYS[3] = the Flash Sale's Holds hash: '<Hold ID>:shopper', '<Hold ID>:state' and
--           '<Hold ID>:expires_at' for every Hold, so any script can reach any Hold through
--           keys it is given
-- KEYS[4] = the Flash Sale's Hold expiries: Hold IDs scored by expiry time, which the reaper
--           scans for expired Holds (ADR-0002)
-- ARGV[1] = the Shopper's ID
-- ARGV[2] = the new Hold's ID
-- ARGV[3] = the Hold window, in milliseconds
-- Returns the new Hold's expiry time in Unix milliseconds, on Redis server time, or why there
-- is no new Hold: {'ALREADY_HOLDING', <Hold ID>, <state>, <expires_at>} with the Shopper's
-- existing Hold, 'NOT_LIVE' or 'SOLD_OUT'.

-- One Hold per Shopper per Flash Sale, counting HELD, PAYING and SOLD Holds. The refusal hands
-- back that Hold, read here so it is exactly the Hold that blocked the reserve. This comes
-- before the LIVE check: a Hold made before the end can still be paid until it expires, so a
-- Shopper who reloads or lost the first reply still gets it back after the end.
local existing = redis.call('HGET', KEYS[2], ARGV[1])
if existing then
  local hold = redis.call('HMGET', KEYS[3], existing .. ':state', existing .. ':expires_at')
  local state = hold[1]
  if state == 'HELD' or state == 'PAYING' or state == 'SOLD' then
    return {'ALREADY_HOLDING', existing, state, hold[2]}
  end
  -- A RELEASED Hold frees the Shopper to reserve again.
end

local time = redis.call('TIME')
local now = tonumber(time[1]) * 1000 + math.floor(tonumber(time[2]) / 1000)

-- LIVE means loaded and not yet at the end time. A Flash Sale that was never loaded hasn't
-- started.
local ends_at = tonumber(redis.call('HGET', KEYS[1], 'ends_at'))
if not ends_at or now >= ends_at then
  return 'NOT_LIVE'
end

if tonumber(redis.call('HGET', KEYS[1], 'available')) <= 0 then
  return 'SOLD_OUT'
end

local id = ARGV[2]
local expires_at = now + tonumber(ARGV[3])
redis.call('HINCRBY', KEYS[1], 'available', -1)
redis.call('HINCRBY', KEYS[1], 'held', 1)
redis.call('HSET', KEYS[2], ARGV[1], id)
redis.call('HSET', KEYS[3], id .. ':shopper', ARGV[1], id .. ':state', 'HELD', id .. ':expires_at', expires_at)
redis.call('ZADD', KEYS[4], expires_at, id)
return expires_at
