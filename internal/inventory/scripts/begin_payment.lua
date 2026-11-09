-- begin_payment: move a Shopper's HELD Hold to PAYING, so the reaper leaves it alone while it
-- is being charged (ADR-0002).
-- KEYS[1] = the Flash Sale's counts hash
-- KEYS[2] = the Flash Sale's Holds hash (see reserve.lua)
-- ARGV[1] = the Hold's ID
-- ARGV[2] = the ID of the Shopper paying
-- Returns the Hold's expiry time in Unix milliseconds, or why it can't be paid for:
-- 'NOT_FOUND', or {'NOT_HELD', <state>} when the Hold has already left HELD.
local id = ARGV[1]
local hold = redis.call('HMGET', KEYS[2], id .. ':shopper', id .. ':state', id .. ':expires_at')

-- Another Shopper's Hold looks the same as no Hold at all, so Hold IDs reveal nothing.
if hold[1] ~= ARGV[2] then
  return 'NOT_FOUND'
end

if hold[2] ~= 'HELD' then
  return {'NOT_HELD', hold[2]}
end

redis.call('HSET', KEYS[2], id .. ':state', 'PAYING')
redis.call('HINCRBY', KEYS[1], 'held', -1)
redis.call('HINCRBY', KEYS[1], 'paying', 1)
return tonumber(hold[3])
